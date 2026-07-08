// Package route 定义路由基类、数据结构和注册机制。
package route

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/scraper"
)

// HTTPError 表示上游 API 返回的非 2xx 状态。
// 携带 StatusCode 以便上层用 errors.As 提取，判定是否业务错误并禁用 feed。
type HTTPError struct {
	StatusCode int
	URL        string
}

func (e *HTTPError) Error() string {
	if e.URL != "" {
		return fmt.Sprintf("上游返回 HTTP %d: %s", e.StatusCode, e.URL)
	}
	return fmt.Sprintf("上游返回 HTTP %d", e.StatusCode)
}

// NewHTTPError 构造携带状态码的上游 HTTP 错误。
func NewHTTPError(statusCode int, url string) error {
	return &HTTPError{StatusCode: statusCode, URL: url}
}

// BaseRoute 提供所有路由共享的元数据与 scraper 生命周期管理。
type BaseRoute struct {
	name string

	scraperMu sync.Mutex
	scraper   *scraper.Scraper
}

// NewBaseRoute 创建可嵌入的路由基座。
func NewBaseRoute(name string) BaseRoute {
	return BaseRoute{
		name: name,
	}
}

// Name 返回路由名称。
func (r *BaseRoute) Name() string { return r.name }

// Scraper 返回路由复用的 scraper，并在每次获取时刷新 Cookie。
func (r *BaseRoute) Scraper(cfg config.ResolvedRouteConfig) (*scraper.Scraper, error) {
	r.scraperMu.Lock()
	defer r.scraperMu.Unlock()

	cookies := scraper.ParseCookieString(cfg.Cookie)
	if r.scraper != nil {
		r.scraper.SetCookies(cookies)
		return r.scraper, nil
	}

	sc, err := scraper.New(scraper.Config{
		Cookies:     cookies,
		RateLimit:   cfg.RateLimit,
		Proxy:       cfg.Proxy,
		Impersonate: cfg.Impersonate,
	})
	if err != nil {
		return nil, err
	}
	r.scraper = sc
	return r.scraper, nil
}

// FeedItem 表示一个 feed 条目。
type FeedItem struct {
	Title      string      // 条目标题
	Link       string      // 条目链接
	Content    string      // HTML 正文
	PubDate    *time.Time  // 发布时间
	Author     string      // 作者
	GUID       string      // 唯一标识，默认用 Link
	Enclosures []Enclosure // 附件（图片等）
	Categories []string    // 分类标签
}

// Enclosure 表示 feed 条目的附件。
type Enclosure struct {
	URL    string
	Length string
	Type   string
}

// FeedInfo 表示 feed 的元信息。
type FeedInfo struct {
	Title       string
	Link        string
	Description string
}

// FeedResult 是一次抓取的完整结果，包含 feed 元信息和条目列表。
type FeedResult struct {
	Info  FeedInfo
	Items []FeedItem
}

// ArticleStore 是文章持久化存储的接口。
type ArticleStore interface {
	Get(routeName, articleID string) (content string, found bool, err error)
	Save(routeName, articleID, content string) error
	HasArticles(routeName string) (bool, error)
}

// Route 是路由基类接口，每个数据源实现此接口。
type Route interface {
	// Name 返回路由名称，决定 URL 前缀 /feed/{name}/...
	Name() string
	// Fetch 抓取数据源，返回 feed 元信息和 FeedItem 列表。
	Fetch(articleStore ArticleStore, pathParams []string, opts FetchOptions) (FeedResult, error)
}

// FetchOptions 包含抓取的可选参数。
type FetchOptions struct {
	Limit       int
	Include     []string // 仅包含的 category 列表
	Format      string   // 输出格式：atom 或 rss
	ExtraParams map[string]string
}

// NormalizeFetchOptions 规范化抓取选项，保证缓存键、HTTP 路径和后台刷新语义一致。
func NormalizeFetchOptions(opts FetchOptions) FetchOptions {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	opts.Format = strings.ToLower(strings.TrimSpace(opts.Format))
	if opts.Format == "" {
		opts.Format = "atom"
	}
	if opts.Format != "rss" {
		opts.Format = "atom"
	}

	if len(opts.Include) > 0 {
		seen := make(map[string]bool, len(opts.Include))
		include := make([]string, 0, len(opts.Include))
		for _, item := range opts.Include {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			include = append(include, item)
		}
		sort.Strings(include)
		opts.Include = include
	}
	return opts
}

// Factory 根据已解析的路由配置创建 Route 实例。
type Factory func(config.ResolvedRouteConfig) Route

type registryEntry struct {
	description string
	factory     Factory
}

// registry 存储已注册的路由工厂。
var (
	registry   = map[string]registryEntry{}
	registryMu sync.RWMutex
)

// Register 注册一个路由工厂函数。name 为路由名，description 用于首页元数据。
func Register(name, description string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = registryEntry{description: description, factory: factory}
}

// GetRegistry 返回已注册路由工厂的快照。
func GetRegistry() map[string]Factory {
	registryMu.RLock()
	defer registryMu.RUnlock()

	snapshot := make(map[string]Factory, len(registry))
	for name, entry := range registry {
		snapshot[name] = entry.factory
	}
	return snapshot
}

// GetDescriptions 返回已注册路由描述的快照。
func GetDescriptions() map[string]string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	snapshot := make(map[string]string, len(registry))
	for name, entry := range registry {
		snapshot[name] = entry.description
	}
	return snapshot
}
