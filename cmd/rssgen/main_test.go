package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/refresher"
	"github.com/PhiFever/RSSGen/internal/route"
	_ "github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
	"github.com/PhiFever/RSSGen/internal/store"
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
	route.Register("_main_index", func(config.ResolvedRouteConfig) route.Route {
		return &mainTestRoute{name: "_main_index"}
	})
	defer route.Unregister("_main_index")

	cfg := &config.Config{Routes: map[string]config.RouteConfig{}}
	handler := makeRouter(notifier.New(notifier.Config{}), cache.New(10*time.Second), nil, nil, cfg)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
	var body struct {
		Name   string            `json:"name"`
		Routes map[string]string `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("首页响应不是 JSON: %v", err)
	}
	if body.Name != "RSSGen" || body.Routes["_main_index"] != "main test route" {
		t.Fatalf("首页响应 = %+v", body)
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

	cfg.Routes["test"] = config.RouteConfig{Enabled: true, PreheatOnStartup: true}
	if !anyRouteEnabled(cfg) {
		t.Error("只启用预热的路由也应启动后台刷新器")
	}
}

func TestBuildRuntime(t *testing.T) {
	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Storage: config.StorageConfig{SQLitePath: filepath.Join(t.TempDir(), "rssgen.db")},
		Cache:   config.CacheConfig{FeedTTL: 60},
		Notifier: config.NotifierConfig{
			Enabled: true,
			Services: []config.NotifierServiceConfig{{
				Type:       "feishu",
				WebhookURL: "https://example.com/hook",
			}},
		},
		Routes: map[string]config.RouteConfig{
			"_disabled": {Enabled: false},
		},
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		t.Fatalf("buildRuntime 返回错误: %v", err)
	}
	defer func() {
		if err := app.articleStore.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if app.server == nil || app.server.Handler == nil {
		t.Fatal("buildRuntime 应创建 HTTP server 和 handler")
	}
	if app.server.Addr != "127.0.0.1:0" {
		t.Fatalf("server addr = %q", app.server.Addr)
	}
	if app.refresher != nil {
		t.Fatal("没有启用调度的路由时不应创建 refresher")
	}

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	app.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runtime router /status = %d", w.Code)
	}
}

func TestBuildRuntimeStartsRefresherWhenRouteScheduled(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Storage:   config.StorageConfig{SQLitePath: filepath.Join(t.TempDir(), "rssgen.db")},
		Cache:     config.CacheConfig{FeedTTL: 60},
		Refresher: config.RefresherConfig{StartupDelay: 1},
		Routes: map[string]config.RouteConfig{
			"_scheduled": {Enabled: true, RefreshInterval: 60},
		},
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		t.Fatalf("buildRuntime 返回错误: %v", err)
	}
	defer func() {
		if err := app.articleStore.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if app.refresher == nil {
		t.Fatal("启用后台调度的路由应创建 refresher")
	}
	app.refresher.Stop()
}

func TestBuildRuntimeStoreInitError(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("写入父级文件失败: %v", err)
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Storage: config.StorageConfig{SQLitePath: filepath.Join(parentFile, "db")},
		Cache:   config.CacheConfig{FeedTTL: 60},
		Routes:  map[string]config.RouteConfig{},
	}
	if _, err := buildRuntime(cfg); err == nil {
		t.Fatal("存储初始化失败时 buildRuntime 应返回错误")
	}
}

func TestStatusHandlerIncludesFeeds(t *testing.T) {
	ref := refresher.New(refresher.Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	makeStatusHandler(ref).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("状态响应不是 JSON: %v", err)
	}
	if body["enabled"] != true {
		t.Fatalf("enabled = %v, want true", body["enabled"])
	}
	if _, ok := body["feeds"]; !ok {
		t.Fatal("/status 应包含 feeds 字段")
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

type mainTestRoute struct {
	name        string
	fetchErr    error
	infoErr     error
	info        *route.FeedInfo
	items       []route.FeedItem
	capturedOpt route.FetchOptions
}

func (r *mainTestRoute) Name() string        { return r.name }
func (r *mainTestRoute) Description() string { return "main test route" }
func (r *mainTestRoute) FeedIDField() string { return "user_id" }
func (r *mainTestRoute) FeedInfo([]string) (*route.FeedInfo, error) {
	if r.infoErr != nil {
		return nil, r.infoErr
	}
	if r.info != nil {
		return r.info, nil
	}
	return &route.FeedInfo{Title: "Test", Link: "https://example.com"}, nil
}
func (r *mainTestRoute) Fetch(_ route.ArticleStore, _ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
	r.capturedOpt = opts
	if r.fetchErr != nil {
		return nil, r.fetchErr
	}
	if r.items != nil {
		return r.items, nil
	}
	return []route.FeedItem{{Title: "Item", Link: "https://example.com/1"}}, nil
}

func TestFeedHandlerSyncFetchSuccessAndQueryParams(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_success"}
	route.Register("_main_success", func(config.ResolvedRouteConfig) route.Route { return testRoute })
	defer route.Unregister("_main_success")

	notif := notifier.New(notifier.Config{})
	feedCache := cache.New(10 * time.Second)
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_success": {Enabled: true}}}
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeFeedHandler(notif, feedCache, nil, (*store.ArticleStore)(nil), cfg))

	req := httptest.NewRequest("GET", "/feed/_main_success/u1?limit=7&include=a,b&format=rss", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", w.Code, w.Body.String())
	}
	if testRoute.capturedOpt.Limit != 7 {
		t.Fatalf("Limit = %d", testRoute.capturedOpt.Limit)
	}
	if len(testRoute.capturedOpt.Include) != 2 || testRoute.capturedOpt.Include[0] != "a" {
		t.Fatalf("Include = %+v", testRoute.capturedOpt.Include)
	}
	if cached, ok := feedCache.Get("_main_success/u1"); !ok || cached == "" {
		t.Fatal("同步抓取成功后应写入缓存")
	}
}

func TestFeedHandlerFetchAndInfoErrors(t *testing.T) {
	t.Run("fetch error", func(t *testing.T) {
		testRoute := &mainTestRoute{name: "_main_fetch_err", fetchErr: errors.New("boom")}
		route.Register("_main_fetch_err", func(config.ResolvedRouteConfig) route.Route { return testRoute })
		defer route.Unregister("_main_fetch_err")

		cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_fetch_err": {Enabled: true}}}
		r := chi.NewRouter()
		r.Get("/feed/{route_name}/*", makeFeedHandler(notifier.New(notifier.Config{}), cache.New(10*time.Second), nil, (*store.ArticleStore)(nil), cfg))

		req := httptest.NewRequest("GET", "/feed/_main_fetch_err/u1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("Fetch 错误应返回 502，got %d", w.Code)
		}
	})

	t.Run("feed info error", func(t *testing.T) {
		testRoute := &mainTestRoute{name: "_main_info_err", infoErr: errors.New("bad info")}
		route.Register("_main_info_err", func(config.ResolvedRouteConfig) route.Route { return testRoute })
		defer route.Unregister("_main_info_err")

		cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_info_err": {Enabled: true}}}
		r := chi.NewRouter()
		r.Get("/feed/{route_name}/*", makeFeedHandler(notifier.New(notifier.Config{}), cache.New(10*time.Second), nil, (*store.ArticleStore)(nil), cfg))

		req := httptest.NewRequest("GET", "/feed/_main_info_err/u1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("FeedInfo 错误应返回 500，got %d", w.Code)
		}
	})
}

func TestStatusHandlerDisabled(t *testing.T) {
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	makeStatusHandler(nil).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("状态响应不是 JSON: %v", err)
	}
	if body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", body["enabled"])
	}
}
