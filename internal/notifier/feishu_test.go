package notifier

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFeishuSign(t *testing.T) {
	// 飞书官方文档示例：timestamp="1599360473", secret="test_secret"
	sign, err := feishuSign("1599360473", "test_secret")
	if err != nil {
		t.Fatalf("feishuSign 返回错误: %v", err)
	}
	// 签名结果应为合法 base64
	if _, err := base64.StdEncoding.DecodeString(sign); err != nil {
		t.Errorf("签名不是合法 base64: %v", err)
	}
	// 同样输入应产生同样输出
	sign2, _ := feishuSign("1599360473", "test_secret")
	if sign != sign2 {
		t.Error("相同输入应产生相同签名")
	}
	// 不同 secret 应产生不同签名
	sign3, _ := feishuSign("1599360473", "other_secret")
	if sign == sign3 {
		t.Error("不同 secret 应产生不同签名")
	}
}

func TestFeishuSenderSend(t *testing.T) {
	// 用 httptest 模拟飞书 Webhook
	var receivedBody []byte
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"code":0,"msg":"success"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s := newFeishuSender(server.URL, "")
	err := s.Send("测试消息")
	if err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}

	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	// 验证请求体结构
	var req feishuRequest
	if err := json.Unmarshal(receivedBody, &req); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
	if req.MsgType != "text" {
		t.Errorf("msg_type = %q, want text", req.MsgType)
	}
	if req.Content.Text != "测试消息" {
		t.Errorf("content.text = %q, want '测试消息'", req.Content.Text)
	}
	if req.Sign != "" {
		t.Error("无 secret 时不应有签名")
	}
}

func TestFeishuSenderSendWithSign(t *testing.T) {
	var req feishuRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("解析请求失败: %v", err)
		}
		if _, err := w.Write([]byte(`{"code":0,"msg":"success"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s := newFeishuSender(server.URL, "my_secret")
	err := s.Send("带签名消息")
	if err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}

	if req.Sign == "" {
		t.Error("有 secret 时应包含签名")
	}
	if req.Timestamp == "" {
		t.Error("有 secret 时应包含 timestamp")
	}
	if req.Content.Text != "带签名消息" {
		t.Errorf("content.text = %q", req.Content.Text)
	}
}

func TestFeishuSenderSendError(t *testing.T) {
	// 模拟飞书返回错误
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"code":9499,"msg":"bad request"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s := newFeishuSender(server.URL, "")
	err := s.Send("msg")
	if err == nil {
		t.Error("飞书返回错误码时应返回 error")
	}
}

func TestFeishuSenderSendHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte("forbidden")); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s := newFeishuSender(server.URL, "")
	err := s.Send("msg")
	if err == nil {
		t.Error("HTTP 非 200 时应返回 error")
	}
}

func TestFeishuSenderSendInvalidJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{bad json`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	s := newFeishuSender(server.URL, "")
	if err := s.Send("msg"); err == nil {
		t.Error("飞书返回无效 JSON 时应返回 error")
	}
}

func TestFeishuSenderSendRequestError(t *testing.T) {
	s := newFeishuSender("http://127.0.0.1:1", "")
	if err := s.Send("msg"); err == nil {
		t.Error("Webhook 请求失败时应返回 error")
	}
}
