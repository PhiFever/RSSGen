// Package notifier 提供通知功能，当获取上游数据失败时发送通知。
package notifier

import (
	"fmt"
	"log/slog"
	"time"
)

// ServiceConfig 是单个通知服务的配置。
type ServiceConfig struct {
	Type       string // feishu, ...
	WebhookURL string
	Secret     string // 飞书签名密钥（可选）
}

// Config 是通知器的配置。
type Config struct {
	Enabled     bool
	ServiceURLs []string        // 兼容旧配置（仅打日志）
	Services    []ServiceConfig // 通知服务列表
}

// sender 是通知发送接口。
type sender interface {
	Send(message string) error
}

// Notifier 是通知管理器。
type Notifier struct {
	enabled bool
	senders []sender
}

// New 创建一个新的通知器实例。
func New(cfg Config) *Notifier {
	var senders []sender
	for _, svc := range cfg.Services {
		switch svc.Type {
		case "feishu":
			senders = append(senders, newFeishuSender(svc.WebhookURL, svc.Secret))
		default:
			slog.Warn("未知的通知服务类型，已跳过", "type", svc.Type)
		}
	}

	return &Notifier{
		enabled: cfg.Enabled,
		senders: senders,
	}
}

// Notify 发送通知。
func (n *Notifier) Notify(feedKey string, statusCode int, errorMessage string) {
	if !n.enabled || len(n.senders) == 0 {
		return
	}

	message := fmt.Sprintf(
		"[RSSGen] 订阅源 %s 获取失败\n状态码: %d\n错误: %s\n时间: %s",
		feedKey,
		statusCode,
		errorMessage,
		time.Now().Format("2006-01-02 15:04:05"),
	)

	// 异步发送通知，不阻塞主流程
	go n.sendNotification(message)
}

func (n *Notifier) sendNotification(message string) {
	slog.Info("发送通知", "message", message)
	for _, s := range n.senders {
		if err := s.Send(message); err != nil {
			slog.Error("通知发送失败", "error", err)
		}
	}
}
