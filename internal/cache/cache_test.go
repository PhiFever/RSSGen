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

func TestDelete(t *testing.T) {
	c := New(1 * time.Second)

	c.Set("key1", "value1")
	c.Delete("key1")

	_, ok := c.Get("key1")
	if ok {
		t.Fatal("expected key1 to be deleted")
	}
}

func TestLen(t *testing.T) {
	c := New(1 * time.Second)

	if c.Len() != 0 {
		t.Fatalf("expected 0, got %d", c.Len())
	}

	c.Set("a", "1")
	c.Set("b", "2")

	if c.Len() != 2 {
		t.Fatalf("expected 2, got %d", c.Len())
	}
}
