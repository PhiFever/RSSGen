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
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
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
	ref.errorStats["zhihu/user1"] = &ErrorStatus{
		RouteName:   "zhihu",
		FeedID:      "user1",
		Variant:     pipeline.FeedVariant{Format: "atom", Limit: 20},
		ItemCount:   5,
		LastSuccess: "2025-01-01T00:00:00Z",
	}
	ref.errorStats["afdian/user2"] = &ErrorStatus{
		RouteName: "afdian",
		FeedID:    "user2",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20},
		Error:     "timeout",
	}
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
	if status["zhihu"]["user1"].Variant.Format != "atom" {
		t.Errorf("Variant = %+v", status["zhihu"]["user1"].Variant)
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

func TestGetStatusIncludesObservedDynamicFeeds(t *testing.T) {
	dynamicRef := pipeline.FeedRef{
		RouteName: "zhihu",
		FeedID:    "u1",
		PathParts: []string{"u1"},
		CacheKey:  pipeline.CacheKey("zhihu", []string{"u1"}, route.FetchOptions{}),
		HealthKey: "zhihu/u1",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20},
	}
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		FeedCatalog:  &staticFeedCatalog{refs: []pipeline.FeedRef{dynamicRef}},
		RoutesConfig: map[string]config.RouteConfig{"zhihu": {Enabled: true, RefreshInterval: 60}},
	})

	status := ref.GetStatus()
	got := status["zhihu"]["u1"]
	if got == nil {
		t.Fatal("status 应包含已观察到的动态 feed")
	}
	if got.Source != statusSourceDynamic {
		t.Fatalf("Source = %q, want dynamic", got.Source)
	}
	if got.CacheKey != dynamicRef.CacheKey {
		t.Fatalf("CacheKey = %q, want %q", got.CacheKey, dynamicRef.CacheKey)
	}
}

// --- Start / Stop 生命周期（迁移自 Python TestStartStop） ---

func TestStartCreatesPerRouteTasks(t *testing.T) {
	// 注册一个 mock route factory
	route.Register("_test_refresher", "_test_refresher", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher"}
	})

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
	route.Register("_test_refresh_feeds_skip", "_test_refresh_feeds_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_refresh_feeds_skip",
			fetchFn: func(route.ArticleStore, []string, route.FetchOptions) ([]route.FeedItem, error) {
				t.Fatal("空 feeds 或缺 user_id 时不应调用 Fetch")
				return nil, nil
			},
		}
	})

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.refreshFeeds(refreshPhase{label: "test"}, "_test_refresh_feeds_skip", config.RouteConfig{})
	ref.refreshFeeds(refreshPhase{label: "test"}, "_test_refresh_feeds_skip", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: ""}},
	})
}

func TestRefreshFeedsWithoutJitterDoesNotSleep(t *testing.T) {
	route.Register("_test_refresh_no_jitter", "_test_refresh_no_jitter", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresh_no_jitter"}
	})

	sleepCount := 0
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.sleepFn = func(time.Duration) bool {
		sleepCount++
		return true
	}

	ref.refreshFeeds(refreshPhaseScheduled, "_test_refresh_no_jitter", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: "u1", Limit: 10}},
	})

	if sleepCount != 0 {
		t.Fatalf("未配置 refresh_jitter 时不应 sleep，实际 %d 次", sleepCount)
	}
}

func TestRefreshFeedsAppliesJitterOncePerScheduledBatch(t *testing.T) {
	route.Register("_test_refresh_jitter", "_test_refresh_jitter", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresh_jitter"}
	})

	var jitterMax []time.Duration
	var slept []time.Duration

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.jitterDurationFn = func(max time.Duration) time.Duration {
		jitterMax = append(jitterMax, max)
		return 3 * time.Second
	}
	ref.sleepFn = func(delay time.Duration) bool {
		slept = append(slept, delay)
		return true
	}

	ref.refreshFeeds(refreshPhaseScheduled, "_test_refresh_jitter", config.RouteConfig{
		RefreshJitter: 30,
		Feeds: []config.FeedConfig{
			{UserID: "u1", Limit: 10},
			{UserID: "u2", Limit: 20},
		},
	})

	wantMax := []time.Duration{30 * time.Second}
	if fmt.Sprint(jitterMax) != fmt.Sprint(wantMax) {
		t.Fatalf("jitter max = %v, want %v", jitterMax, wantMax)
	}
	wantSlept := []time.Duration{3 * time.Second}
	if fmt.Sprint(slept) != fmt.Sprint(wantSlept) {
		t.Fatalf("sleep delays = %v, want %v", slept, wantSlept)
	}
}

func TestRefreshFeedsJitterDoesNotApplyToPreheat(t *testing.T) {
	route.Register("_test_preheat_no_jitter", "_test_preheat_no_jitter", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_preheat_no_jitter"}
	})

	sleepCount := 0
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.sleepFn = func(time.Duration) bool {
		sleepCount++
		return true
	}

	ref.refreshFeeds(refreshPhasePreheat, "_test_preheat_no_jitter", config.RouteConfig{
		RefreshJitter: 30,
		Feeds:         []config.FeedConfig{{UserID: "u1", Limit: 10}},
	})

	if sleepCount != 0 {
		t.Fatalf("预热不应应用 refresh_jitter，实际 sleep %d 次", sleepCount)
	}
}

type staticFeedCatalog struct {
	refs []pipeline.FeedRef
}

func (c *staticFeedCatalog) List(routeName string) []pipeline.FeedRef {
	var out []pipeline.FeedRef
	for _, ref := range c.refs {
		if ref.RouteName == routeName {
			out = append(out, ref)
		}
	}
	return out
}

func TestRefreshFeedsUsesDynamicFeedsWhenStaticListEmpty(t *testing.T) {
	var capturedPath []string
	route.Register("_test_refresh_dynamic_only", "_test_refresh_dynamic_only", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_refresh_dynamic_only",
			fetchFn: func(_ route.ArticleStore, pathParams []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				capturedPath = append([]string(nil), pathParams...)
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	dynamicRef := pipeline.FeedRef{
		RouteName: "_test_refresh_dynamic_only",
		FeedID:    "u1",
		PathParts: []string{"u1"},
		CacheKey:  pipeline.CacheKey("_test_refresh_dynamic_only", []string{"u1"}, route.FetchOptions{}),
		HealthKey: "_test_refresh_dynamic_only/u1",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20},
	}
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		FeedCatalog:  &staticFeedCatalog{refs: []pipeline.FeedRef{dynamicRef}},
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshFeeds(refreshPhase{label: "测试"}, "_test_refresh_dynamic_only", config.RouteConfig{})

	if fmt.Sprint(capturedPath) != "[u1]" {
		t.Fatalf("captured path = %v, want [u1]", capturedPath)
	}
	status := ref.GetStatus()
	got := status["_test_refresh_dynamic_only"]["u1"]
	if got == nil || got.Source != statusSourceDynamic {
		t.Fatalf("status = %+v, want dynamic source", got)
	}
}

func TestRefreshFeedsMergesConfigAndDynamicWithConfigPrecedence(t *testing.T) {
	var captured []string
	route.Register("_test_refresh_dynamic_merge", "_test_refresh_dynamic_merge", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_refresh_dynamic_merge",
			fetchFn: func(_ route.ArticleStore, pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				captured = append(captured, fmt.Sprintf("%s:%d:%v", pathParams[0], opts.Limit, opts.Include))
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	staticOpts := route.FetchOptions{Limit: 10}
	dynamicOpts := route.FetchOptions{Limit: 30, Include: []string{"pin"}}
	dynamicRefs := []pipeline.FeedRef{
		{
			RouteName: "_test_refresh_dynamic_merge",
			FeedID:    "u1",
			PathParts: []string{"u1"},
			CacheKey:  pipeline.CacheKey("_test_refresh_dynamic_merge", []string{"u1"}, staticOpts),
			HealthKey: "_test_refresh_dynamic_merge/u1",
			Variant:   pipeline.FeedVariant{Format: "atom", Limit: 10},
		},
		{
			RouteName: "_test_refresh_dynamic_merge",
			FeedID:    "u2",
			PathParts: []string{"u2"},
			CacheKey:  pipeline.CacheKey("_test_refresh_dynamic_merge", []string{"u2"}, dynamicOpts),
			HealthKey: "_test_refresh_dynamic_merge/u2",
			Variant:   pipeline.FeedVariant{Format: "atom", Limit: 30, Include: []string{"pin"}},
		},
	}
	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		FeedCatalog:  &staticFeedCatalog{refs: dynamicRefs},
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshFeeds(refreshPhase{label: "测试"}, "_test_refresh_dynamic_merge", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: "u1", Limit: 10}},
	})

	want := "[u1:10:[] u2:30:[pin]]"
	if fmt.Sprint(captured) != want {
		t.Fatalf("captured = %v, want %s", captured, want)
	}
	status := ref.GetStatus()
	if status["_test_refresh_dynamic_merge"]["u1"].Source != statusSourceConfig {
		t.Fatalf("u1 source = %q, want config", status["_test_refresh_dynamic_merge"]["u1"].Source)
	}
	if status["_test_refresh_dynamic_merge"]["u2"].Source != statusSourceDynamic {
		t.Fatalf("u2 source = %q, want dynamic", status["_test_refresh_dynamic_merge"]["u2"].Source)
	}
}

func TestStopCancelsAllTasks(t *testing.T) {
	route.Register("_test_refresher2", "_test_refresher2", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher2"}
	})

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
	route.Register("_test_refresher3", "_test_refresher3", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher3"}
	})

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
	route.Register("_test_preheat1", "_test_preheat1", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_preheat1"}
	})

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

func (m *mockRoute) Name() string { return m.name }
func (m *mockRoute) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) (route.FeedResult, error) {
	info := route.FeedInfo{Title: "Mock", Link: "https://example.com"}
	if m.fetchFn != nil {
		items, err := m.fetchFn(articleStore, pathParams, opts)
		if err != nil {
			return route.FeedResult{}, err
		}
		return route.FeedResult{Info: info, Items: items}, nil
	}
	return route.FeedResult{Info: info, Items: []route.FeedItem{{Title: "item1"}}}, nil
}

// --- refreshOne 测试（迁移自 Python TestRefreshOne） ---

func TestRefreshOneSuccess(t *testing.T) {
	route.Register("_test_r1_ok", "_test_r1_ok", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_r1_ok"}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshOneWithOptions("_test_r1_ok", []string{"user1"}, route.FetchOptions{})

	cacheKey := pipeline.CacheKey("_test_r1_ok", []string{"user1"}, route.FetchOptions{})
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
	route.Register("_test_r1_fail", "_test_r1_fail", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_fail",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, errors.New("boom")
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	ref.refreshOneWithOptions("_test_r1_fail", []string{"user1"}, route.FetchOptions{})

	cacheKey := pipeline.CacheKey("_test_r1_fail", []string{"user1"}, route.FetchOptions{})
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
	route.Register("_test_r1_biz", "_test_r1_biz", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_biz",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 403})
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	feedHealth := health.New(health.Config{})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		FeedHealth:     feedHealth,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_biz/user1"
	if feedHealth.IsFeedDisabled(cacheKey) {
		t.Fatal("初始状态 feed 不应被禁用")
	}

	ref.refreshOneWithOptions("_test_r1_biz", []string{"user1"}, route.FetchOptions{})

	if !feedHealth.IsFeedDisabled(cacheKey) {
		t.Error("业务错误(403)后 feed 应被禁用")
	}
}

func TestTemporaryErrorKeepsFeedEnabled(t *testing.T) {
	route.Register("_test_r1_temp", "_test_r1_temp", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_temp",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 500})
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	feedHealth := health.New(health.Config{})
	ref := New(Config{
		FeedCache:      feedCache,
		Notifier:       notif,
		FeedHealth:     feedHealth,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_temp/user1"
	ref.refreshOneWithOptions("_test_r1_temp", []string{"user1"}, route.FetchOptions{})

	if feedHealth.IsFeedDisabled(cacheKey) {
		t.Error("临时错误(500)不应禁用 feed")
	}
}

func TestDisabledFeedSkipsFetch(t *testing.T) {
	fetchCalled := false
	route.Register("_test_r1_skip", "_test_r1_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_skip",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCalled = true
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	feedHealth := health.New(health.Config{})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		FeedHealth:   feedHealth,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_skip/user1"
	feedHealth.RecordFailure(cacheKey, route.NewHTTPError(http.StatusForbidden, ""))

	ref.refreshOneWithOptions("_test_r1_skip", []string{"user1"}, route.FetchOptions{})

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
	route.Register("_test_preheat_skip", "_test_preheat_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_preheat_skip"}
	})

	fetchCount := 0
	route.Register("_test_preheat_count", "_test_preheat_count", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_preheat_count",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCount++
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

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
	route.Register("_test_preheat_run", "_test_preheat_run", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_preheat_run",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCount++
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

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
	route.Register("_test_zero_iv", "_test_zero_iv", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_zero_iv"}
	})

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
	route.Register("_test_zero_iv_preheat", "_test_zero_iv_preheat", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_zero_iv_preheat",
			fetchFn: func(_ route.ArticleStore, _ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				preheated <- struct{}{}
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

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
	route.Register("_test_null_iv", "_test_null_iv", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_null_iv"}
	})

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
	route.Register("_test_r1_params", "_test_r1_params", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_params",
			fetchFn: func(_ route.ArticleStore, _ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				capturedOpts = opts
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notif := notifier.New(notifier.Config{Enabled: false})
	ref := New(Config{
		FeedCache:    feedCache,
		Notifier:     notif,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshOneWithOptions("_test_r1_params", []string{"user1"}, route.FetchOptions{Limit: 5})

	if capturedOpts.Limit != 5 {
		t.Errorf("Limit = %d, want 5", capturedOpts.Limit)
	}
}

func TestRefreshFeedsPassesZhihuIncludeAndLimit(t *testing.T) {
	var capturedOpts route.FetchOptions
	route.Register("_test_r1_include", "_test_r1_include", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			name: "_test_r1_include",
			fetchFn: func(_ route.ArticleStore, _ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				capturedOpts = opts
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshFeeds(refreshPhase{label: "测试"}, "_test_r1_include", config.RouteConfig{
		Feeds: []config.FeedConfig{
			{
				UserID:  "user1",
				Alias:   "用户A",
				Limit:   15,
				Include: []string{"answer", "article"},
			},
		},
	})

	if capturedOpts.Limit != 15 {
		t.Fatalf("Limit = %d, want 15", capturedOpts.Limit)
	}
	wantInclude := []string{"answer", "article"}
	if fmt.Sprint(capturedOpts.Include) != fmt.Sprint(wantInclude) {
		t.Fatalf("Include = %v, want %v", capturedOpts.Include, wantInclude)
	}
}
