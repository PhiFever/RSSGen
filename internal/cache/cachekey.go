package cache

import (
	"strconv"
	"strings"
)

// BuildCacheKey 构建缓存键，格式为 "routeName/pathPart1/pathPart2"。
// 当 pathParts 为空时返回 "routeName"（无尾部斜杠）。
func BuildCacheKey(routeName string, pathParts []string) string {
	key := routeName
	for _, p := range pathParts {
		key += "/" + p
	}
	return key
}

// FeedVariant 表示会影响 feed XML 输出的请求变体。
type FeedVariant struct {
	Format  string
	Limit   int
	Include []string
}

// BuildFeedCacheKey 构建 feed XML 缓存键，包含规范化后的输出变体。
func BuildFeedCacheKey(routeName string, pathParts []string, variant FeedVariant) string {
	key := BuildCacheKey(routeName, pathParts)
	var parts []string
	if variant.Format != "" {
		parts = append(parts, "format="+variant.Format)
	}
	if variant.Limit > 0 {
		parts = append(parts, "limit="+strconv.Itoa(variant.Limit))
	}
	if len(variant.Include) > 0 {
		parts = append(parts, "include="+strings.Join(variant.Include, ","))
	}
	if len(parts) == 0 {
		return key
	}
	return key + "?" + strings.Join(parts, "&")
}
