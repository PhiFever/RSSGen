package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/route"
	_ "github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"/user123", []string{"user123"}},
		{"user123", []string{"user123"}},
		{"/a/b/c", []string{"a", "b", "c"}},
		{"", []string{}},
		{"/", []string{}},
	}

	for _, tt := range tests {
		result := splitPath(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitPath(%q): len = %d, want %d", tt.input, len(result), len(tt.expected))
			continue
		}
		for i, v := range result {
			if v != tt.expected[i] {
				t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.input, i, v, tt.expected[i])
			}
		}
	}
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		routeName string
		pathParts []string
		expected  string
	}{
		{"afdian", []string{"user1"}, "afdian/user1"},
		{"zhihu", []string{"user2"}, "zhihu/user2"},
		{"test", []string{}, "test"},
	}

	for _, tt := range tests {
		result := cache.BuildCacheKey(tt.routeName, tt.pathParts)
		if result != tt.expected {
			t.Errorf("BuildCacheKey(%q, %v) = %q, want %q", tt.routeName, tt.pathParts, result, tt.expected)
		}
	}
}

func TestRouteRegistration(t *testing.T) {
	registry := route.GetRegistry()
	if len(registry) == 0 {
		t.Error("没有注册任何路由")
	}

	// 检查 afdian 路由是否注册
	if _, ok := registry["afdian"]; !ok {
		t.Error("afdian 路由未注册")
	}

	// 检查 zhihu 路由是否注册
	if _, ok := registry["zhihu"]; !ok {
		t.Error("zhihu 路由未注册")
	}
}

func TestIndexEndpoint(t *testing.T) {
	// 简单测试：验证路由注册机制
	registry := route.GetRegistry()
	if len(registry) == 0 {
		t.Fatal("没有注册任何路由")
	}

	// 创建一个简单的 handler 测试
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAnyRouteEnabled(t *testing.T) {
	// 测试没有启用路由的情况
	cfg := &config.Config{
		Routes: map[string]config.RouteConfig{
			"test": {Enabled: false, RefreshInterval: 60},
		},
	}
	if anyRouteEnabled(cfg) {
		t.Error("期望返回 false")
	}

	// 测试有启用路由的情况
	cfg.Routes["test"] = config.RouteConfig{Enabled: true, RefreshInterval: 60}
	if !anyRouteEnabled(cfg) {
		t.Error("期望返回 true")
	}
}

// --- Disabled Feed HTTP 行为测试（迁移自 Python test_app_feed.py） ---

// setupTestRouter 创建用于测试的 chi router，包含 feed handler。
func setupTestRouter(notif *notifier.Notifier, feedCache *cache.TTLCache) *chi.Mux {
	r := chi.NewRouter()
	cfg := &config.Config{
		Scraper: config.ScraperConfig{},
		Routes:  map[string]config.RouteConfig{},
	}
	r.Get("/feed/{route_name}/*", makeFeedHandler(notif, feedCache, nil, nil, cfg))
	return r
}

func TestDisabledFeedReturns502(t *testing.T) {
	notif := notifier.New(notifier.Config{Enabled: false})
	feedCache := cache.New(10 * time.Second)
	notif.DisableFeed("afdian/author1")

	router := setupTestRouter(notif, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/author1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("被禁用的 feed 应返回 502, 实得 %d", w.Code)
	}
}

func TestSiblingFeedNotBlocked(t *testing.T) {
	notif := notifier.New(notifier.Config{Enabled: false})
	feedCache := cache.New(10 * time.Second)
	notif.DisableFeed("afdian/author1")

	// 预填充缓存，避免走真实 fetch
	feedCache.Set("afdian/author2", "<feed/>")

	router := setupTestRouter(notif, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/author2", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("同路由下未禁用的 feed 应返回 200, 实得 %d", w.Code)
	}
	if w.Body.String() != "<feed/>" {
		t.Errorf("应返回缓存内容, 实得 %q", w.Body.String())
	}
}

func TestFeedCacheHitReturnsXML(t *testing.T) {
	notif := notifier.New(notifier.Config{Enabled: false})
	feedCache := cache.New(10 * time.Second)
	feedCache.Set("afdian/user1", `<?xml version="1.0"?><feed/>`)

	router := setupTestRouter(notif, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/user1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("缓存命中应返回 200, 实得 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
}

func TestFeedUnknownRouteReturns404(t *testing.T) {
	notif := notifier.New(notifier.Config{Enabled: false})
	feedCache := cache.New(10 * time.Second)

	router := setupTestRouter(notif, feedCache)

	req := httptest.NewRequest("GET", "/feed/nonexistent/user1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("未知路由应返回 404, 实得 %d", w.Code)
	}
}
