// Package route 定义路由基类、数据结构和注册机制。
package route

import (
	"fmt"
	"time"
)

// FeedItem 表示一个 feed 条目。
type FeedItem struct {
	Title      string            // 条目标题
	Link       string            // 条目链接
	Content    string            // HTML 正文
	PubDate    *time.Time        // 发布时间
	Author     string            // 作者
	GUID       string            // 唯一标识，默认用 Link
	Enclosures []Enclosure       // 附件（图片等）
	Categories []string          // 分类标签
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
	Limit        int
	Include      []string // 仅包含的 category 列表
	ExtraParams  map[string]string
}

// registry 存储已注册的路由工厂。
var registry = map[string]func(map[string]interface{}) Route{}

// Register 注册一个路由工厂函数。name 为路由名，factory 接收配置 map 返回 Route 实例。
func Register(name string, factory func(map[string]interface{}) Route) {
	registry[name] = factory
}

// GetRegistry 返回已注册的路由工厂 map。
func GetRegistry() map[string]func(map[string]interface{}) Route {
	return registry
}

// CreateRoute 根据路由名和配置创建 Route 实例。
func CreateRoute(name string, config map[string]interface{}) (Route, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("路由不存在: %s", name)
	}
	return factory(config), nil
}
