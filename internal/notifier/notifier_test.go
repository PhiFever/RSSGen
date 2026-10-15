package notifier

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ntfyTestURL(server *httptest.Server) string {
	return "ntfy://" + strings.TrimPrefix(server.URL, "http://") + "/RSSGen?scheme=http&token=tk_test"
}

func TestNewDefault(t *testing.T) {
	n, err := New(Config{})
	if err != nil {
		t.Fatalf("创建默认通知器失败: %v", err)
	}
	n.Notify("key", 403, "业务错误")
}

func TestNotifyServiceURL(t *testing.T) {
	type requestInfo struct {
		method, path, authorization, body string
		err                               error
	}
	received := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"t","event":"message"}`))
		received <- requestInfo{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body), err}
	}))
	defer server.Close()
	n, err := New(Config{Enabled: true, ServiceURLs: []string{ntfyTestURL(server)}})
	if err != nil {
		t.Fatalf("创建通知器失败: %v", err)
	}
	n.Notify("key", 403, "业务错误")
	select {
	case req := <-received:
		if req.err != nil {
			t.Fatalf("读取请求失败: %v", req.err)
		}
		if req.method != http.MethodPost || req.path != "/RSSGen" {
			t.Errorf("请求 = %s %s，期望 POST /RSSGen", req.method, req.path)
		}
		if req.authorization != "Bearer tk_test" {
			t.Errorf("Authorization = %q", req.authorization)
		}
		if !strings.Contains(req.body, "[RSSGen] 订阅源 key 获取失败\n状态码: 403\n错误: 业务错误\n时间: ") {
			t.Errorf("通知正文 = %q", req.body)
		}
	case <-time.After(time.Second):
		t.Fatal("service_urls 目标未收到通知")
	}
}

func TestNewInvalidURL(t *testing.T) {
	_, err := New(Config{Enabled: true, ServiceURLs: []string{
		"ntfy://ntfy.example.com/RSSGen?token=tk_test",
		"unknownsvc://x?token=secret123",
	}})
	if err == nil {
		t.Fatal("非法 URL 应返回错误")
	}
	if !strings.Contains(err.Error(), "URL 1") || strings.Contains(err.Error(), "secret123") {
		t.Errorf("错误应含 URL 序号且不泄露密钥: %v", err)
	}
}

func TestNotifyDisabled(t *testing.T) {
	if _, err := New(Config{Enabled: false, ServiceURLs: []string{"unknownsvc://x?token=secret123"}}); err != nil {
		t.Fatalf("关闭通知时不应校验 URL: %v", err)
	}
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"t","event":"message"}`))
		received <- struct{}{}
	}))
	defer server.Close()
	n, err := New(Config{Enabled: false, ServiceURLs: []string{ntfyTestURL(server)}})
	if err != nil {
		t.Fatalf("创建关闭的通知器失败: %v", err)
	}
	n.Notify("key", 403, "业务错误")
	select {
	case <-received:
		t.Fatal("关闭通知时不应发送请求")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSendNotificationExceptionHandled(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		received <- struct{}{}
	}))
	defer server.Close()
	n, err := New(Config{Enabled: true, ServiceURLs: []string{ntfyTestURL(server)}})
	if err != nil {
		t.Fatalf("创建通知器失败: %v", err)
	}
	// 远端返回错误时，异步通知不应 panic。
	n.Notify("key", 403, "业务错误")
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("测试服务器未收到请求")
	}
	time.Sleep(100 * time.Millisecond)
}
