// Package feedcatalog 记录本进程运行期间观察到的 feed 变体。
package feedcatalog

import (
	"container/list"
	"log/slog"
	"sync"

	"github.com/PhiFever/RSSGen/internal/pipeline"
)

// Catalog 在内存中记录最近观察到的 feed 变体。
//
// 条目按路由分桶，每个路由独立限量。上限只约束动态观察到的 feed；
// 配置中的静态 feed 由 refresher 另行合并。
type Catalog struct {
	mu         sync.Mutex
	limit      int
	routeState map[string]*routeState
}

type routeState struct {
	order   *list.List
	entries map[string]*list.Element
}

type catalogEntry struct {
	ref pipeline.FeedRef
}

// New 创建内存中的 feed 观察目录；limit 非正数时不记录。
func New(limit int) *Catalog {
	return &Catalog{
		limit:      limit,
		routeState: make(map[string]*routeState),
	}
}

// Observe 记录一个 feed 变体并更新它的 LRU 位置。
func (c *Catalog) Observe(ref pipeline.FeedRef) {
	if c == nil || c.limit <= 0 || ref.RouteName == "" || ref.CacheKey == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	rs := c.routeState[ref.RouteName]
	if rs == nil {
		rs = &routeState{
			order:   list.New(),
			entries: make(map[string]*list.Element),
		}
		c.routeState[ref.RouteName] = rs
	}

	if elem, ok := rs.entries[ref.CacheKey]; ok {
		elem.Value.(*catalogEntry).ref = cloneFeedRef(ref)
		rs.order.MoveToBack(elem)
		return
	}

	elem := rs.order.PushBack(&catalogEntry{ref: cloneFeedRef(ref)})
	rs.entries[ref.CacheKey] = elem

	if len(rs.entries) <= c.limit {
		return
	}

	oldest := rs.order.Front()
	if oldest == nil {
		return
	}
	oldEntry := oldest.Value.(*catalogEntry)
	delete(rs.entries, oldEntry.ref.CacheKey)
	rs.order.Remove(oldest)
	slog.Debug("动态 feed 超过上限，淘汰最久未访问项", "route", ref.RouteName, "key", oldEntry.ref.CacheKey, "limit", c.limit)
}

// List 返回某路由下已观察 feed 变体的快照，最久未访问的在前。
func (c *Catalog) List(routeName string) []pipeline.FeedRef {
	if c == nil || c.limit <= 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	rs := c.routeState[routeName]
	if rs == nil || len(rs.entries) == 0 {
		return nil
	}

	refs := make([]pipeline.FeedRef, 0, len(rs.entries))
	for elem := rs.order.Front(); elem != nil; elem = elem.Next() {
		refs = append(refs, cloneFeedRef(elem.Value.(*catalogEntry).ref))
	}
	return refs
}

func cloneFeedRef(ref pipeline.FeedRef) pipeline.FeedRef {
	ref.PathParts = append([]string(nil), ref.PathParts...)
	ref.Variant.Include = append([]string(nil), ref.Variant.Include...)
	return ref
}
