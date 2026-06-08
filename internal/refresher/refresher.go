// Package refresher 提供后台刷新调度器，负责预热与定期刷新。
package refresher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/feed"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/route"
)

// ArticleStore 是文章存储接口。
type ArticleStore interface {
	Get(routeName, articleID string) (content string, found bool, err error)
	Save(routeName, articleID, content string) error
	HasArticles(routeName string) (bool, error)
}

// Config 是刷新器的配置。
type Config struct {
	FeedCache      *cache.TTLCache
	ArticleStore   ArticleStore
	Notifier       *notifier.Notifier
	StartupDelay   int
	MaxRetries     int
	RetryBaseDelay int
	RoutesConfig   map[string]config.RouteConfig
}

// Refresher 是后台刷新调度器。
type Refresher struct {
	feedCache      *cache.TTLCache
	articleStore   ArticleStore
	notifier       *notifier.Notifier
	startupDelay   int
	maxRetries     int
	retryBaseDelay int
	routesConfig   map[string]config.RouteConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	pending    map[string]bool
	pendingMu  sync.Mutex
	errorStats map[string]*ErrorStatus
	statsMu    sync.RWMutex
}

// ErrorStatus 记录 feed 的错误状态。
type ErrorStatus struct {
	LastSuccess string `json:"last_success,omitempty"`
	Error       string `json:"error,omitempty"`
	ItemCount   int    `json:"item_count"`
}

// New 创建一个新的刷新器实例。
func New(cfg Config) *Refresher {
	ctx, cancel := context.WithCancel(context.Background())

	startupDelay := cfg.StartupDelay
	if startupDelay <= 0 {
		startupDelay = 5
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	retryBaseDelay := cfg.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = 5
	}

	return &Refresher{
		feedCache:      cfg.FeedCache,
		articleStore:   cfg.ArticleStore,
		notifier:       cfg.Notifier,
		startupDelay:   startupDelay,
		maxRetries:     maxRetries,
		retryBaseDelay: retryBaseDelay,
		routesConfig:   cfg.RoutesConfig,
		ctx:            ctx,
		cancel:         cancel,
		pending:        make(map[string]bool),
		errorStats:     make(map[string]*ErrorStatus),
	}
}

// Start 启动后台刷新任务。
func (r *Refresher) Start() {
	for routeName, rc := range r.routesConfig {
		if !rc.Enabled {
			continue
		}
		if rc.RefreshInterval <= 0 {
			slog.Info("路由未配置 refresh_interval，定时刷新已关闭", "route", routeName)
			continue
		}

		r.wg.Add(1)
		go r.runRouteLoop(routeName, rc)
		slog.Info("路由调度任务已创建", "route", routeName)
	}
}

// Stop 停止所有后台刷新任务。
func (r *Refresher) Stop() {
	r.cancel()
	r.wg.Wait()
	slog.Info("BackgroundRefresher 已停止")
}

// Trigger 动态触发：未知 feed 首次访问时调用，非阻塞。
func (r *Refresher) Trigger(routeName string, pathParams []string, queryParams map[string]string) {
	cacheKey := buildCacheKey(routeName, pathParams)

	r.pendingMu.Lock()
	if r.pending[cacheKey] {
		r.pendingMu.Unlock()
		return
	}
	r.pending[cacheKey] = true
	r.pendingMu.Unlock()

	// 检查 feed 是否被禁用
	if r.notifier.IsFeedDisabled(cacheKey) {
		r.pendingMu.Lock()
		delete(r.pending, cacheKey)
		r.pendingMu.Unlock()
		return
	}

	go r.refreshOne(routeName, pathParams, queryParams)
}

// GetStatus 返回按路由分组的状态。
func (r *Refresher) GetStatus() map[string]map[string]*ErrorStatus {
	r.statsMu.RLock()
	defer r.statsMu.RUnlock()

	grouped := make(map[string]map[string]*ErrorStatus)
	for cacheKey, status := range r.errorStats {
		routeName, feedID := splitCacheKey(cacheKey)
		if grouped[routeName] == nil {
			grouped[routeName] = make(map[string]*ErrorStatus)
		}
		grouped[routeName][feedID] = status
	}
	return grouped
}

// runRouteLoop 是单路由的生命周期：startup_delay -> 预热判定 -> 周期刷新循环。
func (r *Refresher) runRouteLoop(routeName string, rc config.RouteConfig) {
	defer r.wg.Done()

	// startup_delay
	slog.Info("等待网络就绪", "route", routeName, "delay", r.startupDelay)
	select {
	case <-r.ctx.Done():
		return
	case <-time.After(time.Duration(r.startupDelay) * time.Second):
	}

	// 预热阶段
	if rc.PreheatOnStartup {
		hasData, _ := r.articleStore.HasArticles(routeName)
		if hasData {
			slog.Info("路由已有数据，跳过预热", "route", routeName)
		} else {
			slog.Info("开始预热", "route", routeName)
			r.refreshFeeds("预热", routeName, rc)
		}
	} else {
		slog.Info("预热已关闭", "route", routeName)
	}

	// 周期刷新阶段
	interval := time.Duration(rc.RefreshInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.refreshFeeds("定时刷新", routeName, rc)
		}
	}
}

// refreshFeeds 刷新路由下的所有 feed。
func (r *Refresher) refreshFeeds(label string, routeName string, rc config.RouteConfig) {
	if len(rc.Feeds) == 0 {
		slog.Info("未配置 feed 列表，跳过", "route", routeName, "label", label)
		return
	}

	slog.Info("开始刷新", "route", routeName, "label", label, "count", len(rc.Feeds))
	for _, fc := range rc.Feeds {
		if fc.UserID == "" {
			slog.Warn("feed 配置缺少 user_id，跳过", "route", routeName)
			continue
		}
		pathParams := []string{fc.UserID}
		extraParams := make(map[string]string)
		if fc.Limit > 0 {
			extraParams["limit"] = fmt.Sprintf("%d", fc.Limit)
		}
		r.refreshOne(routeName, pathParams, extraParams)
	}
	slog.Info("刷新完成", "route", routeName, "label", label)
}

// refreshOne 刷新单个 feed。
func (r *Refresher) refreshOne(routeName string, pathParams []string, extraParams map[string]string) {
	cacheKey := buildCacheKey(routeName, pathParams)

	// 标记为 pending
	r.pendingMu.Lock()
	r.pending[cacheKey] = true
	r.pendingMu.Unlock()

	defer func() {
		r.pendingMu.Lock()
		delete(r.pending, cacheKey)
		r.pendingMu.Unlock()
	}()

	// 检查 feed 是否被禁用
	if r.notifier.IsFeedDisabled(cacheKey) {
		slog.Warn("feed 已被禁用，跳过刷新", "key", cacheKey)
		return
	}

	// 查找路由工厂
	registry := route.GetRegistry()
	factory, ok := registry[routeName]
	if !ok {
		slog.Error("路由不存在", "route", routeName)
		return
	}

	// 构建配置
	rc := r.routesConfig[routeName]
	mergedConfig := buildMergedConfig(rc)

	// 构建 FetchOptions
	opts := route.FetchOptions{
		Limit: 20,
	}
	if v, ok := extraParams["limit"]; ok {
		fmt.Sscanf(v, "%d", &opts.Limit)
	}

	var lastErr error

	for attempt := 0; attempt < r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(r.retryBaseDelay) * time.Second * time.Duration(1<<(attempt-1))
			slog.Info("重试中", "key", cacheKey, "attempt", attempt+1, "delay", delay)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(delay):
			}
		} else {
			slog.Info("正在刷新", "key", cacheKey)
		}

		// 创建路由实例
		rt := factory(mergedConfig)

		// 抓取
		items, err := rt.Fetch(r.articleStore, pathParams, opts)
		if err != nil {
			lastErr = err
			slog.Warn("刷新失败", "key", cacheKey, "attempt", attempt+1, "error", err)
			continue
		}

		// 获取 feed 信息
		info, err := rt.FeedInfo(pathParams)
		if err != nil {
			lastErr = err
			slog.Warn("获取 feed 信息失败", "key", cacheKey, "attempt", attempt+1, "error", err)
			continue
		}

		// 生成 feed XML
		xml, err := feed.Generate(info, items, "atom")
		if err != nil {
			lastErr = err
			slog.Warn("生成 feed 失败", "key", cacheKey, "attempt", attempt+1, "error", err)
			continue
		}

		// 写入缓存
		r.feedCache.Set(cacheKey, xml)

		// 更新状态
		r.statsMu.Lock()
		r.errorStats[cacheKey] = &ErrorStatus{
			LastSuccess: time.Now().UTC().Format(time.RFC3339),
			ItemCount:   len(items),
		}
		r.statsMu.Unlock()

		slog.Info("刷新完成", "key", cacheKey, "items", len(items))
		return
	}

	// 所有重试都失败
	slog.Error("刷新失败：所有重试均失败", "key", cacheKey, "retries", r.maxRetries)

	r.statsMu.Lock()
	r.errorStats[cacheKey] = &ErrorStatus{
		Error:     fmt.Sprintf("%v", lastErr),
		ItemCount: 0,
	}
	r.statsMu.Unlock()

	// 检查是否是业务错误
	if lastErr != nil {
		statusCode := extractStatusCode(lastErr)
		if statusCode > 0 && notifier.IsBusinessError(statusCode) {
			r.notifier.Notify(cacheKey, statusCode, fmt.Sprintf("%v", lastErr))
			r.notifier.DisableFeed(cacheKey)
			slog.Warn("feed 已禁用（业务错误）", "key", cacheKey, "status", statusCode)
		}
	}
}

// buildCacheKey 构建缓存键。
func buildCacheKey(routeName string, pathParams []string) string {
	key := routeName
	for _, p := range pathParams {
		key += "/" + p
	}
	return key
}

// splitCacheKey 分割缓存键。
func splitCacheKey(key string) (routeName, feedID string) {
	for i, c := range key {
		if c == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// buildMergedConfig 合并全局配置与路由配置。
func buildMergedConfig(rc config.RouteConfig) map[string]interface{} {
	result := make(map[string]interface{})
	if rc.Cookie != "" {
		result["cookie"] = rc.Cookie
	}
	if rc.RateLimit != nil {
		result["rate_limit"] = *rc.RateLimit
	}
	if rc.Proxy != nil {
		result["proxy"] = *rc.Proxy
	}
	if rc.Impersonate != nil {
		result["impersonate"] = *rc.Impersonate
	}
	if len(rc.DefaultInclude) > 0 {
		defaultInc := make([]interface{}, len(rc.DefaultInclude))
		for i, v := range rc.DefaultInclude {
			defaultInc[i] = v
		}
		result["default_include"] = defaultInc
	}
	if rc.IncludeSelfInteraction != nil {
		result["include_self_interaction"] = *rc.IncludeSelfInteraction
	}

	if len(rc.Feeds) > 0 {
		feeds := make([]interface{}, len(rc.Feeds))
		for i, f := range rc.Feeds {
			feedMap := map[string]interface{}{
				"user_id": f.UserID,
			}
			if f.Alias != "" {
				feedMap["alias"] = f.Alias
			}
			if f.Limit > 0 {
				feedMap["limit"] = f.Limit
			}
			if len(f.Include) > 0 {
				include := make([]interface{}, len(f.Include))
				for j, v := range f.Include {
					include[j] = v
				}
				feedMap["include"] = include
			}
			feeds[i] = feedMap
		}
		result["feeds"] = feeds
	}

	return result
}

// extractStatusCode 从错误中提取 HTTP 状态码。
func extractStatusCode(err error) int {
	if err == nil {
		return 0
	}
	// 简化实现：检查错误消息中是否包含 HTTP 状态码
	errMsg := err.Error()
	var statusCode int
	if n, _ := fmt.Sscanf(errMsg, "HTTP %d", &statusCode); n == 1 {
		return statusCode
	}
	return 0
}
