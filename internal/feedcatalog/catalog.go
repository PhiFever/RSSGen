// Package feedcatalog tracks feed variants observed during this process.
package feedcatalog

import (
	"container/list"
	"log/slog"
	"sync"

	"github.com/PhiFever/RSSGen/internal/pipeline"
)

// Catalog records recently observed feed variants in memory.
//
// Entries are bucketed by route and capped independently per route. The limit
// applies only to dynamically observed feeds; statically configured feeds are
// merged by the refresher separately.
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

// New creates an in-memory observed feed catalog. A non-positive limit disables
// observation.
func New(limit int) *Catalog {
	return &Catalog{
		limit:      limit,
		routeState: make(map[string]*routeState),
	}
}

// Observe records a feed variant and updates its LRU position.
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

// List returns a snapshot of observed feed variants for a route, oldest first.
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
