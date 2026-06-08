// zhihu_demo 独立验证知乎 API 请求（签名 + TLS 指纹）。
//
// 模拟 Python 版本 ZhihuRoute._fetch_activities 的完整流程：
// 1. 从 cookie 提取 d_c0
// 2. 构造 API URL
// 3. 生成签名 (x-zse-93, x-zse-96)
// 4. 用 tls-client 发送请求（chrome_131 指纹）
// 5. 输出响应
//
// 用法:
//
//	go run cmd/zhihu_demo/main.go -cookie "d_c0=xxx; _zap=yyy" -user "your-user-id"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/PhiFever/RSSGen/internal/scraper"
	signzhihu "github.com/PhiFever/RSSGen/internal/sign/zhihu"
)

func main() {
	cookie := flag.String("cookie", "", "知乎 cookie 字符串（必须包含 d_c0）")
	userID := flag.String("user", "", "知乎用户 ID（URL 中的 user slug）")
	limit := flag.Int("limit", 5, "获取动态数量")
	flag.Parse()

	if *cookie == "" || *userID == "" {
		fmt.Fprintln(os.Stderr, "用法: zhihu_demo -cookie 'd_c0=xxx; ...' -user 'user-id'")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// 1. 从 cookie 提取 d_c0
	dC0, err := extractDC0(*cookie)
	if err != nil {
		fmt.Fprintf(os.Stderr, "提取 d_c0 失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 构造 API URL（与 Python 版本一致）
	apiURL := fmt.Sprintf(
		"https://www.zhihu.com/api/v3/moments/%s/activities?limit=%d&desktop=true",
		*userID, *limit,
	)
	referer := fmt.Sprintf("https://www.zhihu.com/people/%s", *userID)

	fmt.Println("=== 知乎 API 验证 ===")
	fmt.Printf("用户:   %s\n", *userID)
	fmt.Printf("API:    %s\n", apiURL)
	fmt.Printf("d_c0:   %s...\n", dC0[:min(len(dC0), 20)])
	fmt.Println()

	// 3. 生成签名
	sig, err := signzhihu.GetSignature(apiURL, dC0, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "签名生成失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("x-zse-93: %s\n", sig.XZSE93)
	fmt.Printf("x-zse-96: %s\n", sig.XZSE96)
	fmt.Println()

	// 4. 创建 scraper（chrome_131 TLS 指纹）
	cookies := parseCookieString(*cookie)
	sc := scraper.New(scraper.Config{
		Cookies:     cookies,
		RateLimit:   1.0,
		Impersonate: "chrome_131",
	})

	// 5. 发送请求
	headers := map[string]string{
		"accept":            "*/*",
		"x-requested-with":  "fetch",
		"x-zse-93":         sig.XZSE93,
		"x-zse-96":         sig.XZSE96,
	}

	fmt.Println("发送请求...")
	resp, err := sc.Get(apiURL, referer, headers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "请求失败: %v\n", err)
		os.Exit(1)
	}

	// 6. 输出结果
	fmt.Printf("状态码: %d\n", resp.StatusCode)
	fmt.Println()

	if resp.StatusCode != 200 {
		fmt.Printf("❌ 请求失败，响应体:\n%s\n", string(resp.Body))
		os.Exit(1)
	}

	// 解析 JSON 并格式化输出
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		fmt.Printf("JSON 解析失败: %v\n", err)
		fmt.Printf("原始响应:\n%s\n", string(resp.Body))
		os.Exit(1)
	}

	// 输出 paging 信息
	if paging, ok := result["paging"].(map[string]interface{}); ok {
		fmt.Printf("is_end: %v\n", paging["is_end"])
		fmt.Printf("totals: %v\n", paging["totals"])
	}

	// 输出动态列表
	data, ok := result["data"].([]interface{})
	if !ok {
		fmt.Println("❌ 响应中没有 data 字段")
		fmt.Printf("完整响应:\n")
		prettyPrint(result)
		os.Exit(1)
	}

	fmt.Printf("\n✅ 获取到 %d 条动态:\n", len(data))
	fmt.Println("---")
	for i, item := range data {
		act, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		verb, _ := act["verb"].(string)
		actionText, _ := act["action_text"].(string)

		target, _ := act["target"].(map[string]interface{})
		targetType, _ := target["type"].(string)
		title, _ := target["title"].(string)
		if title == "" {
			if excerpt, ok := target["excerpt"].(string); ok && len(excerpt) > 50 {
				title = excerpt[:50] + "..."
			} else {
				title, _ = target["excerpt"].(string)
			}
		}

		fmt.Printf("[%d] verb=%s type=%s\n", i+1, verb, targetType)
		if actionText != "" {
			fmt.Printf("    action: %s\n", actionText)
		}
		if title != "" {
			fmt.Printf("    title:  %s\n", title)
		}
	}

	fmt.Println("\n=== 验证完成 ===")
}

// extractDC0 从 cookie 字符串提取 d_c0 值。
func extractDC0(cookie string) (string, error) {
	re := regexp.MustCompile(`d_c0=([^;]+)`)
	matches := re.FindStringSubmatch(cookie)
	if len(matches) < 2 {
		return "", fmt.Errorf("cookie 中缺少 d_c0 字段")
	}
	return matches[1], nil
}

// parseCookieString 将 cookie 字符串解析为 map。
func parseCookieString(cookie string) map[string]string {
	result := make(map[string]string)
	re := regexp.MustCompile(`([^=]+)=([^;]+)`)
	for _, match := range re.FindAllStringSubmatch(cookie, -1) {
		if len(match) >= 3 {
			key := match[1]
			value := match[2]
			// 去除首尾空格
			for len(key) > 0 && key[0] == ' ' {
				key = key[1:]
			}
			for len(value) > 0 && value[len(value)-1] == ' ' {
				value = value[:len(value)-1]
			}
			result[key] = value
		}
	}
	return result
}

func prettyPrint(v interface{}) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}
