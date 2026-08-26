package scraper

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestNewDefaultConfig(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	if s == nil {
		t.Fatal("New 返回 nil")
	}
	// 默认值
	if s.rateLimit != time.Second {
		t.Errorf("rateLimit = %v, want 1s", s.rateLimit)
	}
	if s.impersonate != "chrome_131" {
		t.Errorf("impersonate = %q, want %q", s.impersonate, "chrome_131")
	}
	if s.transportAttempts != 2 {
		t.Errorf("transportAttempts = %d, want 2", s.transportAttempts)
	}
}

func TestNewCustomConfig(t *testing.T) {
	s, err := New(Config{
		RateLimit:         2.5,
		Impersonate:       "chrome_120",
		Proxy:             "http://proxy:8080",
		Cookies:           map[string]string{"key": "val"},
		TransportAttempts: 1,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	if s.rateLimit != time.Duration(2.5*float64(time.Second)) {
		t.Errorf("rateLimit = %v, want 2.5s", s.rateLimit)
	}
	if s.impersonate != "chrome_120" {
		t.Errorf("impersonate = %q, want %q", s.impersonate, "chrome_120")
	}
	if s.proxy != "http://proxy:8080" {
		t.Errorf("proxy = %q, want %q", s.proxy, "http://proxy:8080")
	}
	if s.cookies["key"] != "val" {
		t.Errorf("cookies[key] = %q, want %q", s.cookies["key"], "val")
	}
	if s.transportAttempts != 1 {
		t.Errorf("transportAttempts = %d, want 1", s.transportAttempts)
	}
}

func TestNewUnknownProfile(t *testing.T) {
	// 未知 profile 应降级到默认
	s, err := New(Config{Impersonate: "unknown_profile"})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	if s.impersonate != "unknown_profile" {
		t.Errorf("impersonate = %q, want %q", s.impersonate, "unknown_profile")
	}
}

func TestRateLimitWait(t *testing.T) {
	s, err := New(Config{RateLimit: 0.1}) // 100ms
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	// 第一次请求不应等待
	start := time.Now()
	s.rateLimitWait()
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("第一次请求等待了 %v, 应该几乎不等待", elapsed)
	}

	// 立即再次请求应等待
	start = time.Now()
	s.rateLimitWait()
	elapsed = time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Errorf("第二次请求等待了 %v, 应该等待约 100ms", elapsed)
	}
}

func TestSetCookies(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	cookies := map[string]string{"a": "1", "b": "2"}
	s.SetCookies(cookies)
	cookies["a"] = "changed"
	if s.cookies["a"] != "1" || s.cookies["b"] != "2" {
		t.Errorf("cookies = %v, 未正确设置", s.cookies)
	}
}

func TestCookieHeaderUsesSnapshot(t *testing.T) {
	cookies := map[string]string{"b": "2", "a": "1"}
	s, err := New(Config{Cookies: cookies})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	cookies["a"] = "changed"

	if got := s.cookieHeader(); got != "a=1; b=2" {
		t.Fatalf("cookieHeader = %q, want stable sorted snapshot", got)
	}
}

func TestResponseText(t *testing.T) {
	r := &Response{
		StatusCode: 200,
		Body:       []byte("hello world"),
	}
	if r.Text() != "hello world" {
		t.Errorf("Text() = %q, want %q", r.Text(), "hello world")
	}
}

func TestGetSendsHeadersCookiesAndReadsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Referer"); got != "https://referer.example/" {
			t.Errorf("Referer = %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "request" {
			t.Errorf("X-Test = %q", got)
		}
		if got := r.Header.Get("X-Extra"); got != "instance" {
			t.Errorf("X-Extra = %q", got)
		}
		cookie, err := r.Cookie("sid")
		if err != nil || cookie.Value != "abc" {
			t.Errorf("sid cookie = %v, %v", cookie, err)
		}
		w.Header().Set("X-Reply", "ok")
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte("hello")); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s, err := New(Config{
		RateLimit:    0.001,
		Cookies:      map[string]string{"sid": "abc"},
		ExtraHeaders: map[string]string{"X-Extra": "instance"},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	resp, err := s.Get(server.URL, "https://referer.example/", map[string]string{"X-Test": "request"})
	if err != nil {
		t.Fatalf("Get 返回错误: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Reply") != "ok" {
		t.Fatalf("响应 header 未保留")
	}
	if resp.Text() != "hello" {
		t.Fatalf("Text() = %q", resp.Text())
	}
}

func TestGetRecoversWhenALPNChanges(t *testing.T) {
	serverURL := newALPNSwitchingServer(t)
	s, err := New(Config{RateLimit: 0.001})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	for requestNumber := 1; requestNumber <= 2; requestNumber++ {
		resp, err := s.Get(serverURL, "", nil)
		if err != nil {
			t.Fatalf("第 %d 次请求失败: %v", requestNumber, err)
		}
		if resp.StatusCode != http.StatusOK || resp.Text() != "ok" {
			t.Fatalf("第 %d 次响应 = (%d, %q), want (200, %q)", requestNumber, resp.StatusCode, resp.Text(), "ok")
		}
	}
}

func TestGetRecoversAfterConnectionReset(t *testing.T) {
	serverURL := newConnectionResetServer(t)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	s, err := New(Config{RateLimit: 0.001})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	resp, err := s.Get(serverURL+"?token=do-not-log", "", nil)
	if err != nil {
		t.Fatalf("请求未从连接重置中恢复: %v", err)
	}
	if resp.StatusCode != http.StatusOK || resp.Text() != "ok" {
		t.Fatalf("响应 = (%d, %q), want (200, %q)", resp.StatusCode, resp.Text(), "ok")
	}
	if strings.Contains(logs.String(), "do-not-log") {
		t.Fatalf("自愈日志泄漏了完整 URL: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "host=127.0.0.1 category=connection_reset") {
		t.Fatalf("自愈日志缺少主机或错误类别: %s", logs.String())
	}
}

func newALPNSwitchingServer(t *testing.T) string {
	t.Helper()

	certificateSource := httptest.NewTLSServer(nil)
	certificate := certificateSource.TLS.Certificates[0]
	certificateSource.Close()
	trustTestCertificate(t, certificate)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听测试端口失败: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var handshakes atomic.Int32
	serverTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			protocol := "http/1.1"
			if handshakes.Add(1) > 1 {
				protocol = "h2"
			}
			return &tls.Config{
				Certificates: []tls.Certificate{certificate},
				NextProtos:   []string{protocol},
			}, nil
		},
	}

	go serveALPNSwitchingConnections(listener, serverTLSConfig)
	return "https://" + listener.Addr().String() + "/"
}

func newConnectionResetServer(t *testing.T) string {
	t.Helper()

	certificateSource := httptest.NewTLSServer(nil)
	certificate := certificateSource.TLS.Certificates[0]
	certificateSource.Close()
	trustTestCertificate(t, certificate)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听测试端口失败: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		connectionNumber := 0
		for {
			rawConn, err := listener.Accept()
			if err != nil {
				return
			}
			connectionNumber++
			if connectionNumber == 1 {
				if tcpConn, ok := rawConn.(*net.TCPConn); ok {
					_ = tcpConn.SetLinger(0)
				}
				_ = rawConn.Close()
				continue
			}

			conn := tls.Server(rawConn, &tls.Config{
				Certificates: []tls.Certificate{certificate},
				NextProtos:   []string{"http/1.1"},
			})
			if err := conn.Handshake(); err != nil {
				_ = conn.Close()
				continue
			}
			serveOneHTTP1Request(conn)
		}
	}()

	return "https://" + listener.Addr().String() + "/"
}

func trustTestCertificate(t *testing.T, certificate tls.Certificate) {
	t.Helper()

	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("解析测试证书失败: %v", err)
	}
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parsed.Raw})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("写入测试 CA 失败: %v", err)
	}
	t.Setenv("SSL_CERT_FILE", certFile)
}

func serveALPNSwitchingConnections(listener net.Listener, tlsConfig *tls.Config) {
	for {
		rawConn, err := listener.Accept()
		if err != nil {
			return
		}
		conn := tls.Server(rawConn, tlsConfig)
		if err := conn.Handshake(); err != nil {
			_ = conn.Close()
			continue
		}

		if conn.ConnectionState().NegotiatedProtocol == "http/1.1" {
			serveOneHTTP1Request(conn)
			continue
		}

		server := &http2.Server{}
		server.ServeConn(conn, &http2.ServeConnOpts{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			}),
			Context: nil,
			BaseConfig: &http.Server{
				ErrorLog: log.New(io.Discard, "", 0),
			},
		})
	}
}

func serveOneHTTP1Request(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
}

func TestRequestInvalidURLReturnsError(t *testing.T) {
	s, err := New(Config{RateLimit: 0.001})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	if _, err := s.Get("://bad-url", "", nil); err == nil {
		t.Fatal("非法 URL 应返回错误")
	}
}
