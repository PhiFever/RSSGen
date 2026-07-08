package health

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/PhiFever/RSSGen/internal/route"
)

func TestIsBusinessError(t *testing.T) {
	h := New(Config{})
	businessCodes := []int{400, 401, 403, 404, 410, 422, 451}
	for _, code := range businessCodes {
		if !h.IsBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 true", code)
		}
	}
	nonBusiness := []int{200, 301, 500, 502, 503, 0, -1}
	for _, code := range nonBusiness {
		if h.IsBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 false", code)
		}
	}
}

func TestIsBusinessErrorCustomCodes(t *testing.T) {
	h := New(Config{BusinessErrorCodes: []int{418, 429}})
	if !h.IsBusinessError(418) {
		t.Error("自定义 418 应为业务错误")
	}
	if !h.IsBusinessError(429) {
		t.Error("自定义 429 应为业务错误")
	}
	if h.IsBusinessError(403) {
		t.Error("默认 403 不应在自定义列表中")
	}
}

func TestDisableFeedIsolatesSameRoute(t *testing.T) {
	h := New(Config{})
	h.DisableFeed("zhihu/user1")
	if !h.IsFeedDisabled("zhihu/user1") {
		t.Error("DisableFeed 后 IsFeedDisabled 应返回 true")
	}
	if h.IsFeedDisabled("zhihu/user2") {
		t.Error("禁用 user1 不应影响 user2")
	}
	if h.IsFeedDisabled("afdian/user1") {
		t.Error("禁用 zhihu/user1 不应影响 afdian/user1")
	}
}

func TestDisableFeedIdempotent(t *testing.T) {
	h := New(Config{})
	h.DisableFeed("key")
	h.DisableFeed("key")
	if !h.IsFeedDisabled("key") {
		t.Error("重复 DisableFeed 后仍应为禁用状态")
	}
}

func TestRecordFailureBusinessErrorDisablesFeed(t *testing.T) {
	h := New(Config{})

	statusCode, justDisabled := h.RecordFailure("zhihu/user1", fmt.Errorf("wrapped: %w", &route.HTTPError{StatusCode: 403}))
	if statusCode != 403 {
		t.Fatalf("statusCode = %d, want 403", statusCode)
	}
	if !justDisabled {
		t.Fatal("首次业务错误应返回 justDisabled=true")
	}
	if !h.IsFeedDisabled("zhihu/user1") {
		t.Fatal("业务错误后 feed 应被禁用")
	}

	statusCode, justDisabled = h.RecordFailure("zhihu/user1", &route.HTTPError{StatusCode: 403})
	if statusCode != 403 {
		t.Fatalf("second statusCode = %d, want 403", statusCode)
	}
	if justDisabled {
		t.Fatal("已禁用 feed 的后续失败不应重复返回 justDisabled=true")
	}
}

func TestRecordFailureIgnoresTemporaryAndPlainErrors(t *testing.T) {
	h := New(Config{})

	statusCode, justDisabled := h.RecordFailure("zhihu/user1", &route.HTTPError{StatusCode: 500})
	if statusCode != 500 {
		t.Fatalf("statusCode = %d, want 500", statusCode)
	}
	if justDisabled || h.IsFeedDisabled("zhihu/user1") {
		t.Fatal("临时 HTTP 错误不应禁用 feed")
	}

	statusCode, justDisabled = h.RecordFailure("zhihu/user1", errors.New("boom"))
	if statusCode != 0 {
		t.Fatalf("plain statusCode = %d, want 0", statusCode)
	}
	if justDisabled || h.IsFeedDisabled("zhihu/user1") {
		t.Fatal("普通错误不应禁用 feed")
	}
}

func TestConcurrentDisableAndCheck(t *testing.T) {
	h := New(Config{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		key := "feed"
		go func() {
			defer wg.Done()
			h.DisableFeed(key)
		}()
		go func() {
			defer wg.Done()
			h.IsFeedDisabled(key)
		}()
	}
	wg.Wait()
}
