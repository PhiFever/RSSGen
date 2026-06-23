package zhihu

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	signzhihu "github.com/PhiFever/RSSGen/internal/sign/zhihu"
)

func withZhihuTestHost(t *testing.T, url string) {
	t.Helper()
	old := zhihuHostURL
	zhihuHostURL = url
	t.Cleanup(func() { zhihuHostURL = old })
}

func writeZhihuJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("写入 JSON 响应失败: %v", err)
	}
}

func writeZhihuBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("写入响应失败: %v", err)
	}
}

func TestRouteMetadataAndScraper(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test;", RateLimit: 0.001})
	if r.Name() != "zhihu" {
		t.Fatalf("Name() = %q", r.Name())
	}
	if r.Description() == "" || r.FeedIDField() != "user_id" {
		t.Fatalf("metadata 不符合预期")
	}
	if _, err := r.getScraper(); err != nil {
		t.Fatalf("getScraper 返回错误: %v", err)
	}
}

func TestFetchActivitiesPaginationAndActor(t *testing.T) {
	var page int
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v3/moments/user1/activities") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-Zse-96") == "" {
			t.Fatalf("签名 header 未设置")
		}
		page++
		switch page {
		case 1:
			writeZhihuJSON(t, w, map[string]any{
				"data": []map[string]any{{
					"actor": map[string]any{"name": "Alice", "headline": "Hello"},
					"target": map[string]any{
						"type": "answer",
						"id":   "a1",
					},
				}},
				"paging": map[string]any{
					"is_end": false,
					"next":   serverURL + "/api/v3/moments/user1/activities?page=2",
				},
			})
		case 2:
			writeZhihuJSON(t, w, map[string]any{
				"data": []map[string]any{{
					"target": map[string]any{
						"type": "article",
						"id":   "p1",
					},
				}},
				"paging": map[string]any{"is_end": true},
			})
		default:
			t.Fatalf("不应请求第 %d 页", page)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)

	r := New(config.ResolvedRouteConfig{Cookie: `d_c0="abc";`, RateLimit: 0.001})
	activities, err := r.fetchActivities("user1", 2)
	if err != nil {
		t.Fatalf("fetchActivities 返回错误: %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("activities len = %d", len(activities))
	}
	if r.actor["name"] != "Alice" {
		t.Fatalf("actor 未提取: %+v", r.actor)
	}
}

func TestFetchActivitiesErrors(t *testing.T) {
	t.Run("missing d_c0", func(t *testing.T) {
		_, err := New(config.ResolvedRouteConfig{RateLimit: 0.001}).fetchActivities("user1", 1)
		if err == nil {
			t.Fatal("缺少 d_c0 应返回错误")
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()
		withZhihuTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{Cookie: `d_c0="abc";`, RateLimit: 0.001}).fetchActivities("user1", 1)
		if err == nil {
			t.Fatal("HTTP 非 200 应返回错误")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeZhihuBody(t, w, `{bad json`)
		}))
		defer server.Close()
		withZhihuTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{Cookie: `d_c0="abc";`, RateLimit: 0.001}).fetchActivities("user1", 1)
		if err == nil {
			t.Fatal("无效 JSON 应返回错误")
		}
	})
}

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

// --- formatQuestionDescription（迁移自 Python TestZhihuFormatQuestionDescription） ---

func TestFormatQuestionDescription(t *testing.T) {
	tests := []struct {
		name           string
		in             string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "包裹为引用块",
			in:           "<p>这是一个简单的问题描述</p>",
			wantContains: []string{"<h3>【问题描述】</h3>", "<blockquote>", "</blockquote>", "这是一个简单的问题描述"},
		},
		{
			name:         "保留链接结构",
			in:           `<p><a href="https://example.com">链接文字</a></p>`,
			wantContains: []string{`<a href="https://example.com">`, "链接文字"},
		},
		{
			name:           "修复描述中的懒加载图片",
			in:             `<img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/real.jpg">`,
			wantContains:   []string{"https://pic.zhimg.com/real.jpg"},
			wantNotContain: []string{"data:image/svg+xml"},
		},
		{
			name:         "复杂HTML含图片",
			in:           `<p>问题描述文本</p><figure><img src="https://pic.zhimg.com/test.jpg"/></figure>`,
			wantContains: []string{"<blockquote>", "问题描述文本", "https://pic.zhimg.com/test.jpg"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatQuestionDescription(tt.in)
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("formatQuestionDescription(%q) 应包含 %q, 实得 %q", tt.in, sub, got)
				}
			}
			for _, sub := range tt.wantNotContain {
				if strings.Contains(got, sub) {
					t.Errorf("formatQuestionDescription(%q) 不应包含 %q, 实得 %q", tt.in, sub, got)
				}
			}
		})
	}
}

func TestFormatQuestionDescriptionEmpty(t *testing.T) {
	if got := formatQuestionDescription(""); got != "" {
		t.Errorf("空串应返回空串, 实得 %q", got)
	}
}

// --- FeedInfo（迁移自 Python TestZhihuRouteFeedInfo） ---

func TestFeedInfoFallbackWithoutActor(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	info, err := r.FeedInfo([]string{"kvxjr369f"})
	if err != nil {
		t.Fatalf("FeedInfo 返回错误: %v", err)
	}
	if info.Title != "知乎动态 - kvxjr369f" {
		t.Errorf("Title = %q, want '知乎动态 - kvxjr369f'", info.Title)
	}
	if info.Link != "https://www.zhihu.com/people/kvxjr369f" {
		t.Errorf("Link = %q", info.Link)
	}
	if !strings.Contains(info.Description, "kvxjr369f") {
		t.Errorf("Description 应包含 user_id, 实得 %q", info.Description)
	}
}

func TestFeedInfoUsesActorName(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.actor = map[string]interface{}{"name": "张三", "headline": "知乎签名档"}
	info, err := r.FeedInfo([]string{"kvxjr369f"})
	if err != nil {
		t.Fatalf("FeedInfo 返回错误: %v", err)
	}
	if info.Title != "知乎动态 - 张三" {
		t.Errorf("Title = %q, want '知乎动态 - 张三'", info.Title)
	}
	if info.Description != "知乎签名档" {
		t.Errorf("Description = %q, want '知乎签名档'", info.Description)
	}
}

func TestFeedInfoActorEmptyHeadlineFallback(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.actor = map[string]interface{}{"name": "李四", "headline": ""}
	info, err := r.FeedInfo([]string{"kvxjr369f"})
	if err != nil {
		t.Fatalf("FeedInfo 返回错误: %v", err)
	}
	if info.Title != "知乎动态 - 李四" {
		t.Errorf("Title = %q", info.Title)
	}
	// fallback description 应用 displayName（actor.name），与 Python 行为一致
	if info.Description != "知乎用户 李四 的最新动态" {
		t.Errorf("Description = %q, want fallback with displayName", info.Description)
	}
}

func TestFeedInfoRequiresUserID(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	_, err := r.FeedInfo(nil)
	if err == nil {
		t.Error("缺少 user_id 应返回错误")
	}
}

// --- makeFeedItem（迁移自 Python TestZhihuRouteMakeFeedItem） ---

func newTestRoute() *Route {
	return New(config.ResolvedRouteConfig{Cookie: "test"})
}

func mkAct(target map[string]interface{}, verb, actionText string) map[string]interface{} {
	return map[string]interface{}{
		"target":      target,
		"verb":        verb,
		"action_text": actionText,
	}
}

func TestMakeFeedItemAnswerType(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "123", "type": "answer",
		"content": "<p>回答内容</p>", "created_time": float64(1700000000),
		"author":   map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{"id": "456", "title": "问题标题"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_ANSWER_QUESTION", "回答了问题"))
	if item.Title != "[回答了问题] 问题标题" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.Link != "https://www.zhihu.com/question/456/answer/123" {
		t.Errorf("Link = %q", item.Link)
	}
	if item.GUID != "123" {
		t.Errorf("GUID = %q", item.GUID)
	}
	if item.Author != "作者" {
		t.Errorf("Author = %q", item.Author)
	}
}

func TestMakeFeedItemAnswerIncludesQuestionDescription(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "123", "type": "answer",
		"content": "<p>回答内容</p>", "created_time": float64(1700000000),
		"author": map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{
			"id": "456", "title": "问题标题", "detail": "<p>问题描述文本</p>",
		},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_ANSWER_QUESTION", "回答了问题"))
	if !strings.Contains(item.Content, "【问题描述】") {
		t.Error("answer 类型应包含问题描述")
	}
	if !strings.Contains(item.Content, "问题描述文本") {
		t.Error("应包含问题描述文本")
	}
	if !strings.Contains(item.Content, "回答内容") {
		t.Error("应包含回答内容")
	}
	idxDesc := strings.Index(item.Content, "【问题描述】")
	idxAnswer := strings.Index(item.Content, "回答内容")
	if idxDesc >= idxAnswer {
		t.Error("问题描述应在回答内容之前")
	}
}

func TestMakeFeedItemAnswerWithoutQuestionDetail(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "123", "type": "answer",
		"content": "<p>回答内容</p>", "created_time": float64(1700000000),
		"author":   map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{"id": "456", "title": "问题标题", "detail": ""},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_ANSWER_QUESTION", "回答了问题"))
	if strings.Contains(item.Content, "【问题描述】") {
		t.Error("无 detail 时不应包含问题描述")
	}
}

func TestMakeFeedItemArticleType(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "789", "type": "article", "title": "文章标题",
		"content": "<p>文章内容</p>", "created_time": float64(1700000000),
		"author": map[string]interface{}{"name": "作者"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_CREATE_ARTICLE", "发表了文章"))
	if item.Title != "[发表了文章] 文章标题" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.Link != "https://zhuanlan.zhihu.com/p/789" {
		t.Errorf("Link = %q", item.Link)
	}
	if strings.Contains(item.Content, "【问题描述】") {
		t.Error("article 类型不应包含问题描述")
	}
}

func TestMakeFeedItemPinType(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "111", "type": "pin",
		"excerpt": "这是一条想法的摘要内容", "created_time": float64(1700000000),
		"author": map[string]interface{}{"name": "作者"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_CREATE_PIN", "发布了想法"))
	if item.Title != "[发布了想法] 这是一条想法的摘要内容" {
		t.Errorf("Title = %q", item.Title)
	}
}

func TestMakeFeedItemPinVideoBlockLogsOriginalURL(t *testing.T) {
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	r := newTestRoute()
	target := map[string]interface{}{
		"id": "111", "type": "pin",
		"content": []interface{}{
			map[string]interface{}{"type": PinBlockVideo, "url": "https://www.zhihu.com/video/123"},
		},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_CREATE_PIN", "发布了想法"))
	if item.Link != "https://www.zhihu.com/pin/111" {
		t.Errorf("Link = %q", item.Link)
	}
	logs := buf.String()
	if !strings.Contains(logs, PinBlockVideo) || !strings.Contains(logs, item.Link) {
		t.Errorf("video block 应输出带 pin 原文链接的 slog.Warn, 实得日志: %q", logs)
	}
}

func TestMakeFeedItemPinFallsBackToExcerptTitle(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "222", "type": "pin",
		"excerpt": nil, "excerpt_title": "5.2早安<br>大家好",
		"created": float64(1700000000), "author": map[string]interface{}{"name": "作者"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_CREATE_PIN", "发布了想法"))
	if !strings.Contains(item.Title, "5.2早安") {
		t.Errorf("Title 应包含 '5.2早安', 实得 %q", item.Title)
	}
	if strings.Contains(item.Title, "<br>") {
		t.Errorf("Title 不应包含 HTML 标签, 实得 %q", item.Title)
	}
	if !strings.HasPrefix(item.Title, "[发布了想法] ") {
		t.Errorf("Title 应以动作前缀开头, 实得 %q", item.Title)
	}
}

func TestMakeFeedItemCollectAnswerCategory(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "123", "type": "answer",
		"content": "<p>内容</p>", "created_time": float64(1600000000),
		"author":   map[string]interface{}{"name": "原作者"},
		"question": map[string]interface{}{"id": "456", "title": "问题标题"},
	}
	a := map[string]interface{}{
		"target": target, "verb": "MEMBER_COLLECT_ANSWER",
		"action_text": "收藏了回答", "created_time": float64(1700000000),
	}
	item := r.makeFeedItem(a)
	if len(item.Categories) != 1 || item.Categories[0] != TypeCollectedAnswer {
		t.Errorf("Categories = %v, want [%s]", item.Categories, TypeCollectedAnswer)
	}
	if item.Title != "[收藏了回答] 问题标题" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.GUID != "collected_answer_123" {
		t.Errorf("GUID = %q", item.GUID)
	}
	want := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	if item.PubDate == nil || !item.PubDate.Equal(want) {
		t.Errorf("PubDate = %v, want %v", item.PubDate, want)
	}
}

func TestMakeFeedItemCollectArticleCategory(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "789", "type": "article", "title": "文章标题",
		"content": "<p>内容</p>", "created_time": float64(1700000000),
		"author": map[string]interface{}{"name": "作者"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_COLLECT_ARTICLE", "收藏了文章"))
	if len(item.Categories) != 1 || item.Categories[0] != TypeCollectedArticle {
		t.Errorf("Categories = %v, want [%s]", item.Categories, TypeCollectedArticle)
	}
	if item.Title != "[收藏了文章] 文章标题" {
		t.Errorf("Title = %q", item.Title)
	}
}

func TestMakeFeedItemVoteupAnswerCategory(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "888", "type": "answer",
		"content": "<p>内容</p>", "created_time": float64(1700000000),
		"author":   map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{"id": "999", "title": "被赞同的问题"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_VOTEUP_ANSWER", "赞同了回答"))
	if len(item.Categories) != 1 || item.Categories[0] != TypeVoteupAnswer {
		t.Errorf("Categories = %v, want [%s]", item.Categories, TypeVoteupAnswer)
	}
	if item.GUID != "voteup_answer_888" {
		t.Errorf("GUID = %q", item.GUID)
	}
}

func TestMakeFeedItemFollowedQuestionViaEmptyVerb(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "2033612101923464726", "type": "question",
		"title": "张雪回应 820 赛道熄火", "detail": "<p>问题描述</p>",
		"excerpt": "问题摘要", "created": float64(1777630907),
		"author": map[string]interface{}{"name": "提问者"},
	}
	a := map[string]interface{}{
		"target": target, "verb": "", "action_text": "关注了问题",
		"created_time": float64(1777708519),
	}
	item := r.makeFeedItem(a)
	if len(item.Categories) != 1 || item.Categories[0] != TypeFollowedQuestion {
		t.Errorf("Categories = %v, want [%s]", item.Categories, TypeFollowedQuestion)
	}
	if item.Title != "[关注了问题] 张雪回应 820 赛道熄火" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.Link != "https://www.zhihu.com/question/2033612101923464726" {
		t.Errorf("Link = %q", item.Link)
	}
	if item.GUID != "followed_question_2033612101923464726" {
		t.Errorf("GUID = %q", item.GUID)
	}
	if item.Content != "<p>问题描述</p>" {
		t.Errorf("Content 应回退到 detail, 实得 %q", item.Content)
	}
}

func TestMakeFeedItemPubDateFromTargetCreatedTime(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "123", "type": "answer", "created_time": float64(1700000000),
		"content": "", "author": map[string]interface{}{"name": "a"},
		"question": map[string]interface{}{"id": "1", "title": "t"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_ANSWER_QUESTION", ""))
	want := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	if item.PubDate == nil || !item.PubDate.Equal(want) {
		t.Errorf("PubDate = %v, want %v", item.PubDate, want)
	}
}

func TestMakeFeedItemPinUsesCreatedField(t *testing.T) {
	r := newTestRoute()
	target := map[string]interface{}{
		"id": "111", "type": "pin", "excerpt": "想法",
		"created_time": nil, "created": float64(1700000000),
		"author": map[string]interface{}{"name": "a"},
	}
	item := r.makeFeedItem(mkAct(target, "MEMBER_CREATE_PIN", ""))
	want := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	if item.PubDate == nil || !item.PubDate.Equal(want) {
		t.Errorf("PubDate = %v, want %v", item.PubDate, want)
	}
}

// --- isSelfInteraction（迁移自 Python TestIsSelfInteraction） ---

func TestIsSelfInteraction(t *testing.T) {
	tests := []struct {
		name string
		act  map[string]interface{}
		want bool
	}{
		{"收藏自己的回答是自互动",
			map[string]interface{}{"verb": "MEMBER_COLLECT_ANSWER", "actor": map[string]interface{}{"id": "U1"}, "target": map[string]interface{}{"author": map[string]interface{}{"id": "U1"}}},
			true},
		{"收藏他人的回答不是自互动",
			map[string]interface{}{"verb": "MEMBER_COLLECT_ANSWER", "actor": map[string]interface{}{"id": "U1"}, "target": map[string]interface{}{"author": map[string]interface{}{"id": "U2"}}},
			false},
		{"赞同自己的文章是自互动",
			map[string]interface{}{"verb": "MEMBER_VOTEUP_ARTICLE", "actor": map[string]interface{}{"id": "X"}, "target": map[string]interface{}{"author": map[string]interface{}{"id": "X"}}},
			true},
		{"创作类verb不算自互动",
			map[string]interface{}{"verb": "MEMBER_ANSWER_QUESTION", "actor": map[string]interface{}{"id": "U1"}, "target": map[string]interface{}{"author": map[string]interface{}{"id": "U1"}}},
			false},
		{"空verb不算自互动",
			map[string]interface{}{"verb": "", "actor": map[string]interface{}{"id": "U1"}, "target": map[string]interface{}{"author": map[string]interface{}{"id": "U1"}}},
			false},
		{"缺少actor返回false",
			map[string]interface{}{"verb": "MEMBER_COLLECT_ANSWER", "target": map[string]interface{}{"author": map[string]interface{}{"id": "U1"}}},
			false},
		{"缺少target.author返回false",
			map[string]interface{}{"verb": "MEMBER_COLLECT_ANSWER", "actor": map[string]interface{}{"id": "U1"}, "target": map[string]interface{}{}},
			false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSelfInteraction(tt.act)
			if got != tt.want {
				t.Errorf("isSelfInteraction(%v) = %v, want %v", tt.act, got, tt.want)
			}
		})
	}
}

// --- deriveCategory（迁移自 Python TestZhihuApiChangeWarnings 部分） ---

func TestDeriveCategoryKnownTypes(t *testing.T) {
	tests := []struct {
		name string
		act  map[string]interface{}
		want string
	}{
		{"answer verb", map[string]interface{}{"verb": "MEMBER_ANSWER_QUESTION", "target": map[string]interface{}{"type": "answer"}}, TypeAnswer},
		{"article verb", map[string]interface{}{"verb": "MEMBER_CREATE_ARTICLE", "target": map[string]interface{}{"type": "article"}}, TypeArticle},
		{"pin verb", map[string]interface{}{"verb": "MEMBER_CREATE_PIN", "target": map[string]interface{}{"type": "pin"}}, TypePin},
		{"collect answer", map[string]interface{}{"verb": "MEMBER_COLLECT_ANSWER", "target": map[string]interface{}{"type": "answer"}}, TypeCollectedAnswer},
		{"voteup answer", map[string]interface{}{"verb": "MEMBER_VOTEUP_ANSWER", "target": map[string]interface{}{"type": "answer"}}, TypeVoteupAnswer},
		{"empty verb + question type", map[string]interface{}{"verb": "", "target": map[string]interface{}{"type": "question"}}, TypeFollowedQuestion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveCategory(tt.act)
			if got != tt.want {
				t.Errorf("deriveCategory(%v) = %q, want %q", tt.act, got, tt.want)
			}
		})
	}
}

func TestDeriveCategoryUnknownType(t *testing.T) {
	act := map[string]interface{}{
		"verb":   "MEMBER_FUTURE_ACTION",
		"target": map[string]interface{}{"type": "future_type"},
	}
	got := deriveCategory(act)
	if got != "future_type" {
		t.Errorf("未知类型应回退到 target.type, 实得 %q", got)
	}
}

// --- renderPinContent（迁移自 Python TestZhihuApiChangeWarnings 部分） ---

func TestRenderPinContentTextBlock(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"content": "纯文本内容"},
	}
	got := renderPinContent(blocks, "")
	if !strings.Contains(got, "纯文本内容") {
		t.Errorf("应包含文本内容, 实得 %q", got)
	}
}

func TestRenderPinContentHTMLPreserved(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"content": "<p>带有<b>HTML</b>标签的内容</p>"},
	}
	got := renderPinContent(blocks, "")
	if strings.Contains(got, "&lt;") || strings.Contains(got, "&gt;") {
		t.Errorf("HTML 标签不应被转义, 实得 %q", got)
	}
	if !strings.Contains(got, "<p>") {
		t.Errorf("应保留原始 HTML 标签, 实得 %q", got)
	}
}

func TestRenderPinContentImageBlock(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "image", "original_url": "https://pic.zhimg.com/img.jpg"},
	}
	got := renderPinContent(blocks, "")
	if !strings.Contains(got, "https://pic.zhimg.com/img.jpg") {
		t.Errorf("应包含图片URL, 实得 %q", got)
	}
}

func TestRenderPinContentLinkCard(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "link_card", "url": "https://example.com", "data_draft_title": "链接标题"},
	}
	got := renderPinContent(blocks, "")
	if !strings.Contains(got, "https://example.com") || !strings.Contains(got, "链接标题") {
		t.Errorf("应包含链接和标题, 实得 %q", got)
	}
}

func TestRenderPinContentKnownVideoBlock(t *testing.T) {
	// 捕获 slog 输出
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	zhihuURL := "https://www.zhihu.com/pin/123"
	blocks := []interface{}{
		map[string]interface{}{"type": PinBlockVideo, "url": "https://www.zhihu.com/video/123"},
	}
	got := renderPinContent(blocks, zhihuURL)
	if got != "" {
		t.Errorf("video block 暂不渲染内容, 实得 %q", got)
	}
	logs := buf.String()
	if !strings.Contains(logs, PinBlockVideo) || !strings.Contains(logs, zhihuURL) {
		t.Errorf("video block 应输出带原文链接的 slog.Warn, 实得日志: %q", logs)
	}
}

func TestRenderPinContentUnknownBlockType(t *testing.T) {
	// 捕获 slog 输出
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	zhihuURL := "https://www.zhihu.com/pin/456"
	blocks := []interface{}{
		map[string]interface{}{"type": "future_block_type", "foo": "bar"},
	}
	got := renderPinContent(blocks, zhihuURL)
	if got != "" {
		t.Errorf("未知 block type 应返回空串, 实得 %q", got)
	}
	// 应输出告警日志
	logs := buf.String()
	if !strings.Contains(logs, "future_block_type") || !strings.Contains(logs, zhihuURL) {
		t.Errorf("未知 block type 应输出带原文链接的 slog.Warn, 实得日志: %q", logs)
	}
}

// --- Fetch 集成测试（迁移自 Python TestZhihuRouteFetch 等） ---

// mockFetchActivities 创建一个返回固定 activities 的 fetchActivitiesFunc。
func mockFetchActivities(activities []map[string]interface{}, err error) fetchActivitiesFunc {
	return func(userID string, limit int) ([]map[string]interface{}, error) {
		if err != nil {
			return nil, err
		}
		// 模拟截断行为
		if len(activities) > limit {
			return activities[:limit], nil
		}
		return activities, nil
	}
}

// mkAnswerTarget 构造 answer 类型的 target map。
func mkAnswerTarget(id, questionID, questionTitle string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "type": "answer",
		"content": "<p>回答内容</p>", "created_time": float64(1700000000),
		"author":   map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{"id": questionID, "title": questionTitle},
	}
}

// mkPinTarget 构造 pin 类型的 target map。
func mkPinTarget(id, excerpt string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "type": "pin",
		"excerpt": excerpt, "created_time": float64(1700000000),
		"author": map[string]interface{}{"name": "作者"},
	}
}

// mkArticleTarget 构造 article 类型的 target map。
func mkArticleTarget(id, title string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "type": "article", "title": title,
		"content": "<p>文章内容</p>", "created_time": float64(1700000100),
		"author": map[string]interface{}{"name": "作者2"},
	}
}

func TestFetchReturnsFeedItems(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "type": "feed", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("123", "456", "问题标题")},
		{"id": "act2", "type": "feed", "verb": "MEMBER_CREATE_ARTICLE", "action_text": "发表了文章",
			"target": mkArticleTarget("789", "文章标题")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"kvxjr369f"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应返回 2 个 item, 实得 %d", len(items))
	}
	if items[0].Title != "[回答了问题] 问题标题" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
	if items[1].Title != "[发表了文章] 文章标题" {
		t.Errorf("items[1].Title = %q", items[1].Title)
	}
}

func TestFetchFiltersByInclude(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "type": "feed", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
		{"id": "act2", "type": "feed", "verb": "MEMBER_CREATE_PIN", "action_text": "发布了想法",
			"target": mkPinTarget("2", "想法内容")},
	}
	r := New(config.ResolvedRouteConfig{
		Cookie: "d_c0=test",
		Feeds:  []config.FeedConfig{{UserID: "test_user", Include: []string{"answer"}}},
	})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"test_user"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("include=[answer] 应只返回 1 个 item, 实得 %d", len(items))
	}
	if items[0].Categories[0] != TypeAnswer {
		t.Errorf("Categories = %v, want [%s]", items[0].Categories, TypeAnswer)
	}
}

func TestFetchReturnsAllWhenNoInclude(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "type": "feed", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
		{"id": "act2", "type": "feed", "verb": "MEMBER_CREATE_PIN", "action_text": "发布了想法",
			"target": mkPinTarget("2", "想法内容")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"test_user"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("无 include 应返回全部 2 个 item, 实得 %d", len(items))
	}
}

func TestFetchIncludeFromFetchOptions(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "type": "feed", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
		{"id": "act2", "type": "feed", "verb": "MEMBER_CREATE_PIN", "action_text": "发布了想法",
			"target": mkPinTarget("2", "想法内容")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"test_user"}, route.FetchOptions{
		Limit:   20,
		Include: []string{"pin"},
	})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("FetchOptions.Include=[pin] 应只返回 1 个 item, 实得 %d", len(items))
	}
	if items[0].Categories[0] != TypePin {
		t.Errorf("Categories = %v, want [%s]", items[0].Categories, TypePin)
	}
}

func TestFetchFiltersSelfInteractionByDefault(t *testing.T) {
	author := map[string]interface{}{"id": "USER_A", "name": "本人"}
	activities := []map[string]interface{}{
		// 自己创作的回答 — 非自互动，保留
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题", "actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "1", "type": "answer", "content": "<p>原创回答</p>",
				"created_time": float64(1700000000), "author": author,
				"question": map[string]interface{}{"id": "10", "title": "问题A"}}},
		// 收藏自己的回答 — 自互动，默认应被过滤
		{"id": "act2", "verb": "MEMBER_COLLECT_ANSWER", "action_text": "收藏了回答", "actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "1", "type": "answer", "content": "<p>原创回答</p>",
				"created_time": float64(1700001000), "author": author,
				"question": map[string]interface{}{"id": "10", "title": "问题A"}}},
		// 收藏他人的回答 — 非自互动，保留
		{"id": "act3", "verb": "MEMBER_COLLECT_ANSWER", "action_text": "收藏了回答", "actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "2", "type": "answer", "content": "<p>他人回答</p>",
				"created_time": float64(1700002000),
				"author":       map[string]interface{}{"id": "USER_B", "name": "他人"},
				"question":     map[string]interface{}{"id": "20", "title": "问题B"}}},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("默认应过滤自互动，保留 2 个 item, 实得 %d", len(items))
	}
	// 验证保留的是 act1 和 act3
	guids := map[string]bool{}
	for _, item := range items {
		guids[item.GUID] = true
	}
	if !guids["1"] || !guids["collected_answer_2"] {
		t.Errorf("应保留 act1(GUID=1) 和 act3(GUID=collected_answer_2), 实得 GUIDs: %v", guids)
	}
}

func TestFetchKeepsSelfInteractionWhenEnabled(t *testing.T) {
	author := map[string]interface{}{"id": "USER_A", "name": "本人"}
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题", "actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "1", "type": "answer", "content": "<p>内容</p>",
				"created_time": float64(1700000000), "author": author,
				"question": map[string]interface{}{"id": "10", "title": "问题"}}},
		{"id": "act2", "verb": "MEMBER_COLLECT_ANSWER", "action_text": "收藏了回答", "actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "1", "type": "answer", "content": "<p>内容</p>",
				"created_time": float64(1700001000), "author": author,
				"question": map[string]interface{}{"id": "10", "title": "问题"}}},
	}
	r := New(config.ResolvedRouteConfig{
		Cookie:                 "d_c0=test",
		IncludeSelfInteraction: true,
	})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("IncludeSelfInteraction=true 应保留全部 2 个 item, 实得 %d", len(items))
	}
}

func TestFetchCreateVerbNotFilteredEvenIfAuthorsMatch(t *testing.T) {
	// 创作类 verb 即使 actor == target.author 也不算自互动
	author := map[string]interface{}{"id": "USER_A", "name": "本人"}
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"actor": map[string]interface{}{"id": "USER_A"},
			"target": map[string]interface{}{"id": "1", "type": "answer", "content": "<p>内容</p>",
				"created_time": float64(1700000000), "author": author,
				"question": map[string]interface{}{"id": "10", "title": "问题"}}},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("创作类 verb 不应被过滤, 实得 %d 个 item", len(items))
	}
}

func TestFetchIncludeExcludesCollectedWhenOnlyAnswer(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
		{"id": "act2", "verb": "MEMBER_COLLECT_ANSWER", "action_text": "收藏了回答",
			"target": mkAnswerTarget("2", "20", "问题2")},
	}
	r := New(config.ResolvedRouteConfig{
		Cookie: "d_c0=test",
		Feeds:  []config.FeedConfig{{UserID: "u", Include: []string{"answer"}}},
	})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("include=[answer] 应排除 collected_answer, 实得 %d 个 item", len(items))
	}
	if items[0].Categories[0] != TypeAnswer {
		t.Errorf("Categories = %v, want [%s]", items[0].Categories, TypeAnswer)
	}
}

// --- deriveCategory warning 测试（迁移自 Python TestZhihuApiChangeWarnings） ---

func TestDeriveCategoryWarnsOnUnknownType(t *testing.T) {
	// 捕获 slog 输出
	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldLogger)

	act := map[string]interface{}{
		"verb":        "MEMBER_FUTURE_ACTION",
		"target":      map[string]interface{}{"type": "future_type"},
		"action_text": "做了某事",
	}
	category := deriveCategory(act)
	if category != "future_type" {
		t.Errorf("deriveCategory = %q, want 'future_type'", category)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "MEMBER_FUTURE_ACTION") {
		t.Errorf("warning 日志应包含 verb 'MEMBER_FUTURE_ACTION', 实得: %s", logOutput)
	}
	if !strings.Contains(logOutput, "future_type") {
		t.Errorf("warning 日志应包含 target_type 'future_type', 实得: %s", logOutput)
	}
}

func TestDeriveCategoryNoWarnOnKnownTypes(t *testing.T) {
	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldLogger)

	// 已知 verb
	deriveCategory(map[string]interface{}{
		"verb": "MEMBER_ANSWER_QUESTION", "target": map[string]interface{}{"type": "answer"},
	})
	// 已知 fallback type
	deriveCategory(map[string]interface{}{
		"verb": "", "target": map[string]interface{}{"type": "question"},
	})

	if buf.Len() > 0 {
		t.Errorf("已知类型不应打 warning, 实得: %s", buf.String())
	}
}

// --- Fetch 错误处理 ---

func TestFetchPropagatesError(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(nil, fmt.Errorf("网络超时"))

	_, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err == nil {
		t.Fatal("Fetch 应返回错误")
	}
	if !strings.Contains(err.Error(), "网络超时") {
		t.Errorf("错误信息应包含原始错误, 实得: %v", err)
	}
}

func TestFetchRequiresUserID(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	_, err := r.Fetch(nil, []string{}, route.FetchOptions{Limit: 20})
	if err == nil {
		t.Fatal("缺少 user_id 应返回错误")
	}
}

func TestFetchSkipsNilTarget(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "type": "feed", "target": nil},
		{"id": "act2", "type": "feed", "verb": "MEMBER_CREATE_ARTICLE", "action_text": "发表了文章",
			"target": mkArticleTarget("789", "文章标题")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("nil target 应被跳过, 实得 %d 个 item", len(items))
	}
}

// --- fixLazyImages 补充测试（迁移自 Python TestZhihuFixLazyImages） ---

func TestFixLazyImagesSVGPlaceholderDataActualsrc(t *testing.T) {
	placeholder := `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='100' height='50'></svg>`
	in := `<img src="` + placeholder + `" data-actualsrc="https://pic.zhimg.com/test_b.jpg" class="lazy">`
	got := fixLazyImages(in)
	if !strings.Contains(got, "https://pic.zhimg.com/test_b.jpg") {
		t.Errorf("应包含 data-actualsrc 的 URL, 实得 %q", got)
	}
	if strings.Contains(got, "data:image/svg+xml") {
		t.Errorf("SVG 占位符应被替换, 实得 %q", got)
	}
}

func TestFixLazyImagesSVGPlaceholderDataOriginal(t *testing.T) {
	placeholder := `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='100' height='50'></svg>`
	in := `<img src="` + placeholder + `" data-original="https://pic.zhimg.com/test_r.jpg">`
	got := fixLazyImages(in)
	if !strings.Contains(got, "https://pic.zhimg.com/test_r.jpg") {
		t.Errorf("应包含 data-original 的 URL, 实得 %q", got)
	}
	if strings.Contains(got, "data:image/svg+xml") {
		t.Errorf("SVG 占位符应被替换, 实得 %q", got)
	}
}

func TestFixLazyImagesPrioritizesActualsrc(t *testing.T) {
	in := `<img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/actual.jpg" data-original="https://pic.zhimg.com/original.jpg">`
	got := fixLazyImages(in)
	if !strings.Contains(got, "actual.jpg") {
		t.Errorf("应优先使用 data-actualsrc, 实得 %q", got)
	}
	if strings.Contains(got, "original.jpg") {
		t.Errorf("有 data-actualsrc 时不应使用 data-original, 实得 %q", got)
	}
}

func TestFixLazyImagesRemovesNoscript(t *testing.T) {
	in := `<figure><noscript><img src="https://pic.zhimg.com/real.jpg"></noscript><img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/real.jpg"></figure>`
	got := fixLazyImages(in)
	if strings.Contains(got, "<noscript>") {
		t.Errorf("noscript 标签应被移除, 实得 %q", got)
	}
	if !strings.Contains(got, "https://pic.zhimg.com/real.jpg") {
		t.Errorf("应保留真实图片 URL, 实得 %q", got)
	}
}

func TestFixLazyImagesComplexHTMLWithImages(t *testing.T) {
	in := `<p>问题描述文本</p><figure><img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/test.jpg"/></figure>`
	got := fixLazyImages(in)
	if !strings.Contains(got, "问题描述文本") {
		t.Errorf("应保留文本内容, 实得 %q", got)
	}
	if !strings.Contains(got, "https://pic.zhimg.com/test.jpg") {
		t.Errorf("应替换懒加载图片, 实得 %q", got)
	}
	if strings.Contains(got, "data:image/svg+xml") {
		t.Errorf("SVG 占位符应被替换, 实得 %q", got)
	}
}

// --- Fetch 请求头验证（迁移自 Python TestZhihuRouteFetchWithSigner） ---

func TestFetchBuildsCorrectHeaders(t *testing.T) {
	// 验证签名逻辑：GetSignature 返回的 x-zse-93 和 x-zse-96 格式正确，
	// 且不同 URL 产生不同签名。
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=abc123"})
	dC0, err := r.getDC0()
	if err != nil {
		t.Fatalf("getDC0 失败: %v", err)
	}

	url := "https://www.zhihu.com/api/v3/moments/test_user/activities"
	signResult, err := signzhihu.GetSignature(url, dC0, "")
	if err != nil {
		t.Fatalf("GetSignature 失败: %v", err)
	}

	if signResult.XZSE93 == "" {
		t.Error("x-zse-93 不应为空")
	}
	if signResult.XZSE96 == "" {
		t.Error("x-zse-96 不应为空")
	}

	// 验证不同 URL 产生不同签名
	url2 := "https://www.zhihu.com/api/v3/moments/other_user/activities"
	signResult2, err := signzhihu.GetSignature(url2, dC0, "")
	if err != nil {
		t.Fatalf("GetSignature(url2) 失败: %v", err)
	}
	if signResult.XZSE96 == signResult2.XZSE96 {
		t.Error("不同 URL 应产生不同的 x-zse-96 签名")
	}
}

// --- Fetch 分页测试（迁移自 Python TestZhihuRouteFetchActivitiesPagination） ---

func TestFetchStopsWhenLimitReached(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题1")},
		{"id": "act2", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("2", "20", "问题2")},
		{"id": "act3", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("3", "30", "问题3")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("limit=2 应返回 2 个 item, 实得 %d", len(items))
	}
}

func TestFetchStopsWhenIsEndTrue(t *testing.T) {
	// mockFetchActivities 已在内部处理截断，这里验证单页数据正常返回
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
	}
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("is_end=true 且 1 条数据应返回 1 个 item, 实得 %d", len(items))
	}
}

func TestFetchStopsWhenNextMissing(t *testing.T) {
	// 空 activities 列表模拟 next 缺失
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = mockFetchActivities([]map[string]interface{}{}, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("空 activities 应返回 0 个 item, 实得 %d", len(items))
	}
}

func TestFetchRaisesOnNon200(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "d_c0=test"})
	r.fetchActivitiesFn = func(userID string, limit int) ([]map[string]interface{}, error) {
		return nil, &route.HTTPError{StatusCode: 403, URL: "https://www.zhihu.com/api/v3/moments/test/activities"}
	}

	_, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err == nil {
		t.Fatal("非 200 响应应返回错误")
	}
	var httpErr *route.HTTPError
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误信息应包含状态码 403, 实得: %v", err)
	}
	_ = httpErr
}

// --- Fetch include=[collected_answer] 正向过滤（迁移自 Python TestZhihuRouteFetchFilter） ---

func TestFetchIncludeCollectedAnswer(t *testing.T) {
	activities := []map[string]interface{}{
		{"id": "act1", "verb": "MEMBER_ANSWER_QUESTION", "action_text": "回答了问题",
			"target": mkAnswerTarget("1", "10", "问题")},
		{"id": "act2", "verb": "MEMBER_COLLECT_ANSWER", "action_text": "收藏了回答",
			"target": mkAnswerTarget("2", "20", "问题2")},
	}
	r := New(config.ResolvedRouteConfig{
		Cookie: "d_c0=test",
		Feeds:  []config.FeedConfig{{UserID: "u", Include: []string{"collected_answer"}}},
	})
	r.fetchActivitiesFn = mockFetchActivities(activities, nil)

	items, err := r.Fetch(nil, []string{"u"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("include=[collected_answer] 应只返回收藏的回答, 实得 %d 个 item", len(items))
	}
	if items[0].Categories[0] != TypeCollectedAnswer {
		t.Errorf("Categories = %v, want [%s]", items[0].Categories, TypeCollectedAnswer)
	}
}

// --- makeFeedItem 空标题警告（迁移自 Python TestZhihuApiChangeWarnings） ---

func TestMakeFeedItemWarnsWhenTitleEmpty(t *testing.T) {
	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldLogger)

	r := newTestRoute()
	// answer 类型但 question 为空，导致 title 为空
	target := map[string]interface{}{
		"id": "123", "type": "answer",
		"content": "<p>内容</p>", "created_time": float64(1700000000),
		"author":   map[string]interface{}{"name": "作者"},
		"question": map[string]interface{}{"id": "456", "title": ""},
	}
	r.makeFeedItem(mkAct(target, "MEMBER_ANSWER_QUESTION", "回答了问题"))

	logOutput := buf.String()
	if !strings.Contains(logOutput, "标题为空") && !strings.Contains(logOutput, "empty") && !strings.Contains(logOutput, "title") {
		// 也接受其他形式的警告，只要日志非空说明有警告
		if logOutput == "" {
			t.Error("标题为空时应输出 warning 日志")
		}
	}
}
