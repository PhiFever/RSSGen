package refresher

import (
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/notifier"
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

func TestBuildMergedConfig(t *testing.T) {
	rateLimit := 0.5
	proxy := "http://proxy:8080"

	rc := config.RouteConfig{
		Cookie:    "test_cookie",
		RateLimit: &rateLimit,
		Proxy:     &proxy,
		Feeds: []config.FeedConfig{
			{UserID: "user1", Alias: "用户1", Limit: 10},
		},
	}

	result := buildMergedConfig(rc)

	if result["cookie"] != "test_cookie" {
		t.Errorf("cookie = %v, want %q", result["cookie"], "test_cookie")
	}
	if result["rate_limit"] != 0.5 {
		t.Errorf("rate_limit = %v, want 0.5", result["rate_limit"])
	}
	if result["proxy"] != "http://proxy:8080" {
		t.Errorf("proxy = %v, want %q", result["proxy"], "http://proxy:8080")
	}

	feeds, ok := result["feeds"].([]interface{})
	if !ok {
		t.Fatal("feeds 不存在或类型错误")
	}
	if len(feeds) != 1 {
		t.Fatalf("feeds len = %d, want 1", len(feeds))
	}
	feedMap, ok := feeds[0].(map[string]interface{})
	if !ok {
		t.Fatal("feed 类型错误")
	}
	if feedMap["user_id"] != "user1" {
		t.Errorf("user_id = %v, want %q", feedMap["user_id"], "user1")
	}
	if feedMap["alias"] != "用户1" {
		t.Errorf("alias = %v, want %q", feedMap["alias"], "用户1")
	}
}

func TestExtractStatusCode(t *testing.T) {
	tests := []struct {
		err      string
		expected int
	}{
		{"HTTP 403", 403},
		{"HTTP 404", 404},
		{"some other error", 0},
		{"", 0},
	}

	for _, tt := range tests {
		var err error
		if tt.err != "" {
			err = &testError{msg: tt.err}
		}
		result := extractStatusCode(err)
		if result != tt.expected {
			t.Errorf("extractStatusCode(%q) = %d, want %d", tt.err, result, tt.expected)
		}
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
