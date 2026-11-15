// Package refresher 提供后台刷新调度器，负责预热与定期刷新。
package refresher

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/route"
)

const (
	statusSourceConfig  = "config"
	statusSourceDynamic = "dynamic"
)

// FeedCatalog 向刷新器提供动态观察到的 feed 变体。
type FeedCatalog interface {
	List(routeName string) []pipeline.FeedRef
}

// Config 是刷新器的配置。
type Config struct {
	FeedHealth     *health.FeedHealth
	Pipeline       *pipeline.Pipeline
	FeedCatalog    FeedCatalog
	StartupDelay   int
	MaxRetries     int
	RetryBaseDelay int
	PreinitURL     string
	RoutesConfig   map[string]config.RouteConfig
}

// Refresher 是后台刷新调度器。
type Refresher struct {
	feedHealth     *health.FeedHealth
	pipe           *pipeline.Pipeline
	feedCatalog    FeedCatalog
	startupDelay   int
	maxRetries     int
	retryBaseDelay int
	preinitURL     string
	routesConfig   map[string]config.RouteConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	errorStats map[string]*ErrorStatus
	statsMu    sync.RWMutex
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
	RouteName   string               `json:"-"`
	FeedID      string               `json:"-"`
	CacheKey    string               `json:"cache_key,omitempty"`
	Variant     pipeline.FeedVariant `json:"variant"`
	LastSuccess string               `json:"last_success,omitempty"`
	Error       string               `json:"error,omitempty"`
	ItemCount   int                  `json:"item_count"`
	Source      string               `json:"source,omitempty"`
}

type refreshTarget struct {
	ref    pipeline.FeedRef
	source string
}

// New 创建一个新的刷新器实例。
func New(cfg Config) *Refresher {
	ctx, cancel := context.WithCancel(context.Background())

	startupDelay := cfg.StartupDelay
	if startupDelay <= 0 {
		startupDelay = config.DefaultStartupDelay
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = config.DefaultMaxRetries
	}
	retryBaseDelay := cfg.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = config.DefaultRetryBaseDelay
	}

	return &Refresher{
		feedHealth:     cfg.FeedHealth,
		pipe:           cfg.Pipeline,
		feedCatalog:    cfg.FeedCatalog,
		startupDelay:   startupDelay,
		maxRetries:     maxRetries,
		retryBaseDelay: retryBaseDelay,
		preinitURL:     cfg.PreinitURL,
		routesConfig:   cfg.RoutesConfig,
		ctx:            ctx,
		cancel:         cancel,
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
		if !rc.NeedsScheduling() {
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

// Stop 停止所有后台刷新任务。
func (r *Refresher) Stop() {
	r.cancel()
	r.wg.Wait()
	slog.Info("BackgroundRefresher 已停止")
}

// GetStatus 返回按路由、feed 分组且按缓存键排序的各变体状态。
func (r *Refresher) GetStatus() map[string]map[string][]ErrorStatus {
	grouped := make(map[string]map[string][]ErrorStatus)
	seen := make(map[string]bool)
	appendStatus := func(status ErrorStatus) {
		if grouped[status.RouteName] == nil {
			grouped[status.RouteName] = make(map[string][]ErrorStatus)
		}
		grouped[status.RouteName][status.FeedID] = append(grouped[status.RouteName][status.FeedID], status)
		seen[status.CacheKey] = true
	}
	r.statsMu.RLock()
	for _, status := range r.errorStats {
		appendStatus(*status)
	}
	r.statsMu.RUnlock()
	if r.feedCatalog != nil {
		for routeName := range r.routesConfig {
			for _, ref := range r.feedCatalog.List(routeName) {
				if seen[ref.CacheKey] {
					continue
				}
				appendStatus(ErrorStatus{
					RouteName: ref.RouteName,
					FeedID:    ref.FeedID,
					CacheKey:  ref.CacheKey,
					Variant:   ref.Variant,
					Source:    statusSourceDynamic,
				})
			}
		}
	}
	for _, feeds := range grouped {
		for _, variants := range feeds {
			slices.SortFunc(variants, func(a, b ErrorStatus) int { return cmp.Compare(a.CacheKey, b.CacheKey) })
		}
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
		slog.Info("开始预热", "route", routeName)
		r.refreshFeeds(refreshPhasePreheat, routeName, rc)
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

// refreshFeeds 刷新路由下的所有静态和动态 feed。
func (r *Refresher) refreshFeeds(phase refreshPhase, routeName string, rc config.RouteConfig) {
	label := phase.label
	targets := r.refreshTargets(routeName, rc)
	r.pruneStatus(routeName, targets)
	if len(targets) == 0 {
		slog.Info("未配置 feed 列表，跳过", "route", routeName, "label", label)
		return
	}

	slog.Info("开始刷新", "route", routeName, "label", label, "count", len(targets))
	if !r.sleepRefreshJitter(phase, routeName, rc) {
		return
	}
	for _, target := range targets {
		r.refreshOne(target.ref.RouteName, target.ref.PathParts, fetchOptionsFromVariant(target.ref.Variant), target.source)
	}
	slog.Info("刷新完成", "route", routeName, "label", label)
}

// pruneStatus 删除该路由下已不在本轮刷新目标里的状态，即被 feedcatalog 淘汰的动态变体。
func (r *Refresher) pruneStatus(routeName string, targets []refreshTarget) {
	active := make(map[string]bool, len(targets))
	for _, target := range targets {
		active[target.ref.CacheKey] = true
	}
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	for key, status := range r.errorStats {
		if status.RouteName == routeName && !active[key] {
			delete(r.errorStats, key)
		}
	}
}

func (r *Refresher) refreshTargets(routeName string, rc config.RouteConfig) []refreshTarget {
	targets := make([]refreshTarget, 0, len(rc.Feeds))
	seen := make(map[string]bool, len(rc.Feeds))

	for _, fc := range rc.Feeds {
		if fc.UserID == "" {
			slog.Warn("feed 配置缺少 user_id，跳过", "route", routeName)
			continue
		}
		pathParts := []string{fc.UserID}
		ref := r.pipe.FeedRef(routeName, pathParts, pipeline.OptionsFromFeedConfig(fc))
		targets = append(targets, refreshTarget{ref: ref, source: statusSourceConfig})
		seen[ref.CacheKey] = true
	}

	if r.feedCatalog == nil {
		return targets
	}
	for _, ref := range r.feedCatalog.List(routeName) {
		if seen[ref.CacheKey] {
			continue
		}
		targets = append(targets, refreshTarget{ref: ref, source: statusSourceDynamic})
		seen[ref.CacheKey] = true
	}
	return targets
}

func fetchOptionsFromVariant(variant pipeline.FeedVariant) route.FetchOptions {
	return route.FetchOptions{
		Format:  variant.Format,
		Limit:   variant.Limit,
		Include: append([]string(nil), variant.Include...),
	}
}

func (r *Refresher) sleepRefreshJitter(phase refreshPhase, routeName string, rc config.RouteConfig) bool {
	if !phase.scheduled || rc.RefreshJitter <= 0 {
		return true
	}

	maxJitter := time.Duration(rc.RefreshJitter) * time.Second
	if maxJitter <= 0 {
		return true
	}
	delay := time.Duration(rand.Int63n(int64(maxJitter)))
	if delay <= 0 {
		return true
	}

	slog.Info("定时刷新随机延迟", "route", routeName, "delay", delay)
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func (r *Refresher) refreshOne(routeName string, pathParams []string, opts route.FetchOptions, source string) {
	ref := r.pipe.FeedRef(routeName, pathParams, opts)

	// 检查 feed 是否被禁用
	if r.feedHealth.IsFeedDisabled(ref.HealthKey) {
		slog.Warn("feed 已被禁用，跳过刷新", "key", ref.HealthKey)
		return
	}

	var lastErr error

	for attempt := 0; attempt < r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(r.retryBaseDelay) * time.Second * time.Duration(1<<(attempt-1))
			slog.Info("重试中", "key", ref.CacheKey, "attempt", attempt+1, "delay", delay)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(delay):
			}
		} else {
			slog.Info("正在刷新", "key", ref.CacheKey)
		}

		result, err := r.pipe.Refresh(routeName, pathParams, opts)
		if err != nil {
			lastErr = err
			slog.Warn("刷新失败", "key", ref.CacheKey, "attempt", attempt+1, "error", err)
			continue
		}

		// 更新状态
		r.statsMu.Lock()
		r.errorStats[result.Ref.CacheKey] = &ErrorStatus{
			RouteName:   result.Ref.RouteName,
			FeedID:      result.Ref.FeedID,
			CacheKey:    result.Ref.CacheKey,
			Variant:     result.Ref.Variant,
			LastSuccess: time.Now().UTC().Format(time.RFC3339),
			ItemCount:   result.ItemCount,
			Source:      source,
		}
		r.statsMu.Unlock()

		slog.Info("刷新完成", "key", result.Ref.CacheKey, "items", result.ItemCount)
		return
	}

	// 所有重试都失败
	slog.Error("刷新失败：所有重试均失败", "key", ref.CacheKey, "retries", r.maxRetries)

	r.statsMu.Lock()
	r.errorStats[ref.CacheKey] = &ErrorStatus{
		RouteName: ref.RouteName,
		FeedID:    ref.FeedID,
		CacheKey:  ref.CacheKey,
		Variant:   ref.Variant,
		Error:     fmt.Sprintf("%v", lastErr),
		ItemCount: 0,
		Source:    source,
	}
	r.statsMu.Unlock()

	r.feedHealth.RecordFailure(ref.HealthKey, lastErr)
}
