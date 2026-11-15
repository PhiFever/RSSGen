package refresher

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/route"
)

// newTestRefresher 统一组装测试所需的流水线和健康状态依赖。
func newTestRefresher(t *testing.T, feedCache *cache.TTLCache, cfg Config) *Refresher {
	t.Helper()
	cfg.Pipeline = pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.RoutesConfig})
	if cfg.FeedHealth == nil {
		cfg.FeedHealth = health.New(health.Config{})
	}
	ref := New(cfg)
	t.Cleanup(ref.cancel)
	return ref
}

func TestNew(t *testing.T) {
	feedCache := cache.New(10 * time.Second)

	cfg := Config{
		StartupDelay:   1,
		MaxRetries:     2,
		RetryBaseDelay: 1,
		RoutesConfig:   map[string]config.RouteConfig{},
	}

	ref := newTestRefresher(t, feedCache, cfg)
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

// --- GetStatus（迁移自 Python TestGetStatus） ---

func TestGetStatusReturnsRouteGroupedDict(t *testing.T) {
	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
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
	if status["zhihu"]["user1"][0].ItemCount != 5 {
		t.Errorf("ItemCount = %d, want 5", status["zhihu"]["user1"][0].ItemCount)
	}
	if status["zhihu"]["user1"][0].Variant.Format != "atom" {
		t.Errorf("Variant = %+v", status["zhihu"]["user1"][0].Variant)
	}
	if status["afdian"] == nil || status["afdian"]["user2"] == nil {
		t.Error("应包含 afdian/user2")
	}
	if status["afdian"]["user2"][0].Error != "timeout" {
		t.Errorf("Error = %q, want 'timeout'", status["afdian"]["user2"][0].Error)
	}
}

func TestGetStatusEmpty(t *testing.T) {
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		FeedCatalog:  &staticFeedCatalog{refs: []pipeline.FeedRef{dynamicRef}},
		RoutesConfig: map[string]config.RouteConfig{"zhihu": {Enabled: true, RefreshInterval: 60}},
	})

	rss := ref.pipe.FeedRef("zhihu", []string{"u1"}, route.FetchOptions{Format: "rss"})
	ref.errorStats[rss.CacheKey] = &ErrorStatus{RouteName: rss.RouteName, FeedID: rss.FeedID, CacheKey: rss.CacheKey, Variant: rss.Variant}
	status := ref.GetStatus()
	got := status["zhihu"]["u1"]
	if len(got) != 2 {
		t.Fatal("status 应同时包含已刷新变体和已观察到的动态变体")
	}
	if got[0].Source != statusSourceDynamic {
		t.Fatalf("Source = %q, want dynamic", got[0].Source)
	}
	if got[0].CacheKey != dynamicRef.CacheKey {
		t.Fatalf("CacheKey = %q, want %q", got[0].CacheKey, dynamicRef.CacheKey)
	}
}

// --- Start / Stop 生命周期（迁移自 Python TestStartStop） ---

func TestStartCreatesPerRouteTasks(t *testing.T) {
	// 注册一个 mock route factory
	route.Register("_test_refresher", "_test_refresher", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{}
	})

	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
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

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
	newTestRefresher(t, cache.New(10*time.Second), Config{
		RoutesConfig: map[string]config.RouteConfig{},
	}).preinitHTTPClient()

	errorRef := newTestRefresher(t, cache.New(10*time.Second), Config{
		PreinitURL:   "http://127.0.0.1:1",
		RoutesConfig: map[string]config.RouteConfig{},
	})
	errorRef.preinitHTTPClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	statusRef := newTestRefresher(t, cache.New(10*time.Second), Config{
		PreinitURL:   server.URL,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	statusRef.preinitHTTPClient()
}

func TestRefreshFeedsSkipsEmptyAndInvalidFeedConfig(t *testing.T) {
	route.Register("_test_refresh_feeds_skip", "_test_refresh_feeds_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func([]string, route.FetchOptions) ([]route.FeedItem, error) {
				t.Fatal("空 feeds 或缺 user_id 时不应调用 Fetch")
				return nil, nil
			},
		}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.refreshFeeds(refreshPhase{label: "test"}, "_test_refresh_feeds_skip", config.RouteConfig{})
	ref.refreshFeeds(refreshPhase{label: "test"}, "_test_refresh_feeds_skip", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: ""}},
	})
}

func TestRefreshFeedsWithoutJitterDoesNotSleep(t *testing.T) {
	route.Register("_test_refresh_no_jitter", "_test_refresh_no_jitter", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{}
	})

	sleepCount := 0
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.sleepFn = func(time.Duration) bool {
		sleepCount++
		return true
	}

	ref.refreshFeeds(refreshPhaseScheduled, "_test_refresh_no_jitter", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: "u1"}},
	})

	if sleepCount != 0 {
		t.Fatalf("未配置 refresh_jitter 时不应 sleep，实际 %d 次", sleepCount)
	}
}

func TestRefreshFeedsAppliesJitterOncePerScheduledBatch(t *testing.T) {
	route.Register("_test_refresh_jitter", "_test_refresh_jitter", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{}
	})

	var jitterMax []time.Duration
	var slept []time.Duration

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
			{UserID: "u1"},
			{UserID: "u2"},
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
		return &mockRoute{}
	})

	sleepCount := 0
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})
	ref.sleepFn = func(time.Duration) bool {
		sleepCount++
		return true
	}

	ref.refreshFeeds(refreshPhasePreheat, "_test_preheat_no_jitter", config.RouteConfig{
		RefreshJitter: 30,
		Feeds:         []config.FeedConfig{{UserID: "u1"}},
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
			fetchFn: func(pathParams []string, _ route.FetchOptions) ([]route.FeedItem, error) {
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
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
	if len(got) == 0 || got[0].Source != statusSourceDynamic {
		t.Fatalf("status = %+v, want dynamic source", got)
	}
}

func TestRefreshFeedsMergesConfigAndDynamicWithConfigPrecedence(t *testing.T) {
	var captured []string
	route.Register("_test_refresh_dynamic_merge", "_test_refresh_dynamic_merge", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				captured = append(captured, fmt.Sprintf("%s:%d:%v", pathParams[0], opts.Limit, opts.Include))
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	staticOpts := route.FetchOptions{}
	dynamicOpts := route.FetchOptions{Limit: 30, Include: []string{"pin"}}
	dynamicRefs := []pipeline.FeedRef{
		{
			RouteName: "_test_refresh_dynamic_merge",
			FeedID:    "u1",
			PathParts: []string{"u1"},
			CacheKey:  pipeline.CacheKey("_test_refresh_dynamic_merge", []string{"u1"}, staticOpts),
			HealthKey: "_test_refresh_dynamic_merge/u1",
			Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20},
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
	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		FeedCatalog:  &staticFeedCatalog{refs: dynamicRefs},
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshFeeds(refreshPhase{label: "测试"}, "_test_refresh_dynamic_merge", config.RouteConfig{
		Feeds: []config.FeedConfig{{UserID: "u1"}},
	})

	want := "[u1:20:[] u2:30:[pin]]"
	if fmt.Sprint(captured) != want {
		t.Fatalf("captured = %v, want %s", captured, want)
	}
	status := ref.GetStatus()
	if status["_test_refresh_dynamic_merge"]["u1"][0].Source != statusSourceConfig {
		t.Fatalf("u1 source = %q, want config", status["_test_refresh_dynamic_merge"]["u1"][0].Source)
	}
	if status["_test_refresh_dynamic_merge"]["u2"][0].Source != statusSourceDynamic {
		t.Fatalf("u2 source = %q, want dynamic", status["_test_refresh_dynamic_merge"]["u2"][0].Source)
	}
}

func TestStopCancelsAllTasks(t *testing.T) {
	route.Register("_test_refresher2", "_test_refresher2", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{}
	})

	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
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
		return &mockRoute{}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
		return &mockRoute{}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
	fetchFn func([]string, route.FetchOptions) ([]route.FeedItem, error)
}

func (m *mockRoute) Fetch(pathParams []string, opts route.FetchOptions) (route.FeedResult, error) {
	info := route.FeedInfo{Title: "Mock", Link: "https://example.com"}
	if m.fetchFn != nil {
		items, err := m.fetchFn(pathParams, opts)
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
		return &mockRoute{}
	})

	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshOne("_test_r1_ok", []string{"user1"}, route.FetchOptions{}, statusSourceConfig)

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
}

func TestRefreshOneFailure(t *testing.T) {
	route.Register("_test_r1_fail", "_test_r1_fail", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, errors.New("boom")
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	ref.refreshOne("_test_r1_fail", []string{"user1"}, route.FetchOptions{}, statusSourceConfig)

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
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 403})
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	notifications := 0
	feedHealth := health.New(health.Config{OnDisabled: func(key string, code int, message string) {
		notifications++
		if key != "_test_r1_biz/user1" || code != 403 || message == "" {
			t.Errorf("通知参数不匹配: %s %d %s", key, code, message)
		}
	}})
	ref := newTestRefresher(t, feedCache, Config{
		FeedHealth:     feedHealth,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_biz/user1"
	if feedHealth.IsFeedDisabled(cacheKey) {
		t.Fatal("初始状态 feed 不应被禁用")
	}

	ref.refreshOne("_test_r1_biz", []string{"user1"}, route.FetchOptions{}, statusSourceConfig)

	if !feedHealth.IsFeedDisabled(cacheKey) {
		t.Error("业务错误(403)后 feed 应被禁用")
	}

	if notifications != 1 {
		t.Fatalf("通知次数 = %d, want 1", notifications)
	}
}

func TestTemporaryErrorKeepsFeedEnabled(t *testing.T) {
	route.Register("_test_r1_temp", "_test_r1_temp", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				return nil, fmt.Errorf("upstream: %w", &route.HTTPError{StatusCode: 500})
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	ref := newTestRefresher(t, feedCache, Config{
		FeedHealth:     feedHealth,
		MaxRetries:     1,
		RetryBaseDelay: 0,
		RoutesConfig:   map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_temp/user1"
	ref.refreshOne("_test_r1_temp", []string{"user1"}, route.FetchOptions{}, statusSourceConfig)

	if feedHealth.IsFeedDisabled(cacheKey) {
		t.Error("临时错误(500)不应禁用 feed")
	}
}

func TestDisabledFeedSkipsFetch(t *testing.T) {
	fetchCalled := false
	route.Register("_test_r1_skip", "_test_r1_skip", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCalled = true
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	ref := newTestRefresher(t, feedCache, Config{
		FeedHealth:   feedHealth,
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	cacheKey := "_test_r1_skip/user1"
	feedHealth.RecordFailure(cacheKey, route.NewHTTPError(http.StatusForbidden, ""))

	ref.refreshOne("_test_r1_skip", []string{"user1"}, route.FetchOptions{}, statusSourceConfig)

	if fetchCalled {
		t.Error("被禁用的 feed 不应调用 Fetch")
	}
}

func TestPreheatRunsWheneverEnabled(t *testing.T) {
	fetchCount := 0
	route.Register("_test_preheat_run", "_test_preheat_run", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				fetchCount++
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
		t.Error("preheat=true 时每次进程启动都应执行预热")
	}
}

func TestZeroIntervalWithoutPreheatSkipsRoute(t *testing.T) {
	route.Register("_test_zero_iv", "_test_zero_iv", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
			fetchFn: func(_ []string, _ route.FetchOptions) ([]route.FeedItem, error) {
				preheated <- struct{}{}
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
		return &mockRoute{}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
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
			fetchFn: func(_ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				capturedOpts = opts
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	feedCache := cache.New(10 * time.Second)
	ref := newTestRefresher(t, feedCache, Config{
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshOne("_test_r1_params", []string{"user1"}, route.FetchOptions{Limit: 5}, statusSourceConfig)

	if capturedOpts.Limit != 5 {
		t.Errorf("Limit = %d, want 5", capturedOpts.Limit)
	}
}

func TestRefreshFeedsPassesZhihuIncludeAndDefaultLimit(t *testing.T) {
	var capturedOpts route.FetchOptions
	route.Register("_test_r1_include", "_test_r1_include", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{
			fetchFn: func(_ []string, opts route.FetchOptions) ([]route.FeedItem, error) {
				capturedOpts = opts
				return []route.FeedItem{{Title: "t"}}, nil
			},
		}
	})

	ref := newTestRefresher(t, cache.New(10*time.Second), Config{
		MaxRetries:   1,
		RoutesConfig: map[string]config.RouteConfig{},
	})

	ref.refreshFeeds(refreshPhase{label: "测试"}, "_test_r1_include", config.RouteConfig{
		Feeds: []config.FeedConfig{
			{
				UserID:  "user1",
				Alias:   "用户A",
				Include: []string{"answer", "article"},
			},
		},
	})

	if capturedOpts.Limit != 20 {
		t.Fatalf("Limit = %d, want default 20", capturedOpts.Limit)
	}
	wantInclude := []string{"answer", "article"}
	if fmt.Sprint(capturedOpts.Include) != fmt.Sprint(wantInclude) {
		t.Fatalf("Include = %v, want %v", capturedOpts.Include, wantInclude)
	}
}

func TestGetStatusPreservesSortedVariants(t *testing.T) {
	ref := newTestRefresher(t, cache.New(time.Second), Config{})
	atom := ref.pipe.FeedRef("zhihu", []string{"u1"}, route.FetchOptions{Format: "atom"})
	rss := ref.pipe.FeedRef("zhihu", []string{"u1"}, route.FetchOptions{Format: "rss"})
	for _, variant := range []pipeline.FeedRef{rss, atom} {
		ref.errorStats[variant.CacheKey] = &ErrorStatus{RouteName: variant.RouteName, FeedID: variant.FeedID, CacheKey: variant.CacheKey, Variant: variant.Variant}
	}
	for i := 0; i < 10; i++ {
		data, err := json.Marshal(ref.GetStatus())
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]map[string][]ErrorStatus
		if err := json.Unmarshal(data, &status); err != nil {
			t.Fatalf("状态应按变体列出: %v", err)
		}
		got := status["zhihu"]["u1"]
		if len(got) != 2 || got[0].CacheKey != atom.CacheKey || got[1].CacheKey != rss.CacheKey {
			t.Fatalf("变体状态或排序不匹配: %+v", got)
		}
	}
}

func TestRefreshFeedsPrunesStaleRouteStatus(t *testing.T) {
	ref := newTestRefresher(t, cache.New(time.Second), Config{})
	stale := ref.pipe.FeedRef("zhihu", []string{"u1"}, route.FetchOptions{})
	other := ref.pipe.FeedRef("afdian", []string{"u2"}, route.FetchOptions{})
	for _, feed := range []pipeline.FeedRef{stale, other} {
		ref.errorStats[feed.CacheKey] = &ErrorStatus{RouteName: feed.RouteName, FeedID: feed.FeedID, CacheKey: feed.CacheKey}
	}
	ref.refreshFeeds(refreshPhaseScheduled, "zhihu", config.RouteConfig{})
	if _, ok := ref.errorStats[stale.CacheKey]; ok {
		t.Error("本轮没有目标时也应删除同路由的淘汰状态")
	}
	if _, ok := ref.errorStats[other.CacheKey]; !ok {
		t.Error("应保留其它路由状态")
	}
}
