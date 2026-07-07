// Package notifier 测试 —— 迁移自 Python tests/core/test_notifier.py
package notifier

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- New / 初始化 ---

func TestNewDefault(t *testing.T) {
	n := New(Config{})
	if n.enabled {
		t.Error("默认 notifier 不应启用")
	}
	if len(n.senders) != 0 {
		t.Errorf("默认 senders 长度应为 0，实际 %d", len(n.senders))
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
		if _, err := w.Write([]byte(`{"code":0,"msg":"success"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
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

func TestNotifyMultipleMessagesSmoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"code":0,"msg":"success"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()
	n := New(Config{
		Enabled:  true,
		Services: []ServiceConfig{{Type: "feishu", WebhookURL: server.URL}},
	})

	// 通知不应 panic
	n.Notify("f1", 403, "错误")
	n.Notify("f2", 500, "临时错误")
}

// --- 通知异常处理（迁移自 Python test_send_notification_exception_handled） ---

func TestSendNotificationExceptionHandled(t *testing.T) {
	// 模拟飞书 Webhook 返回错误
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(`{"code":1,"msg":"internal error"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
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
