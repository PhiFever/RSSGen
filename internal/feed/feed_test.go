// Package feed 测试 —— 迁移自 Python tests/test_feed.py + tests/test_feed_item.py
package feed

import (
	"strings"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/route"
)

// --- 辅助函数 ---

func ptrTime(t time.Time) *time.Time { return &t }

// --- Generate 基础 ---

func TestGenerateEmptyItems(t *testing.T) {
	info := &route.FeedInfo{Title: "测试", Link: "https://example.com", Description: "desc"}
	out, err := Generate(info, nil, "")
	if err != nil {
		t.Fatalf("Generate 返回错误: %v", err)
	}
	if !strings.Contains(out, "<feed") {
		t.Error("空条目应仍生成有效 Atom feed")
	}
}

func TestGenerateAtomBasic(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &route.FeedInfo{Title: "标题", Link: "https://example.com", Description: "desc"}
	items := []route.FeedItem{
		{Title: "文章1", Link: "https://example.com/1", Content: "<p>内容</p>", PubDate: &now, Author: "作者"},
	}
	out, err := Generate(info, items, "")
	if err != nil {
		t.Fatalf("Generate 返回错误: %v", err)
	}
	for _, want := range []string{"<feed", "标题", "文章1", "作者", "https://example.com/1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Atom 输出应包含 %q", want)
		}
	}
}

func TestGenerateRSSFormat(t *testing.T) {
	info := &route.FeedInfo{Title: "RSS标题", Link: "https://example.com", Description: "desc"}
	items := []route.FeedItem{
		{Title: "条目", Link: "https://example.com/1", Content: "正文"},
	}
	out, err := Generate(info, items, "rss")
	if err != nil {
		t.Fatalf("Generate(rss) 返回错误: %v", err)
	}
	for _, want := range []string{`<rss`, `version="2.0"`, "RSS标题", "条目"} {
		if !strings.Contains(out, want) {
			t.Errorf("RSS 输出应包含 %q", want)
		}
	}
}

// --- Fallback 行为（对应 Python TestFeedFallbacks） ---

func TestGenerateFallbackTitle(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "", Link: "https://x.com/1"}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "无标题") {
		t.Error("空 title 应回退为 '无标题'")
	}
}

func TestGenerateFallbackNoneTitle(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Link: "https://x.com/1"}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "无标题") {
		t.Error("缺失 title 应回退为 '无标题'")
	}
}

func TestGenerateFallbackGUID(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Link: "https://x.com/1"}}
	out, _ := Generate(info, items, "")
	// GUID 应 fallback 到 Link
	if !strings.Contains(out, "https://x.com/1") {
		t.Error("无 GUID 时应 fallback 到 Link")
	}
}

func TestGenerateExplicitGUID(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", GUID: "custom-guid", Link: "https://x.com/1"}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "custom-guid") {
		t.Error("显式 GUID 应保留")
	}
}

func TestGenerateFallbackPubDate(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Link: "https://x.com/1"}}
	// PubDate 为 nil 时，Atom 应用 now，RSS 不输出 pubDate
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "<updated") {
		t.Error("Atom 条目缺失 PubDate 时应填充 <updated>")
	}
}

func TestGeneratePubDatePreserved(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Link: "https://x.com/1", PubDate: &ts}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "2025-06-01") {
		t.Error("显式 PubDate 应保留在输出中")
	}
}

// --- Categories（对应 Python TestFeedCategories） ---

func TestGenerateCategory(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Categories: []string{"tech"}}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "tech") {
		t.Error("Atom 输出应包含 category term='tech'")
	}
}

func TestGenerateMultipleCategories(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Categories: []string{"a", "b", "c"}}}
	out, _ := Generate(info, items, "")
	for _, cat := range []string{"a", "b", "c"} {
		if !strings.Contains(out, cat) {
			t.Errorf("输出应包含 category %q", cat)
		}
	}
}

func TestGenerateEmptyCategories(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T"}}
	out, _ := Generate(info, items, "")
	// 不应有 category 标签
	if strings.Contains(out, "<category") {
		t.Error("空 Categories 不应生成 category 标签")
	}
}

// --- Enclosure / Author ---

func TestGenerateEnclosure(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{
		Title:      "T",
		Enclosures: []route.Enclosure{{URL: "https://x.com/img.jpg", Type: "image/jpeg"}},
	}}
	// Atom 暂不渲染 enclosure（Go 实现无此字段），RSS 也不渲染——验证不报错即可
	_, err := Generate(info, items, "rss")
	if err != nil {
		t.Fatalf("含 enclosure 不应报错: %v", err)
	}
}

func TestGenerateAuthor(t *testing.T) {
	info := &route.FeedInfo{Title: "F", Link: "https://x.com"}
	items := []route.FeedItem{{Title: "T", Author: "张三"}}
	out, _ := Generate(info, items, "")
	if !strings.Contains(out, "张三") {
		t.Error("Atom 输出应包含作者名")
	}
}

// --- sanitizeHTML（对应 Python feed 安全处理） ---

func TestSanitizeHTMLScript(t *testing.T) {
	in := `<p>安全</p><script>alert('xss')</script>`
	got := sanitizeHTML(in)
	if strings.Contains(got, "<script") {
		t.Error("sanitizeHTML 应转义 <script")
	}
	if !strings.Contains(got, "安全") {
		t.Error("sanitizeHTML 不应破坏安全内容")
	}
}

func TestSanitizeHTMLIframe(t *testing.T) {
	in := `<p>内容</p><iframe src="evil.com"></iframe>`
	got := sanitizeHTML(in)
	if strings.Contains(got, "<iframe") {
		t.Error("sanitizeHTML 应转义 <iframe")
	}
}

// --- orDefault ---

func TestOrDefault(t *testing.T) {
	if got := orDefault("", "fallback"); got != "fallback" {
		t.Errorf("orDefault('', 'fallback') = %q, want 'fallback'", got)
	}
	if got := orDefault("first", "second"); got != "first" {
		t.Errorf("orDefault('first', 'second') = %q, want 'first'", got)
	}
	if got := orDefault("", ""); got != "" {
		t.Errorf("orDefault('', '') = %q, want ''", got)
	}
}
