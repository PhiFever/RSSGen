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
		if !h.isBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 true", code)
		}
	}
	nonBusiness := []int{200, 301, 500, 502, 503, 0, -1}
	for _, code := range nonBusiness {
		if h.isBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 false", code)
		}
	}
}

func TestIsBusinessErrorCustomCodes(t *testing.T) {
	h := New(Config{BusinessErrorCodes: []int{418, 429}})
	if !h.isBusinessError(418) {
		t.Error("自定义 418 应为业务错误")
	}
	if !h.isBusinessError(429) {
		t.Error("自定义 429 应为业务错误")
	}
	if h.isBusinessError(403) {
		t.Error("默认 403 不应在自定义列表中")
	}
}

func TestRecordFailureIsolatesSameRoute(t *testing.T) {
	h := New(Config{})
	h.RecordFailure("zhihu/user1", &route.HTTPError{StatusCode: 403})
	if !h.IsFeedDisabled("zhihu/user1") {
		t.Error("RecordFailure 后 IsFeedDisabled 应返回 true")
	}
	if h.IsFeedDisabled("zhihu/user2") {
		t.Error("禁用 user1 不应影响 user2")
	}
	if h.IsFeedDisabled("afdian/user1") {
		t.Error("禁用 zhihu/user1 不应影响 afdian/user1")
	}
}

func TestRecordFailureIdempotent(t *testing.T) {
	h := New(Config{})
	h.RecordFailure("key", &route.HTTPError{StatusCode: 403})
	h.RecordFailure("key", &route.HTTPError{StatusCode: 403})
	if !h.IsFeedDisabled("key") {
		t.Error("重复 RecordFailure 后仍应为禁用状态")
	}
}

func TestRecordFailureBusinessErrorDisablesFeed(t *testing.T) {
	notifications := 0
	failure := fmt.Errorf("wrapped: %w", &route.HTTPError{StatusCode: 403})
	var h *FeedHealth
	h = New(Config{OnDisabled: func(key string, code int, message string) {
		notifications++
		if key != "zhihu/user1" || code != 403 || message != failure.Error() {
			t.Errorf("通知参数不匹配: %s %d %s", key, code, message)
		}
		if !h.IsFeedDisabled(key) {
			t.Error("通知前应已禁用 feed")
		}
	}})
	h.RecordFailure("zhihu/user1", failure)
	h.RecordFailure("zhihu/user1", failure)
	if !h.IsFeedDisabled("zhihu/user1") || notifications != 1 {
		t.Fatalf("禁用状态 = %v, 通知次数 = %d", h.IsFeedDisabled("zhihu/user1"), notifications)
	}
}

func TestRecordFailureIgnoresTemporaryAndPlainErrors(t *testing.T) {
	notifications := 0
	h := New(Config{OnDisabled: func(string, int, string) { notifications++ }})
	h.RecordFailure("zhihu/user1", &route.HTTPError{StatusCode: 500})
	h.RecordFailure("zhihu/user1", errors.New("boom"))
	if h.IsFeedDisabled("zhihu/user1") || notifications != 0 {
		t.Fatal("非业务错误不应禁用或通知")
	}
}

func TestConcurrentRecordFailureAndCheck(t *testing.T) {
	h := New(Config{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		key := "feed"
		go func() {
			defer wg.Done()
			h.RecordFailure(key, &route.HTTPError{StatusCode: 403})
		}()
		go func() {
			defer wg.Done()
			h.IsFeedDisabled(key)
		}()
	}
	wg.Wait()
}
