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
