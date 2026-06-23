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
	"flag"
	"fmt"
	"io"
	"net/http"
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
	cookies := scraper.ParseCookieString(*cookie)
	sc, err := scraper.New(scraper.Config{
		Cookies:     cookies,
		RateLimit:   1.0,
		Impersonate: "chrome_131",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 HTTP 客户端失败: %v\n", err)
		os.Exit(1)
	}

	// 5. 发送请求（tls-client）
	headers := map[string]string{
		"accept":           "*/*",
		"x-requested-with": "fetch",
		"x-zse-93":         sig.XZSE93,
		"x-zse-96":         sig.XZSE96,
	}

	fmt.Println("--- tls-client 请求 ---")
	resp, err := sc.Get(apiURL, referer, headers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls-client 请求失败: %v\n", err)
	} else {
		fmt.Printf("状态码: %d\n", resp.StatusCode)
		if resp.StatusCode != 200 {
			fmt.Printf("响应: %s\n", string(resp.Body[:min(len(resp.Body), 500)]))
		}
	}

	// 6. 对比：用标准 net/http 发送相同请求
	fmt.Println("\n--- 标准 net/http 请求 ---")
	sig2, _ := signzhihu.GetSignature(apiURL, dC0, "")
	stdHeaders := map[string]string{
		"accept":           "*/*",
		"x-requested-with": "fetch",
		"x-zse-93":         sig2.XZSE93,
		"x-zse-96":         sig2.XZSE96,
		"referer":          referer,
		"cookie":           *cookie,
	}

	stdReq, _ := http.NewRequest("GET", apiURL, nil)
	for k, v := range stdHeaders {
		stdReq.Header.Set(k, v)
	}
	stdResp, err := http.DefaultClient.Do(stdReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "标准请求失败: %v\n", err)
	} else {
		bodyBytes, readErr := io.ReadAll(stdResp.Body)
		if closeErr := stdResp.Body.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "关闭标准请求响应失败: %v\n", closeErr)
		}
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "读取标准请求响应失败: %v\n", readErr)
			return
		}
		fmt.Printf("状态码: %d\n", stdResp.StatusCode)
		if stdResp.StatusCode != 200 {
			fmt.Printf("响应: %s\n", string(bodyBytes[:min(len(bodyBytes), 500)]))
		} else {
			fmt.Println("✅ 标准请求成功")
		}
	}
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
