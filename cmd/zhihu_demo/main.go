// zhihu_demo 独立验证知乎签名生成。
//
// 用法:
//
//	go run cmd/zhihu_demo/main.go
//	go run cmd/zhihu_demo/main.go -url "https://www.zhihu.com/api/v4/questions/123/answers?limit=5" -dc0 "your_dc0_value"
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/PhiFever/RSSGen/internal/sign/zhihu"
)

func main() {
	url := flag.String("url", "https://www.zhihu.com/api/v4/questions/123/answers?limit=5", "知乎 API URL")
	dc0 := flag.String("dc0", "test_dc0_value", "d_c0 cookie 值")
	body := flag.String("body", "", "POST body（可选）")
	flag.Parse()

	fmt.Println("=== 知乎签名验证 ===")
	fmt.Printf("URL:  %s\n", *url)
	fmt.Printf("d_c0: %s\n", *dc0)
	fmt.Println()

	// 生成签名
	result, err := zhihu.GetSignature(*url, *dc0, *body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "签名生成失败: %v\n", err)
		os.Exit(1)
	}

	// 输出结果
	fmt.Println("--- 签名结果 ---")
	fmt.Printf("x_zse_93: %s\n", result.XZSE93)
	fmt.Printf("x_zse_96: %s\n", result.XZSE96)
	fmt.Printf("source:   %s\n", result.Source)
	fmt.Println()

	// 验证格式
	fmt.Println("--- 格式验证 ---")
	if result.XZSE93 == zhihu.XZSE93Version {
		fmt.Printf("✅ x_zse_93 = %s（正确）\n", result.XZSE93)
	} else {
		fmt.Printf("❌ x_zse_93 = %s（期望 %s）\n", result.XZSE93, zhihu.XZSE93Version)
	}

	if len(result.XZSE96) > len(zhihu.XZSE96Prefix) && result.XZSE96[:len(zhihu.XZSE96Prefix)] == zhihu.XZSE96Prefix {
		fmt.Printf("✅ x_zse_96 以 %q 开头（正确）\n", zhihu.XZSE96Prefix)
	} else {
		fmt.Printf("❌ x_zse_96 格式异常: %s\n", result.XZSE96)
	}

	fmt.Println()
	fmt.Println("=== 验证完成 ===")
}
