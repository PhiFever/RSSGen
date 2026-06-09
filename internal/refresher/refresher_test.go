package refresher

import (
	"errors"
	"fmt"
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
		result := buildCacheKey(tt.routeName, tt.pathParams)
		if result != tt.expected {
			t.Errorf("buildCacheKey(%q, %v) = %q, want %q", tt.routeName, tt.pathParams, result, tt.expected)
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
	defer delete(route.GetRegistry(), "_test_refresher")

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

func TestStopCancelsAllTasks(t *testing.T) {
	route.Register("_test_refresher2", func(cfg config.ResolvedRouteConfig) route.Route {
		return &mockRoute{name: "_test_refresher2"}
	})
	defer delete(route.GetRegistry(), "_test_refresher2")

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
	defer delete(route.GetRegistry(), "_test_refresher3")

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
	defer delete(route.GetRegistry(), "_test_preheat1")

	ref := New(Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		StartupDelay: 0,
		RoutesConfig: map[string]config.RouteConfig{
			"_test_preheat1": {
				Enabled:         true,
				RefreshInterval: 60,
				PreheatOnStartup: false,
				Feeds:           []config.FeedConfig{{UserID: "u1"}},
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
	name string
}

func (m *mockRoute) Name() string        { return m.name }
func (m *mockRoute) Description() string  { return "mock" }
func (m *mockRoute) FeedIDField() string  { return "user_id" }
func (m *mockRoute) FeedInfo(pathParams []string) (*route.FeedInfo, error) {
	return &route.FeedInfo{Title: "Mock", Link: "https://example.com"}, nil
}
func (m *mockRoute) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
	return []route.FeedItem{{Title: "item1"}}, nil
}
