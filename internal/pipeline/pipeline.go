// Package pipeline 提供 feed 生成流水线。
package pipeline

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/feed"
	"github.com/PhiFever/RSSGen/internal/route"
)

// Config 是 Pipeline 的配置。
type Config struct {
	FeedCache     *cache.TTLCache
	ArticleStore  route.ArticleStore
	ScraperConfig config.ScraperConfig
	RoutesConfig  map[string]config.RouteConfig
}

// FeedVariant 是一次 feed 输出请求的规范化变体。
type FeedVariant struct {
	Format  string   `json:"format"`
	Limit   int      `json:"limit"`
	Include []string `json:"include,omitempty"`
}

// FeedRef 是同一个 feed 的健康键与 XML 缓存键的单一来源。
type FeedRef struct {
	RouteName string      `json:"route_name"`
	FeedID    string      `json:"feed_id"`
	PathParts []string    `json:"-"`
	HealthKey string      `json:"health_key"`
	CacheKey  string      `json:"cache_key"`
	Variant   FeedVariant `json:"variant"`
}

// RefreshResult 是一次流水线刷新的结果。
type RefreshResult struct {
	XML       string
	ItemCount int
	Ref       FeedRef
}

// Pipeline 完成从路由抓取到 XML 落缓存的全过程。
type Pipeline struct {
	feedCache     *cache.TTLCache
	articleStore  route.ArticleStore
	scraperConfig config.ScraperConfig
	routesConfig  map[string]config.RouteConfig

	routesMu sync.Mutex
	routes   map[string]route.Route
}

// New 创建 feed 生成流水线。
func New(cfg Config) *Pipeline {
	return &Pipeline{
		feedCache:     cfg.FeedCache,
		articleStore:  cfg.ArticleStore,
		scraperConfig: cfg.ScraperConfig,
		routesConfig:  cfg.RoutesConfig,
		routes:        make(map[string]route.Route),
	}
}

// OptionsFromQuery 从 HTTP 查询参数解析抓取选项。
func OptionsFromQuery(values url.Values) route.FetchOptions {
	opts := route.FetchOptions{
		Format: values.Get("format"),
	}
	if limitStr := values.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			opts.Limit = limit
		}
	}
	if includeStr := values.Get("include"); includeStr != "" {
		opts.Include = strings.Split(includeStr, ",")
	}
	return route.NormalizeFetchOptions(opts)
}

// OptionsFromFeedConfig 从配置中的 feed 条目构建抓取选项。
func OptionsFromFeedConfig(fc config.FeedConfig) route.FetchOptions {
	opts := route.FetchOptions{Limit: fc.Limit, Include: fc.Include}
	return route.NormalizeFetchOptions(opts)
}

// CacheKey 返回指定路由、路径与抓取选项对应的 feed XML 缓存键。
func CacheKey(routeName string, pathParams []string, opts route.FetchOptions) string {
	opts = route.NormalizeFetchOptions(opts)
	return cache.BuildFeedCacheKey(routeName, pathParams, cache.FeedVariant{
		Format:  opts.Format,
		Limit:   opts.Limit,
		Include: opts.Include,
	})
}

// FeedRef 返回当前 pipeline 配置下指定 feed 的结构化引用。
func (p *Pipeline) FeedRef(routeName string, pathParams []string, opts route.FetchOptions) FeedRef {
	return newFeedRef(routeName, pathParams, p.normalizeOptions(routeName, pathParams, opts))
}

// RouteAllowsDynamicObservation reports whether HTTP requests for routeName
// should be added to the dynamic feed catalog. Missing route config is treated
// as enabled to preserve the existing zero-config test and local route behavior.
func (p *Pipeline) RouteAllowsDynamicObservation(routeName string) bool {
	if p == nil {
		return false
	}
	rc, ok := p.routesConfig[routeName]
	return !ok || rc.Enabled
}

// Refresh 完成一次抓取、生成 XML 并写入缓存。
func (p *Pipeline) Refresh(routeName string, pathParams []string, opts route.FetchOptions) (RefreshResult, error) {
	ref := p.FeedRef(routeName, pathParams, opts)
	opts = route.FetchOptions{
		Limit:       ref.Variant.Limit,
		Include:     append([]string(nil), ref.Variant.Include...),
		Format:      ref.Variant.Format,
		ExtraParams: opts.ExtraParams,
	}

	rt, err := p.route(routeName)
	if err != nil {
		return RefreshResult{Ref: ref}, err
	}

	result, err := rt.Fetch(p.articleStore, pathParams, opts)
	if err != nil {
		return RefreshResult{Ref: ref}, err
	}

	xml, err := feed.Generate(&result.Info, result.Items, opts.Format)
	if err != nil {
		return RefreshResult{Ref: ref, ItemCount: len(result.Items)}, err
	}
	if p.feedCache != nil {
		p.feedCache.Set(ref.CacheKey, xml)
	}
	return RefreshResult{
		XML:       xml,
		ItemCount: len(result.Items),
		Ref:       ref,
	}, nil
}

func newFeedRef(routeName string, pathParams []string, opts route.FetchOptions) FeedRef {
	opts = route.NormalizeFetchOptions(opts)
	variant := FeedVariant{
		Format:  opts.Format,
		Limit:   opts.Limit,
		Include: append([]string(nil), opts.Include...),
	}
	return FeedRef{
		RouteName: routeName,
		FeedID:    strings.Join(pathParams, "/"),
		PathParts: append([]string(nil), pathParams...),
		HealthKey: cache.BuildCacheKey(routeName, pathParams),
		CacheKey:  CacheKey(routeName, pathParams, opts),
		Variant:   variant,
	}
}

func (p *Pipeline) normalizeOptions(routeName string, pathParams []string, opts route.FetchOptions) route.FetchOptions {
	opts = route.NormalizeFetchOptions(opts)
	if len(opts.Include) > 0 {
		return opts
	}

	rc := p.routesConfig[routeName]
	if len(pathParams) > 0 {
		feedID := pathParams[0]
		for _, fc := range rc.Feeds {
			if fc.UserID == feedID && len(fc.Include) > 0 {
				opts.Include = fc.Include
				return route.NormalizeFetchOptions(opts)
			}
		}
	}
	if len(rc.DefaultInclude) > 0 {
		opts.Include = rc.DefaultInclude
		return route.NormalizeFetchOptions(opts)
	}
	return opts
}

func (p *Pipeline) route(routeName string) (route.Route, error) {
	p.routesMu.Lock()
	defer p.routesMu.Unlock()

	if rt, ok := p.routes[routeName]; ok {
		return rt, nil
	}

	registry := route.GetRegistry()
	factory, ok := registry[routeName]
	if !ok {
		return nil, fmt.Errorf("路由不存在: %s", routeName)
	}

	rt := factory(config.ResolveRoute(p.scraperConfig, p.routesConfig[routeName]))
	p.routes[routeName] = rt
	return rt, nil
}
