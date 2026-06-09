package zhihu

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"短于上限原样返回", "想法", 50, "想法"},
		{"恰好等于上限", "abcde", 5, "abcde"},
		{"中文按 rune 截断", "知乎用户的最新动态内容很长很长很长", 5, "知乎用户的"},
		{"ASCII 截断", "hello world", 5, "hello"},
		{"空串", "", 50, ""},
		{"零长度", "中文", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.in, tt.n)
			if got != tt.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
			// 关键回归点：截断结果必须是合法 UTF-8（旧的字节截断会切断汉字）
			if !utf8.ValidString(got) {
				t.Errorf("truncateRunes(%q, %d) 产生非法 UTF-8: %q", tt.in, tt.n, got)
			}
		})
	}
}

// TestTruncateRunesBytewiseWouldCorrupt 固化 bug #2：旧的 s[:n] 字节截断会破坏多字节字符，
// rune 截断必须避免这一点。
func TestTruncateRunesBytewiseWouldCorrupt(t *testing.T) {
	s := "中文标题" // 每个汉字 3 字节
	if got := truncateRunes(s, 2); !utf8.ValidString(got) || got != "中文" {
		t.Errorf("truncateRunes 应得合法的 %q, 实得 %q", "中文", got)
	}
}

// TestFixLazyImages 固化迁移回归：知乎正文里的 SVG 懒加载占位符必须替换为
// data-actualsrc / data-original 指向的真实图片链接，而不是被清空。
func TestFixLazyImages(t *testing.T) {
	placeholder := `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='100' height='50'></svg>`
	tests := []struct {
		name           string
		in             string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:           "用 data-actualsrc 替换占位符并去掉 lazy",
			in:             `<img src="` + placeholder + `" data-actualsrc="https://pic.zhimg.com/test_b.jpg" class="lazy">`,
			wantContains:   []string{"https://pic.zhimg.com/test_b.jpg"},
			wantNotContain: []string{"data:image/svg+xml", "lazy"},
		},
		{
			name:           "无 actualsrc 时回退 data-original",
			in:             `<img src="` + placeholder + `" data-original="https://pic.zhimg.com/test_r.jpg">`,
			wantContains:   []string{"https://pic.zhimg.com/test_r.jpg"},
			wantNotContain: []string{"data:image/svg+xml"},
		},
		{
			name:           "data-actualsrc 优先于 data-original",
			in:             `<img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/actual.jpg" data-original="https://pic.zhimg.com/original.jpg">`,
			wantContains:   []string{"actual.jpg"},
			wantNotContain: []string{"original.jpg"},
		},
		{
			name:           "移除 noscript 标签",
			in:             `<figure><noscript><img src="https://pic.zhimg.com/real.jpg"></noscript><img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/real.jpg"></figure>`,
			wantContains:   []string{"https://pic.zhimg.com/real.jpg"},
			wantNotContain: []string{"<noscript>"},
		},
		{
			name:         "普通图片原样保留",
			in:           `<img src="https://pic.zhimg.com/normal.jpg">`,
			wantContains: []string{"https://pic.zhimg.com/normal.jpg"},
		},
		{
			name: "空串返回空串",
			in:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fixLazyImages(tt.in)
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("fixLazyImages(%q) = %q, 应包含 %q", tt.in, got, sub)
				}
			}
			for _, sub := range tt.wantNotContain {
				if strings.Contains(got, sub) {
					t.Errorf("fixLazyImages(%q) = %q, 不应包含 %q", tt.in, got, sub)
				}
			}
		})
	}
}
