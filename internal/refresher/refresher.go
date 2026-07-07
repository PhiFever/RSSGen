// Package refresher 提供后台刷新调度器，负责预热与定期刷新。
package refresher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
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
	FeedHealth     *health.FeedHealth
	Pipeline       *pipeline.Pipeline
	StartupDelay   int
	MaxRetries     int
	RetryBaseDelay int
	PreinitURL     string
	ScraperConfig  config.ScraperConfig
	RoutesConfig   map[string]config.RouteConfig
}

// Refresher 是后台刷新调度器。
type Refresher struct {
	articleStore   ArticleStore
	notifier       *notifier.Notifier
	feedHealth     *health.FeedHealth
	pipe           *pipeline.Pipeline
	startupDelay   int
	maxRetries     int
	retryBaseDelay int
	preinitURL     string
	routesConfig   map[string]config.RouteConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	pending    map[string]bool
	pendingMu  sync.Mutex
	errorStats map[string]*ErrorStatus
	statsMu    sync.RWMutex

	jitterDurationFn func(time.Duration) time.Duration
	sleepFn          func(time.Duration) bool
}

type refreshPhase struct {
	label     string
	scheduled bool
}

var (
	refreshPhasePreheat   = refreshPhase{label: "预热"}
	refreshPhaseScheduled = refreshPhase{label: "定时刷新", scheduled: true}
)

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
	pipe := cfg.Pipeline
	if pipe == nil {
		pipe = pipeline.New(pipeline.Config{
			FeedCache:     cfg.FeedCache,
			ArticleStore:  cfg.ArticleStore,
			ScraperConfig: cfg.ScraperConfig,
			RoutesConfig:  cfg.RoutesConfig,
		})
	}
	feedHealth := cfg.FeedHealth
	if feedHealth == nil {
		feedHealth = health.New(health.Config{})
	}

	return &Refresher{
		articleStore:   cfg.ArticleStore,
		notifier:       cfg.Notifier,
		feedHealth:     feedHealth,
		pipe:           pipe,
		startupDelay:   startupDelay,
		maxRetries:     maxRetries,
		retryBaseDelay: retryBaseDelay,
		preinitURL:     cfg.PreinitURL,
		routesConfig:   cfg.RoutesConfig,
		ctx:            ctx,
		cancel:         cancel,
		pending:        make(map[string]bool),
		errorStats:     make(map[string]*ErrorStatus),
	}
}

// Start 启动后台刷新任务。
func (r *Refresher) Start() {
	r.preinitHTTPClient()

	for routeName, rc := range r.routesConfig {
		if !rc.Enabled {
			continue
		}
		if !shouldScheduleRoute(rc) {
			slog.Info("路由未配置 refresh_interval 且未启用预热，调度已关闭", "route", routeName)
			continue
		}

		r.wg.Add(1)
		go r.runRouteLoop(routeName, rc)
		slog.Info("路由调度任务已创建", "route", routeName)
	}
}

func (r *Refresher) preinitHTTPClient() {
	if r.preinitURL == "" {
		slog.Info("preinit_url 为空，跳过 HTTP 客户端预初始化")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(r.preinitURL)
	if err != nil {
		slog.Warn("HTTP 客户端预初始化失败", "url", r.preinitURL, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("HTTP 客户端预初始化响应异常", "url", r.preinitURL, "status", resp.StatusCode)
		return
	}
	slog.Info("HTTP 客户端预初始化完成", "url", r.preinitURL)
}

func shouldScheduleRoute(rc config.RouteConfig) bool {
	return rc.Enabled && (rc.PreheatOnStartup || rc.RefreshInterval > 0)
}

func defaultJitterDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}

// Stop 停止所有后台刷新任务。
func (r *Refresher) Stop() {
	r.cancel()
	r.wg.Wait()
	slog.Info("BackgroundRefresher 已停止")
}

// Trigger 动态触发：未知 feed 首次访问时调用，非阻塞。
func (r *Refresher) Trigger(routeName string, pathParams []string, queryParams map[string]string) {
	feedKey := cache.BuildCacheKey(routeName, pathParams)
	opts := pipeline.OptionsFromParams(queryParams)
	refreshKey := r.pipe.CacheKey(routeName, pathParams, opts)

	r.pendingMu.Lock()
	if r.pending[refreshKey] {
		r.pendingMu.Unlock()
		return
	}
	r.pending[refreshKey] = true
	r.pendingMu.Unlock()

	// 检查 feed 是否被禁用
	if r.feedHealth.IsFeedDisabled(feedKey) {
		r.pendingMu.Lock()
		delete(r.pending, refreshKey)
		r.pendingMu.Unlock()
		return
	}

	go r.refreshOneWithOptions(routeName, pathParams, opts)
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
		statusCopy := *status
		grouped[routeName][feedID] = &statusCopy
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
		hasData := false
		if r.articleStore != nil {
			hasData, _ = r.articleStore.HasArticles(routeName)
		}
		if hasData {
			slog.Info("路由已有数据，跳过预热", "route", routeName)
		} else {
			slog.Info("开始预热", "route", routeName)
			r.refreshFeeds(refreshPhasePreheat, routeName, rc)
		}
	} else {
		slog.Info("预热已关闭", "route", routeName)
	}

	if rc.RefreshInterval <= 0 {
		slog.Info("路由未配置 refresh_interval，定时刷新已关闭", "route", routeName)
		return
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
			r.refreshFeeds(refreshPhaseScheduled, routeName, rc)
		}
	}
}

// refreshFeeds 刷新路由下的所有 feed。
func (r *Refresher) refreshFeeds(phase refreshPhase, routeName string, rc config.RouteConfig) {
	label := phase.label
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
		if !r.sleepRefreshJitter(phase, routeName, fc.UserID, rc) {
			return
		}
		pathParams := []string{fc.UserID}
		r.refreshOneWithOptions(routeName, pathParams, pipeline.OptionsFromFeedConfig(fc))
	}
	slog.Info("刷新完成", "route", routeName, "label", label)
}

func (r *Refresher) sleepRefreshJitter(phase refreshPhase, routeName string, feedID string, rc config.RouteConfig) bool {
	if !phase.scheduled || rc.RefreshJitter <= 0 {
		return true
	}

	maxJitter := time.Duration(rc.RefreshJitter) * time.Second
	jitterDuration := defaultJitterDuration
	if r.jitterDurationFn != nil {
		jitterDuration = r.jitterDurationFn
	}
	delay := jitterDuration(maxJitter)
	if delay <= 0 {
		return true
	}

	slog.Info("定时刷新随机延迟", "route", routeName, "feed", feedID, "delay", delay)
	if r.sleepFn != nil {
		return r.sleepFn(delay)
	}
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// refreshOne 刷新单个 feed。
func (r *Refresher) refreshOne(routeName string, pathParams []string, extraParams map[string]string) {
	r.refreshOneWithOptions(routeName, pathParams, pipeline.OptionsFromParams(extraParams))
}

func (r *Refresher) refreshOneWithOptions(routeName string, pathParams []string, opts route.FetchOptions) {
	feedKey := cache.BuildCacheKey(routeName, pathParams)
	refreshKey := r.pipe.CacheKey(routeName, pathParams, opts)

	// 标记为 pending
	r.pendingMu.Lock()
	r.pending[refreshKey] = true
	r.pendingMu.Unlock()

	defer func() {
		r.pendingMu.Lock()
		delete(r.pending, refreshKey)
		r.pendingMu.Unlock()
	}()

	// 检查 feed 是否被禁用
	if r.feedHealth.IsFeedDisabled(feedKey) {
		slog.Warn("feed 已被禁用，跳过刷新", "key", feedKey)
		return
	}

	var lastErr error

	for attempt := 0; attempt < r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(r.retryBaseDelay) * time.Second * time.Duration(1<<(attempt-1))
			slog.Info("重试中", "key", refreshKey, "attempt", attempt+1, "delay", delay)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(delay):
			}
		} else {
			slog.Info("正在刷新", "key", refreshKey)
		}

		result, err := r.pipe.Refresh(routeName, pathParams, opts)
		if err != nil {
			lastErr = err
			slog.Warn("刷新失败", "key", refreshKey, "attempt", attempt+1, "error", err)
			continue
		}

		// 更新状态
		r.statsMu.Lock()
		r.errorStats[result.CacheKey] = &ErrorStatus{
			LastSuccess: time.Now().UTC().Format(time.RFC3339),
			ItemCount:   result.ItemCount,
		}
		r.statsMu.Unlock()

		slog.Info("刷新完成", "key", result.CacheKey, "items", result.ItemCount)
		return
	}

	// 所有重试都失败
	slog.Error("刷新失败：所有重试均失败", "key", refreshKey, "retries", r.maxRetries)

	r.statsMu.Lock()
	r.errorStats[refreshKey] = &ErrorStatus{
		Error:     fmt.Sprintf("%v", lastErr),
		ItemCount: 0,
	}
	r.statsMu.Unlock()

	// 检查是否是业务错误
	if lastErr != nil {
		statusCode := extractStatusCode(lastErr)
		if statusCode > 0 && r.feedHealth.IsBusinessError(statusCode) {
			if r.notifier != nil {
				r.notifier.Notify(feedKey, statusCode, fmt.Sprintf("%v", lastErr))
			}
			r.feedHealth.DisableFeed(feedKey)
			slog.Warn("feed 已禁用（业务错误）", "key", feedKey, "status", statusCode)
		}
	}
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

// extractStatusCode 从错误链中提取 HTTP 状态码，无则返回 0。
func extractStatusCode(err error) int {
	var he *route.HTTPError
	if errors.As(err, &he) {
		return he.StatusCode
	}
	return 0
}
