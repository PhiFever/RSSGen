// Package health 管理 feed 健康状态，例如业务错误触发的进程内熔断。
package health

import "sync"

// defaultBusinessErrorCodes 是默认业务错误状态码；命中后 feed 会禁用到进程重启。
var defaultBusinessErrorCodes = []int{400, 401, 403, 404, 410, 422, 451}

// Config 控制 feed 健康状态判定。
type Config struct {
	BusinessErrorCodes []int
}

// FeedHealth 记录 feed 级熔断状态。
type FeedHealth struct {
	businessErrorCodes map[int]bool
	disabledFeeds      map[string]bool
	mu                 sync.RWMutex
}

// New 创建 feed 健康状态管理器。
func New(cfg Config) *FeedHealth {
	codes := cfg.BusinessErrorCodes
	if len(codes) == 0 {
		codes = defaultBusinessErrorCodes
	}
	codeSet := make(map[int]bool, len(codes))
	for _, c := range codes {
		codeSet[c] = true
	}
	return &FeedHealth{
		businessErrorCodes: codeSet,
		disabledFeeds:      make(map[string]bool),
	}
}

// IsBusinessError 判断状态码是否应触发 feed 熔断。
func (h *FeedHealth) IsBusinessError(statusCode int) bool {
	return h.businessErrorCodes[statusCode]
}

// IsFeedDisabled 检查 feed 是否已在当前进程内禁用。
func (h *FeedHealth) IsFeedDisabled(feedKey string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.disabledFeeds[feedKey]
}

// DisableFeed 禁用单个 feed，重启后恢复。
func (h *FeedHealth) DisableFeed(feedKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disabledFeeds[feedKey] = true
}
