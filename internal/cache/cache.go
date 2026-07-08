// Package cache 提供内存 TTL 缓存。
package cache

import (
	"sync"
	"time"
)

type entry struct {
	value     string
	expiresAt time.Time
}

// TTLCache 是基于 sync.Map 的内存 TTL 缓存。
// 适用于 feed XML 缓存等低并发场景，过期条目在访问时惰性清理。
type TTLCache struct {
	m   sync.Map
	ttl time.Duration
}

// New 创建一个 TTL 缓存，ttl 为条目过期时间。
func New(ttl time.Duration) *TTLCache {
	return &TTLCache{ttl: ttl}
}

// Get 获取缓存值。未找到或已过期返回 ("", false)。
func (c *TTLCache) Get(key string) (string, bool) {
	v, ok := c.m.Load(key)
	if !ok {
		return "", false
	}
	e := v.(*entry)
	if time.Now().After(e.expiresAt) {
		c.m.Delete(key)
		return "", false
	}
	return e.value, true
}

// Set 写入缓存值，覆盖已有条目。
func (c *TTLCache) Set(key, value string) {
	c.m.Store(key, &entry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	})
}
