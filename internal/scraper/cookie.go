package scraper

import "strings"

// ParseCookieString 解析 "key1=val1; key2=val2" 格式的 cookie 字符串为 map。
func ParseCookieString(s string) map[string]string {
	cookies := make(map[string]string)
	s = strings.TrimSpace(s)
	if s == "" {
		return cookies
	}
	pairs := strings.Split(s, ";")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			cookies[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return cookies
}
