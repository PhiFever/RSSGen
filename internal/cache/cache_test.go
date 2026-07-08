package cache

import (
	"testing"
	"time"
)

func TestSetAndGet(t *testing.T) {
	c := New(1 * time.Second)

	c.Set("key1", "value1")
	v, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected to find key1")
	}
	if v != "value1" {
		t.Fatalf("expected value1, got %s", v)
	}
}

func TestGetMissing(t *testing.T) {
	c := New(1 * time.Second)

	_, ok := c.Get("missing")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestTTLExpiry(t *testing.T) {
	c := New(50 * time.Millisecond)

	c.Set("key1", "value1")

	// 立即获取应成功
	v, ok := c.Get("key1")
	if !ok || v != "value1" {
		t.Fatal("expected value1 immediately after set")
	}

	// 等待过期
	time.Sleep(100 * time.Millisecond)

	_, ok = c.Get("key1")
	if ok {
		t.Fatal("expected key1 to be expired")
	}
}

func TestOverwrite(t *testing.T) {
	c := New(1 * time.Second)

	c.Set("key1", "value1")
	c.Set("key1", "value2")

	v, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected to find key1")
	}
	if v != "value2" {
		t.Fatalf("expected value2, got %s", v)
	}
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		route string
		parts []string
		want  string
	}{
		{route: "zhihu", parts: nil, want: "zhihu"},
		{route: "afdian", parts: []string{"author"}, want: "afdian/author"},
		{route: "test", parts: []string{"a", "b"}, want: "test/a/b"},
	}
	for _, tt := range tests {
		if got := BuildCacheKey(tt.route, tt.parts); got != tt.want {
			t.Fatalf("BuildCacheKey(%q, %+v) = %q, want %q", tt.route, tt.parts, got, tt.want)
		}
	}
}

func TestBuildFeedCacheKeyIncludesVariant(t *testing.T) {
	got := BuildFeedCacheKey("zhihu", []string{"user1"}, FeedVariant{
		Format:  "rss",
		Limit:   7,
		Include: []string{"answer", "pin"},
	})
	want := "zhihu/user1?format=rss&limit=7&include=answer,pin"
	if got != want {
		t.Fatalf("BuildFeedCacheKey variant = %q, want %q", got, want)
	}

	base := BuildCacheKey("zhihu", []string{"user1"})
	if got == base {
		t.Fatal("变体缓存键不应退化为基础 feed 键")
	}
}
