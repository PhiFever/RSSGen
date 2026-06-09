package cache

// BuildCacheKey 构建缓存键，格式为 "routeName/pathPart1/pathPart2"。
// 当 pathParts 为空时返回 "routeName"（无尾部斜杠）。
func BuildCacheKey(routeName string, pathParts []string) string {
	key := routeName
	for _, p := range pathParts {
		key += "/" + p
	}
	return key
}
