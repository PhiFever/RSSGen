package scraper

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
}

func TestNewCustomConfig(t *testing.T) {
	s, err := New(Config{
		RateLimit:   2.5,
		Impersonate: "chrome_120",
		Proxy:       "http://proxy:8080",
		Cookies:     map[string]string{"key": "val"},
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

	s.SetCookies(map[string]string{"a": "1", "b": "2"})
	if s.cookies["a"] != "1" || s.cookies["b"] != "2" {
		t.Errorf("cookies = %v, 未正确设置", s.cookies)
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
		w.Write([]byte("hello"))
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

func TestPostAndPostJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/post":
			if r.Method != http.MethodPost || string(body) != "raw-body" {
				t.Fatalf("raw POST = %s %q", r.Method, string(body))
			}
			w.Write([]byte("posted"))
		case "/json":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("JSON body 无效: %v", err)
			}
			if payload["k"] != "v" || r.Header.Get("X-Mode") != "json" {
				t.Fatalf("JSON 请求未保留 body/header")
			}
			w.Write([]byte("json"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s, err := New(Config{RateLimit: 0.001})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	resp, err := s.Post(server.URL+"/post", "", strings.NewReader("raw-body"), nil)
	if err != nil {
		t.Fatalf("Post 返回错误: %v", err)
	}
	if resp.Text() != "posted" {
		t.Fatalf("Post response = %q", resp.Text())
	}

	resp, err = s.PostJSON(server.URL+"/json", "", `{"k":"v"}`, map[string]string{"X-Mode": "json"})
	if err != nil {
		t.Fatalf("PostJSON 返回错误: %v", err)
	}
	if resp.Text() != "json" {
		t.Fatalf("PostJSON response = %q", resp.Text())
	}
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
