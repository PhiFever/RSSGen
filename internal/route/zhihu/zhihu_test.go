package zhihu

import (
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
