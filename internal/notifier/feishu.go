package notifier

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// feishuSender 通过飞书 Incoming Webhook 发送通知。
type feishuSender struct {
	webhookURL string
	secret     string // 签名密钥，可选
	client     *http.Client
}

func newFeishuSender(webhookURL, secret string) *feishuSender {
	return &feishuSender{
		webhookURL: webhookURL,
		secret:     secret,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// feishuRequest 飞书 Webhook 请求体。
type feishuRequest struct {
	MsgType string     `json:"msg_type"`
	Content feishuText `json:"content"`
	Sign    string     `json:"sign,omitempty"`
}

type feishuText struct {
	Text string `json:"text"`
}

// Send 发送文本消息到飞书。
func (f *feishuSender) Send(message string) error {
	req := feishuRequest{
		MsgType: "text",
		Content: feishuText{Text: message},
	}

	// 如果配置了签名密钥，计算签名
	if f.secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		sign, err := feishuSign(timestamp, f.secret)
		if err != nil {
			return fmt.Errorf("计算飞书签名失败: %w", err)
		}
		req.Sign = sign
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("序列化飞书请求失败: %w", err)
	}

	resp, err := f.client.Post(f.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("飞书 Webhook 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 统一读取 Body，避免双读风险
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取飞书响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书 Webhook 返回 %d: %s", resp.StatusCode, string(respBody))
	}

	// 飞书返回 {"code":0,"msg":"success"} 或 {"code":9499,"msg":"bad request"}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("解析飞书响应失败: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("飞书 Webhook 错误: code=%d, msg=%s", result.Code, result.Msg)
	}

	return nil
}

// feishuSign 计算飞书签名：HMAC-SHA256(timestamp + "\n" + secret, secret) → base64。
func feishuSign(timestamp, secret string) (string, error) {
	strToSign := timestamp + "\n" + secret
	h := hmac.New(sha256.New, []byte(strToSign))
	if _, err := h.Write(nil); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
