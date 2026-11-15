// Package health 管理 feed 健康状态，例如业务错误触发的进程内熔断。
package health

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/PhiFever/RSSGen/internal/route"
)

// defaultBusinessErrorCodes 是默认业务错误状态码；命中后 feed 会禁用到进程重启。
var defaultBusinessErrorCodes = []int{400, 401, 403, 404, 410, 422, 451}

// Config 控制 feed 健康状态判定。
type Config struct {
	BusinessErrorCodes []int
	// OnDisabled 在 feed 首次禁用时调用；nil 表示不通知。
	OnDisabled func(feedKey string, statusCode int, errorMessage string)
}

// FeedHealth 记录 feed 级熔断状态。
type FeedHealth struct {
	businessErrorCodes map[int]bool
	disabledFeeds      map[string]bool
	mu                 sync.RWMutex
	onDisabled         func(string, int, string)
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
		onDisabled:         cfg.OnDisabled,
	}
}

// isBusinessError 判断状态码是否应触发 feed 熔断。
func (h *FeedHealth) isBusinessError(statusCode int) bool {
	return h.businessErrorCodes[statusCode]
}

// RecordFailure 记录 feed 失败，在业务错误首次禁用 feed 时记录日志并通知。
func (h *FeedHealth) RecordFailure(feedKey string, err error) {
	statusCode := extractStatusCode(err)
	if statusCode == 0 || !h.isBusinessError(statusCode) {
		return
	}
	h.mu.Lock()
	if h.disabledFeeds[feedKey] {
		h.mu.Unlock()
		return
	}
	h.disabledFeeds[feedKey] = true
	h.mu.Unlock()
	slog.Warn("feed 已禁用（业务错误）", "key", feedKey, "status", statusCode)
	if h.onDisabled != nil {
		h.onDisabled(feedKey, statusCode, err.Error())
	}
}

// IsFeedDisabled 检查 feed 是否已在当前进程内禁用。
func (h *FeedHealth) IsFeedDisabled(feedKey string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.disabledFeeds[feedKey]
}

func extractStatusCode(err error) int {
	var he *route.HTTPError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	return 0
}
