package scraper

import (
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
