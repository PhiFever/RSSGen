// Package route 定义路由基类、数据结构和注册机制。
package route

import (
	"fmt"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
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
	// Description 返回路由描述。
	Description() string
	// FeedIDField 返回 feeds 配置中用于标识 feed 的字段名，默认 "user_id"。
	FeedIDField() string
	// FeedInfo 返回 feed 的元信息。
	FeedInfo(pathParams []string) (*FeedInfo, error)
	// Fetch 抓取数据源，返回 FeedItem 列表。
	Fetch(articleStore ArticleStore, pathParams []string, opts FetchOptions) ([]FeedItem, error)
}

// FetchOptions 包含抓取的可选参数。
type FetchOptions struct {
	Limit       int
	Include     []string // 仅包含的 category 列表
	ExtraParams map[string]string
}

// Factory 根据已解析的路由配置创建 Route 实例。
type Factory func(config.ResolvedRouteConfig) Route

// registry 存储已注册的路由工厂。
var (
	registry   = map[string]Factory{}
	registryMu sync.RWMutex
)

// Register 注册一个路由工厂函数。name 为路由名，factory 接收已解析配置返回 Route 实例。
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

// Unregister 移除一个路由工厂，主要用于测试隔离和动态注册清理。
func Unregister(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, name)
}

// GetRegistry 返回已注册路由工厂的快照。
func GetRegistry() map[string]Factory {
	registryMu.RLock()
	defer registryMu.RUnlock()

	snapshot := make(map[string]Factory, len(registry))
	for name, factory := range registry {
		snapshot[name] = factory
	}
	return snapshot
}
