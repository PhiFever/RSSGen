// Package notifier 提供通知功能，当获取上游数据失败时发送通知。
package notifier

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Config 是通知器的配置。
type Config struct {
	Enabled     bool
	ServiceURLs []string
}

// Notifier 是通知管理器。
type Notifier struct {
	enabled       bool
	serviceURLs   []string
	disabledFeeds map[string]bool
	mu            sync.RWMutex
}

// New 创建一个新的通知器实例。
func New(cfg Config) *Notifier {
	return &Notifier{
		enabled:       cfg.Enabled,
		serviceURLs:   cfg.ServiceURLs,
		disabledFeeds: make(map[string]bool),
	}
}

// IsBusinessError 判断是否为业务错误（4xx 响应码）。
func IsBusinessError(statusCode int) bool {
	businessErrors := []int{400, 401, 403, 404, 410, 422, 451}
	for _, code := range businessErrors {
		if statusCode == code {
			return true
		}
	}
	return false
}

// IsFeedDisabled 检查 feed 是否被禁用。
func (n *Notifier) IsFeedDisabled(feedKey string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.disabledFeeds[feedKey]
}

// DisableFeed 禁用单个 feed。
func (n *Notifier) DisableFeed(feedKey string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.disabledFeeds[feedKey] = true
}

// Notify 发送通知。
func (n *Notifier) Notify(feedKey string, statusCode int, errorMessage string) {
	if !n.enabled || len(n.serviceURLs) == 0 {
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
	// 简化实现：记录日志
	// 生产环境应集成 nikoksr/notify 进行实际通知发送
	slog.Info("发送通知", "message", message)

	for _, serviceURL := range n.serviceURLs {
		slog.Info("通知服务", "url", serviceURL)
		// TODO: 集成 nikoksr/notify 进行实际通知发送
	}
}
