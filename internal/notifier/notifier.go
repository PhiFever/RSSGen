// Package notifier 提供通知功能，当获取上游数据失败时发送通知。
package notifier

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// Config 是通知器的配置。
type Config struct {
	Enabled     bool
	ServiceURLs []string // shoutrrr 通知 URL 列表
}

// Notifier 是通知管理器，零值表示不发送通知。
type Notifier struct {
	router *router.ServiceRouter
}

// New 创建通知器，启用时校验通知 URL。
func New(cfg Config) (*Notifier, error) {
	if !cfg.Enabled || len(cfg.ServiceURLs) == 0 {
		return &Notifier{}, nil
	}
	serviceRouter, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{}, cfg.ServiceURLs...)
	if err != nil {
		return nil, fmt.Errorf("初始化通知服务失败: %w", err)
	}
	return &Notifier{router: serviceRouter}, nil
}

// Notify 发送通知。
func (n *Notifier) Notify(feedKey string, statusCode int, errorMessage string) {
	if n.router == nil {
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
	for _, err := range n.router.Send(message, nil) {
		if err != nil {
			slog.Error("通知发送失败", "error", err)
		}
	}
}
