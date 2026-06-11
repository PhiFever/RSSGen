package refresher

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/route"
)

func TestNew(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})

	cfg := Config{
		FeedCache:      feedCache,
		ArticleStore:   nil,
		Notifier:       notif,
		StartupDelay:   1,
		MaxRetries:     2,
		RetryBaseDelay: 1,
		RoutesConfig:   map[string]config.RouteConfig{},
	}

	ref := New(cfg)
	if ref == nil {
		t.Fatal("New 返回 nil")
	}
	if ref.startupDelay != 1 {
		t.Errorf("startupDelay = %d, want 1", ref.startupDelay)
	}
	if ref.maxRetries != 2 {
		t.Errorf("maxRetries = %d, want 2", ref.maxRetries)
	}
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		routeName  string
		pathParams []string
		expected   string
	}{
		{"afdian", []string{"user1"}, "afdian/user1"},
		{"zhihu", []string{"user2"}, "zhihu/user2"},
		{"test", []string{"a", "b"}, "test/a/b"},
	}

	for _, tt := range tests {
		result := cache.BuildCacheKey(tt.routeName, tt.pathParams)
		if result != tt.expected {
			t.Errorf("BuildCacheKey(%q, %v) = %q, want %q", tt.routeName, tt.pathParams, result, tt.expected)
		}
	}
}

func TestSplitCacheKey(t *testing.T) {
	tests := []struct {
		key            string
		expectedRoute  string
		expectedFeedID string
	}{
		{"afdian/user1", "afdian", "user1"},
		{"zhihu/user2", "zhihu", "user2"},
		{"test/a/b", "test", "a/b"},
		{"single", "single", ""},
	}

	for _, tt := range tests {
		routeName, feedID := splitCacheKey(tt.key)
		if routeName != tt.expectedRoute {
			t.Errorf("splitCacheKey(%q): routeName = %q, want %q", tt.key, routeName, tt.expectedRoute)
		}
		if feedID != tt.expectedFeedID {
			t.Errorf("splitCacheKey(%q): feedID = %q, want %q", tt.key, feedID, tt.expectedFeedID)
		}
	}
}

func TestExtractStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{"裸 HTTPError", &route.HTTPError{StatusCode: 403}, 403},
		// 回归 bug #1：路由抛出的 HTTPError 会被多层 fmt.Errorf 包裹，
		// 旧的字符串解析在此恒返回 0，导致风控禁用从不触发。
		{"包裹后的 HTTPError", fmt.Errorf("获取知乎动态失败: %w", &route.HTTPError{StatusCode: 404}), 404},
		{"普通错误", errors.New("some other error"), 0},
		{"nil", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := extractStatusCode(tt.err); result != tt.expected {
				t.Errorf("extractStatusCode(%v) = %d, want %d", tt.err, result, tt.expected)
			}
		})
	}
}

// --- Trigger（迁移自 Python TestTrigger） ---

func TestTriggerCreatesTask(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	// Trigger 非阻塞，不应 panic
	ref.Trigger("zhihu", []string{"user1"}, nil)

	// pending 应有标记（goroutine 可能已完成，但 Trigger 本身设置 pending）
	// 这里只验证不 panic
}

func TestTriggerDedup(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	// 设置 pending 模拟已有任务在跑
	ref.pendingMu.Lock()
	ref.pending["zhihu/user1"] = true
	ref.pendingMu.Unlock()

	// 第二次 Trigger 应被去重（不启动新 goroutine）
	ref.Trigger("zhihu", []string{"user1"}, nil)
	// 不 panic 即为通过
}

func TestTriggerSkipsDisabledFeed(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	notif.DisableFeed("zhihu/user1")
	// 被禁用的 feed 不应启动刷新
	ref.Trigger("zhihu", []string{"user1"}, nil)
	// 不 panic 即为通过；pending 应被清理
	ref.pendingMu.Lock()
	isPending := ref.pending["zhihu/user1"]
	ref.pendingMu.Unlock()
	if isPending {
		t.Error("被禁用的 feed 不应留在 pending 中")
	}
}

// --- GetStatus（迁移自 Python TestGetStatus） ---

func TestGetStatusReturnsRouteGroupedDict(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	// 手动写入 errorStats
	ref.statsMu.Lock()
	ref.errorStats["zhihu/user1"] = &ErrorStatus{ItemCount: 5, LastSuccess: "2025-01-01T00:00:00Z"}
	ref.errorStats["afdian/user2"] = &ErrorStatus{Error: "timeout"}
	ref.statsMu.Unlock()

	status := ref.GetStatus()

	if len(status) != 2 {
		t.Errorf("应有 2 个路由分组, 实得 %d", len(status))
	}
	if status["zhihu"] == nil || status["zhihu"]["user1"] == nil {
		t.Error("应包含 zhihu/user1")
	}
	if status["zhihu"]["user1"].ItemCount != 5 {
		t.Errorf("ItemCount = %d, want 5", status["zhihu"]["user1"].ItemCount)
	}
	if status["afdian"] == nil || status["afdian"]["user2"] == nil {
		t.Error("应包含 afdian/user2")
	}
	if status["afdian"]["user2"].Error != "timeout" {
		t.Errorf("Error = %q, want 'timeout'", status["afdian"]["user2"].Error)
	}
}

func TestGetStatusEmpty(t *testing.T) {
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	})
	status := ref.GetStatus()
	if len(status) != 0 {
		t.Errorf("空状态应返回空 map, 实得 %d 个路由", len(status))
	}
}

// --- Start / Stop 生命周期（迁移自 Python TestStartStop） ---

func TestStartCreatesPerRouteTasks(t *testing.T) {
	// 注册一个 mock route factory
	route.Register("_test_refresher", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher"}
	})
	defer route.Unregister("_test_refresher")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_refresher": {
				Enabled:         true,
				RefreshInterval: 60,
				Feeds:           []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	// 等一下让 goroutine 启动
	time.Sleep(50 * time.Millisecond)
	ref.Stop()
	// 不 panic、不 hang 即为通过
}

func TestStartPreinitURL(t *testing.T) {
	hit := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		PreinitURL:   server.URL,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.Start()
	ref.Stop()

	select {
	case <-hit:
	default:
		t.Fatal("Start 应按 preinit_url 预初始化 HTTP 客户端")
	}
}

func TestPreinitHTTPClientSkipsAndHandlesFailures(t *testing.T) {
	New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	}).preinitHTTPClient()

	errorRef := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		PreinitURL:   "http://127.0.0.1:1",
		RoutesConfig: map[string]config.RouteConfig{},
	})
	errorRef.preinitHTTPClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	statusRef := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		PreinitURL:   server.URL,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	statusRef.preinitHTTPClient()
}

func TestRefreshFeedsSkipsEmptyAndInvalidFeedConfig(t *testing.T) {
	route.Register("_test_refresh_feeds_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_refresh_feeds_skip",
			fetchFn: func(route.ArticleStore, []string, route.FetchOptions) ([]route.FeedItem, error) {
				t.Fatal("空 feeds 或缺 user_id 时不应调用 Fetch")
				return nil, nil
			},
		}
	})
	defer route.Unregister("_test_refresh_feeds_skip")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.refreshFeeds("test", "_test_refresh_feeds_skip", config.RouteConfig{})
	ref.refreshFeeds("test", "_test_refresh_feeds_skip", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: ""}},
	})
}

func TestStopCancelsAllTasks(t *testing.T) {
	route.Register("_test_refresher2", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher2"}
	})
	defer route.Unregister("_test_refresher2")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_refresher2": {
				Enabled:         true,
				RefreshInterval: 1,
				Feeds:           []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(20 * time.Millisecond)
	ref.Stop()
	// 再次 Stop 不应 panic（幂等）
	ref.Stop()
}

func TestIdempotentStart(t *testing.T) {
	route.Register("_test_refresher3", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher3"}
	})
	defer route.Unregister("_test_refresher3")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_refresher3": {
				Enabled:         true,
				RefreshInterval: 60,
				Feeds:           []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	ref.Start() // 重复启动
	ref.Stop()
}

// --- PreheatDecision（迁移自 Python TestPreheatDecision） ---

func TestPreheatSkippedWhenDisabled(t *testing.T) {
	route.Register("_test_preheat1", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_preheat1"}
	})
	defer route.Unregister("_test_preheat1")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_preheat1": {
				Enabled:          true,
				RefreshInterval:  60,
				PreheatOnStartup: false,
				Feeds:            []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(50 * time.Millisecond)
	ref.Stop()
	// 预热关闭时不应 panic
}

// --- mockRoute 用于测试 ---

type mockRoute struct {
	name    string
	fetchFn func(route.ArticleStore, []string, route.FetchOptions) ([]route.FeedItem, error)
}

func (m *mockRoute) Name() string        { return m.name }
func (m *mockRoute) Description() string { return "mock" }
func (m *mockRoute) FeedIDField() string { return "user_id" }
func (m *mockRoute) FeedInfo(pathParams []string) (*route.FeedInfo, error) {
	return &route.FeedInfo{Title: "Mock", Link: "https://example.com"}, nil
}
func (m *mockRoute) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
	if m.fetchFn != nil {
		return m.fetchFn(articleStore, pathParams, opts)
	}
	return []route.FeedItem{{Title: "item1"}}, nil
}

// --- refreshOne 测试（迁移自 Python TestRefreshOne） ---

func TestRefreshOneSuccess(t *testing.T) {
	route.Register("_test_r1_ok", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_r1_ok"}
	})
	defer route.Unregister("_test_r1_ok")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshOne("_test_r1_ok", []string{"user1"}, nil)

	cacheKey := "_test_r1_ok/user1"
	ref.statsMu.RLock()
	st, ok := ref.errorStats[cacheKey]
	ref.statsMu.RUnlock()

	if !ok {
		t.Fatal("成功后应有 errorStats 记录")
	}
	if st.Error != "" {
		t.Errorf("成功时 Error 应为空, 实得 %q", st.Error)
	}
	if st.LastSuccess == "" {
		t.Error("成功时 LastSuccess 不应为空")
	}
	if st.ItemCount != 1 {
		t.Errorf("ItemCount = %d, want 1", st.ItemCount)
	}
	// pending 应被清理
	ref.pendingMu.Lock()
	isPending := ref.pending[cacheKey]
	ref.pendingMu.Unlock()
	if isPending {
		t.Error("refreshOne 完成后 pending 应被清理")
	}
}

func TestRefreshOneFailure(t *testing.T) {
	route.Register("_test_r1_fail", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_fail",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, errors.New("boom")
			},
		}
	})
	defer route.Unregister("_test_r1_fail")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	ref.refreshOne("_test_r1_fail", []string{"user1"}, nil)

	cacheKey := "_test_r1_fail/user1"
	ref.statsMu.RLock()
	st, ok := ref.errorStats[cacheKey]
	ref.statsMu.RUnlock()

	if !ok {
		t.Fatal("失败后应有 errorStats 记录")
	}
	if st.Error == "" {
		t.Error("失败时 Error 不应为空")
	}
	if st.ItemCount != 0 {
		t.Errorf("失败时 ItemCount = %d, want 0", st.ItemCount)
	}
}

// --- Business error disables feed（迁移自 Python TestNotifierIntegration） ---

func TestBusinessErrorDisablesFeed(t *testing.T) {
	route.Register("_test_r1_biz", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_biz",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 403})
			},
		}
	})
	defer route.Unregister("_test_r1_biz")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_biz/user1"
	if notif.IsFeedDisabled(cacheKey) {
		t.Fatal("初始状态 feed 不应被禁用")
	}

	ref.refreshOne("_test_r1_biz", []string{"user1"}, nil)

	if !notif.IsFeedDisabled(cacheKey) {
		t.Error("业务错误(403)后 feed 应被禁用")
	}
}

func TestTemporaryErrorKeepsFeedEnabled(t *testing.T) {
	route.Register("_test_r1_temp", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_temp",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 500})
			},
		}
	})
	defer route.Unregister("_test_r1_temp")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_temp/user1"
	ref.refreshOne("_test_r1_temp", []string{"user1"}, nil)

	if notif.IsFeedDisabled(cacheKey) {
		t.Error("临时错误(500)不应禁用 feed")
	}
}

func TestDisabledFeedSkipsFetch(t *testing.T) {
	fetchCalled := false
	route.Register("_test_r1_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_skip",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCalled = true
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})
	defer route.Unregister("_test_r1_skip")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_skip/user1"
	notif.DisableFeed(cacheKey)

	ref.refreshOne("_test_r1_skip", []string{"user1"}, nil)

	if fetchCalled {
		t.Error("被禁用的 feed 不应调用 Fetch")
	}
}

// --- Preheat 已有数据跳过 / 无数据执行（迁移自 Python TestPreheatDecision） ---

// mockArticleStore 用于测试的最小 ArticleStore 实现。
type mockArticleStore struct {
	data map[string]bool // routeName -> hasArticles
}

func (m *mockArticleStore) Get(routeName, articleID string) (string, bool, error) {
	return "", false, nil
}
func (m *mockArticleStore) Save(routeName, articleID, content string) error { return nil }
func (m *mockArticleStore) HasArticles(routeName string) (bool, error) {
	if m.data == nil {
		return false, nil
	}
	return m.data[routeName], nil
}

func TestPreheatSkippedWhenAlreadyHasData(t *testing.T) {
	route.Register("_test_preheat_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_preheat_skip"}
	})
	defer route.Unregister("_test_preheat_skip")

	fetchCount := 0
	route.Register("_test_preheat_count", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_preheat_count",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCount++
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})
	defer route.Unregister("_test_preheat_count")

	store := &mockArticleStore{data: map[string]bool{"_test_preheat_count": true}}
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		ArticleStore: store,
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_preheat_count": {
				Enabled:          true,
				RefreshInterval:  60,
				PreheatOnStartup: true,
				Feeds:            []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(100 * time.Millisecond)
	ref.Stop()

	if fetchCount != 0 {
		t.Errorf("已有数据时预热应跳过, 实际 Fetch 调用 %d 次", fetchCount)
	}
}

func TestPreheatRunsWhenEnabledAndNoData(t *testing.T) {
	fetchCount := 0
	route.Register("_test_preheat_run", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_preheat_run",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCount++
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})
	defer route.Unregister("_test_preheat_run")

	store := &mockArticleStore{data: map[string]bool{}} // 无数据
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		ArticleStore: store,
		StartupDelay: 1,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_preheat_run": {
				Enabled:          true,
				RefreshInterval:  60,
				PreheatOnStartup: true,
				Feeds:            []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(2 * time.Second) // startupDelay=1 + preheat 执行
	ref.Stop()

	if fetchCount == 0 {
		t.Error("无数据且 preheat=true 时应执行预热, 实际 Fetch 调用 0 次")
	}
}

func TestZeroIntervalWithoutPreheatSkipsRoute(t *testing.T) {
	route.Register("_test_zero_iv", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_zero_iv"}
	})
	defer route.Unregister("_test_zero_iv")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_zero_iv": {
				Enabled:         true,
				RefreshInterval: 0, // 间隔为 0
				Feeds:           []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(50 * time.Millisecond)
	ref.Stop()
	// 不 hang、不 panic 即为通过；RefreshInterval=0 且未启用预热时不应创建 goroutine
}

func TestZeroIntervalRunsPreheatThenStops(t *testing.T) {
	preheated := make(chan struct{}, 1)
	route.Register("_test_zero_iv_preheat", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_zero_iv_preheat",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				preheated <- struct{}{}
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})
	defer route.Unregister("_test_zero_iv_preheat")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		ArticleStore: &mockArticleStore{},
		StartupDelay: 1,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_zero_iv_preheat": {
				Enabled:          true,
				RefreshInterval:  0,
				PreheatOnStartup: true,
				Feeds:            []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})
	ref.startupDelay = 0

	ref.Start()
	defer ref.Stop()

	select {
	case <-preheated:
	case <-time.After(time.Second):
		t.Fatal("RefreshInterval=0 但 preheat_on_startup=true 时应执行一次预热")
	}
}

func TestNullIntervalSkipsRoute(t *testing.T) {
	route.Register("_test_null_iv", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_null_iv"}
	})
	defer route.Unregister("_test_null_iv")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_null_iv": {
				Enabled: true,
				// RefreshInterval 未设置，默认 0
				Feeds: []config.FeedConfig{{UserID: "u1"}},
			},
		},
	})

	ref.Start()
	time.Sleep(50 * time.Millisecond)
	ref.Stop()
	// 不 hang、不 panic 即为通过
}

// --- Trigger 参数透传（迁移自 Python TestFetchKwargsPassthrough） ---

func TestTriggerInjectsFeedConfigParams(t *testing.T) {
	var capturedOpts route.FetchOptions
	route.Register("_test_r1_params", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_params",
			fetchFn: func(_ route.ArticleStore, _ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				capturedOpts = opts
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})
	defer route.Unregister("_test_r1_params")

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	// 直接调用 refreshOne 并传入 extraParams
	ref.refreshOne("_test_r1_params", []string{"user1"}, map[string]string{"limit": "5"})

	if capturedOpts.Limit != 5 {
		t.Errorf("Limit = %d, want 5", capturedOpts.Limit)
	}
}
