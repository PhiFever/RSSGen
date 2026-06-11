// Package notifier 测试 —— 迁移自 Python tests/core/test_notifier.py
package notifier

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// --- New / 初始化 ---

func TestNewDefault(t *testing.T) {
	n := New(Config{})
	if n.IsFeedDisabled("any") {
		t.Error("新建 notifier 不应有禁用的 feed")
	}
}

func TestNewWithConfig(t *testing.T) {
	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "feishu", WebhookURL: "https://hooks.example.com"}},
	})
	if !n.enabled {
		t.Error("Enabled=true 应生效")
	}
	if len(n.senders) != 1 {
		t.Errorf("senders 长度应为 1，实际 %d", len(n.senders))
	}
}

func TestNewWithUnknownServiceType(t *testing.T) {
	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "unknown_type"}},
	})
	// 未知类型应被跳过，senders 为空
	if len(n.senders) != 0 {
		t.Errorf("未知类型应被跳过, senders 长度 = %d", len(n.senders))
	}
}

// --- IsBusinessError ---

func TestIsBusinessError(t *testing.T) {
	n := New(Config{})
	businessCodes := []int{400, 401, 403, 404, 410, 422, 451}
	for _, code := range businessCodes {
		if !n.IsBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 true", code)
		}
	}
	nonBusiness := []int{200, 301, 500, 502, 503, 0, -1}
	for _, code := range nonBusiness {
		if n.IsBusinessError(code) {
			t.Errorf("IsBusinessError(%d) 应返回 false", code)
		}
	}
}

func TestIsBusinessErrorCustomCodes(t *testing.T) {
	n := New(Config{BusinessErrorCodes: []int{418, 429}})
	if !n.IsBusinessError(418) {
		t.Error("自定义 418 应为业务错误")
	}
	if !n.IsBusinessError(429) {
		t.Error("自定义 429 应为业务错误")
	}
	if n.IsBusinessError(403) {
		t.Error("默认 403 不应在自定义列表中")
	}
}

// --- DisableFeed / IsFeedDisabled ---

func TestDisableFeedIsolatesSameRoute(t *testing.T) {
	n := New(Config{})
	n.DisableFeed("zhihu:user1")
	if !n.IsFeedDisabled("zhihu:user1") {
		t.Error("DisableFeed 后 IsFeedDisabled 应返回 true")
	}
	if n.IsFeedDisabled("zhihu:user2") {
		t.Error("禁用 user1 不应影响 user2")
	}
	if n.IsFeedDisabled("afdian:user1") {
		t.Error("禁用 zhihu:user1 不应影响 afdian:user1")
	}
}

func TestDisableFeedIdempotent(t *testing.T) {
	n := New(Config{})
	n.DisableFeed("key")
	n.DisableFeed("key") // 重复调用不应 panic
	if !n.IsFeedDisabled("key") {
		t.Error("重复 DisableFeed 后仍应为禁用状态")
	}
}

// --- Notify ---

func TestNotifyDisabled(t *testing.T) {
	n := New(Config{Enabled: false, ServiceURLs: []string{"https://hooks.example.com"}})
	// 不应 panic；enabled=false 时应静默跳过
	n.Notify("key", 403, "业务错误")
}

func TestNotifyNoSenders(t *testing.T) {
	n := New(Config{Enabled: true})
	// 不应 panic；无 senders 时应静默跳过
	n.Notify("key", 500, "服务器错误")
}

func TestNotifyWithFeishuService(t *testing.T) {
	// 用 httptest 模拟飞书 Webhook，验证 Notify 能实际发送
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()

	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "feishu", WebhookURL: server.URL}},
	})
	n.Notify("key", 403, "错误")
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Error("飞书 Webhook 应收到请求")
	}
}

// --- 组合场景 ---

func TestStateCombinationSmoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()
	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "feishu", WebhookURL: server.URL}},
	})

	// 初始状态
	if n.IsFeedDisabled("f1") {
		t.Error("初始状态不应有禁用 feed")
	}

	// 禁用一个
	n.DisableFeed("f1")
	if !n.IsFeedDisabled("f1") {
		t.Error("DisableFeed 后应为禁用")
	}

	// 通知不应 panic
	n.Notify("f1", 403, "错误")
	n.Notify("f2", 500, "临时错误")
}

// --- 通知异常处理（迁移自 Python test_send_notification_exception_handled） ---

func TestSendNotificationExceptionHandled(t *testing.T) {
	// 模拟飞书 Webhook 返回错误
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":1,"msg":"internal error"}`))
	}))
	defer server.Close()

	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "feishu", WebhookURL: server.URL}},
	})

	// Notify 不应 panic，即使远端返回错误
	n.Notify("key", 403, "业务错误")
	time.Sleep(100 * time.Millisecond)
	// 不 panic 即为通过；sendNotification 内部 slog.Error 处理错误
}

// --- 并发安全 ---

func TestConcurrentDisableAndCheck(t *testing.T) {
	n := New(Config{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		key := "feed"
		go func() {
			defer wg.Done()
			n.DisableFeed(key)
		}()
		go func() {
			defer wg.Done()
			n.IsFeedDisabled(key)
		}()
	}
	wg.Wait()
	// 不 panic 即为通过
}
