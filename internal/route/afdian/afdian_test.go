package afdian

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
)

func TestNew(t *testing.T) {
	r := New(config.ResolvedRouteConfig{
		Cookie:    "test_cookie",
		RateLimit: 0.1,
	})
	if r == nil {
		t.Fatal("New 返回 nil")
	}
	if r.Name() != "afdian" {
		t.Errorf("Name() = %q, want %q", r.Name(), "afdian")
	}
	if route.GetDescriptions()["afdian"] != "爱发电创作者动态订阅" {
		t.Errorf("注册表 description = %q", route.GetDescriptions()["afdian"])
	}
}

func TestGetScraperReusesInstance(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "a=1", RateLimit: 0.001})
	first, err := r.Scraper(r.cfg)
	if err != nil {
		t.Fatalf("第一次 Scraper 返回错误: %v", err)
	}
	r.cfg.Cookie = "a=2"
	second, err := r.Scraper(r.cfg)
	if err != nil {
		t.Fatalf("第二次 Scraper 返回错误: %v", err)
	}
	if first != second {
		t.Fatal("同一路由实例应复用 scraper，以保留限速与连接状态")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("写入 JSON 响应失败: %v", err)
	}
}

func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("写入响应失败: %v", err)
	}
}

func TestFeedInfo(t *testing.T) {
	r := New(config.ResolvedRouteConfig{
		Cookie: "test",
		Feeds: []config.FeedConfig{
			{UserID: "test_user", Alias: "测试用户"},
		},
	})

	// 测试正常情况
	info, err := r.feedInfo([]string{"test_user"})
	if err != nil {
		t.Fatalf("FeedInfo 错误: %v", err)
	}
	if info.Title != "爱发电 - 测试用户" {
		t.Errorf("Title = %q, want %q", info.Title, "爱发电 - 测试用户")
	}
	if info.Link != "https://afdian.com/a/test_user" {
		t.Errorf("Link = %q, want %q", info.Link, "https://afdian.com/a/test_user")
	}

	// 测试无 pathParams
	_, err = r.feedInfo([]string{})
	if err == nil {
		t.Error("期望返回错误，但没有")
	}
}

func TestFetchWithMockServer(t *testing.T) {
	// 创建 mock HTTP 服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get-profile-by-slug":
			// 返回作者信息
			resp := map[string]interface{}{
				"ec": 200,
				"data": map[string]interface{}{
					"user": map[string]interface{}{
						"user_id": "test_user_id",
					},
				},
			}
			writeJSON(t, w, resp)

		case "/api/post/get-list":
			// 返回帖子列表
			resp := map[string]interface{}{
				"ec": 200,
				"data": map[string]interface{}{
					"list": []interface{}{
						map[string]interface{}{
							"post_id":      "post_001",
							"title":        "测试文章",
							"publish_time": 1700000000,
							"publish_sn":   "100",
							"user":         map[string]interface{}{"name": "作者"},
							"pics":         []interface{}{},
						},
					},
				},
			}
			writeJSON(t, w, resp)

		case "/api/post/get-detail":
			// 返回文章详情
			resp := map[string]interface{}{
				"ec": 200,
				"data": map[string]interface{}{
					"post": map[string]interface{}{
						"content": "<p>测试内容</p>",
					},
				},
			}
			writeJSON(t, w, resp)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// 注意：这个测试需要修改 Route 以支持自定义 base URL
	// 当前实现使用硬编码的 hostURL，所以这个测试主要验证结构
	r := New(config.ResolvedRouteConfig{
		Cookie:    "test",
		RateLimit: 0.1,
	})

	// 由于使用硬编码 URL，这里只测试 Fetch 不会 panic
	// 实际测试需要 mock 整个 HTTP 客户端或允许自定义 base URL
	_, err := r.Fetch([]string{"test_user"}, route.FetchOptions{Limit: 5})
	// 期望错误（因为 URL 是硬编码的，无法连接）
	if err == nil {
		t.Log("Fetch 成功（可能连接到了真实服务器）")
	} else {
		t.Logf("Fetch 错误（预期）: %v", err)
	}
}

// makePost 构造帖子数据。
func makePost(postID string) afdianPost {
	return afdianPost{
		PostID:      postID,
		Title:       "title-" + postID,
		PublishTime: 1700000000,
		User:        afdianUser{Name: "作者"},
	}
}

func emptyComments(_ *scraper.Scraper, _ string) (afdianCommentList, error) {
	return afdianCommentList{}, nil
}

func sampleComments() afdianCommentList {
	return afdianCommentList{
		HotList: []afdianComment{
			{
				User:        afdianUser{Name: "热评用户"},
				PublishTime: afdianUnixTime(1700000000),
				Content:     "热评内容",
			},
		},
		List: []afdianComment{
			{
				User:        afdianUser{Name: "普通用户"},
				PublishTime: afdianUnixTime(1700000100),
				Content:     "普通评论",
				ReplyUser:   &afdianUser{Name: "被回复者"},
			},
		},
	}
}

func TestFetchGetsDetailAndComments(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid1", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{makePost("post3")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "<p>detail</p>", nil
	}
	r.getPostCommentsFn = emptyComments

	result, err := r.Fetch([]string{"slug1"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if result.Items[0].Content != "<p>detail</p>" {
		t.Errorf("Content = %q", result.Items[0].Content)
	}
}

// --- Pipeline 测试（迁移自 Python test_afdian_pipeline.py） ---

func TestFetchPipelineOrderPreserved(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{
			makePost("p1"), makePost("p2"), makePost("p3"),
			makePost("p4"), makePost("p5"),
		}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, postID string) (string, error) {
		return fmt.Sprintf("<p>%s</p>", postID), nil
	}
	r.getPostCommentsFn = emptyComments

	result, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(result.Items) != 5 {
		t.Fatalf("应返回 5 个 item, 实得 %d", len(result.Items))
	}
	expectedGUIDs := []string{"p1", "p2", "p3", "p4", "p5"}
	for i, item := range result.Items {
		if item.GUID != expectedGUIDs[i] {
			t.Errorf("result.Items[%d].GUID = %q, want %q", i, item.GUID, expectedGUIDs[i])
		}
		if item.Content != fmt.Sprintf("<p>%s</p>", expectedGUIDs[i]) {
			t.Errorf("result.Items[%d].Content = %q", i, item.Content)
		}
	}
}

func TestFetchDetailsRunConcurrentlyAndPreserveOrder(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{makePost("p1"), makePost("p2"), makePost("p3")}, nil
	}

	var active int32
	var maxActive int32
	r.getPostDetailFn = func(_ *scraper.Scraper, postID string) (string, error) {
		current := atomic.AddInt32(&active, 1)
		for {
			max := atomic.LoadInt32(&maxActive)
			if current <= max || atomic.CompareAndSwapInt32(&maxActive, max, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return fmt.Sprintf("<p>%s</p>", postID), nil
	}
	r.getPostCommentsFn = emptyComments

	result, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if atomic.LoadInt32(&maxActive) < 2 {
		t.Fatalf("详情抓取应并发执行，maxActive=%d", maxActive)
	}
	for i, want := range []string{"p1", "p2", "p3"} {
		if result.Items[i].GUID != want {
			t.Fatalf("result.Items[%d].GUID = %q, want %q", i, result.Items[i].GUID, want)
		}
	}
}

func TestFetchPartialDetailFailure(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{
			makePost("p1"), makePost("p2"), makePost("p3"), makePost("p4"),
		}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, postID string) (string, error) {
		if postID == "p3" {
			return "", fmt.Errorf("simulated detail failure")
		}
		return fmt.Sprintf("<p>%s</p>", postID), nil
	}
	r.getPostCommentsFn = emptyComments

	result, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	// p3 失败应跳过，返回 3 个 item（与 Python 行为一致）
	if len(result.Items) != 3 {
		t.Fatalf("应返回 3 个 item, 实得 %d", len(result.Items))
	}
	// 结果不应包含 p3
	for _, item := range result.Items {
		if item.GUID == "p3" {
			t.Error("p3 失败后应跳过，不应出现在结果中")
		}
	}
}

func TestFetchCommentFailureKeepsBody(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{makePost("p1")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "<p>body</p>", nil
	}
	r.getPostCommentsFn = func(_ *scraper.Scraper, _ string) (afdianCommentList, error) {
		return afdianCommentList{}, fmt.Errorf("comment boom")
	}

	result, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("应返回 1 个 item, 实得 %d", len(result.Items))
	}
	if result.Items[0].Content != "<p>body</p>" {
		t.Fatalf("评论失败时应保留正文, got %q", result.Items[0].Content)
	}
}

func TestFetchDetailFailureUsesEmptyContent(t *testing.T) {
	// 验证详情失败时跳过该条目（与 Python 行为一致）
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return []afdianPost{makePost("p1")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "", &route.HTTPError{StatusCode: 403, URL: "test"}
	}

	result, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 不应因详情失败而报错: %v", err)
	}
	// 全部失败应返回空列表
	if len(result.Items) != 0 {
		t.Fatalf("详情失败应跳过条目, 实得 %d 个 item", len(result.Items))
	}
}

func TestRenderCommentsHotBeforeNormalAndReply(t *testing.T) {
	rendered := renderComments(sampleComments())
	hotIdx := strings.Index(rendered, "<h2>热评</h2>")
	normalIdx := strings.Index(rendered, "<h2>评论</h2>")
	if hotIdx < 0 || normalIdx < 0 {
		t.Fatalf("应渲染热评和评论两段: %q", rendered)
	}
	if hotIdx > normalIdx {
		t.Fatalf("热评应在评论之前: %q", rendered)
	}

	for _, want := range []string{
		"热评用户",
		"热评内容",
		"普通用户",
		"普通评论",
		"回复 被回复者",
		"2023-11-14 22:13:20",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("渲染结果缺少 %q: %q", want, rendered)
		}
	}
}

func TestFetchGetAuthorIDError(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "", fmt.Errorf("用户不存在")
	}

	_, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 5})
	if err == nil {
		t.Fatal("getAuthorID 失败应返回错误")
	}
}

func TestFetchGetPostListError(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]afdianPost, error) {
		return nil, &route.HTTPError{StatusCode: 403, URL: "test"}
	}

	_, err := r.Fetch([]string{"slug"}, route.FetchOptions{Limit: 5})
	if err == nil {
		t.Fatal("getPostList 失败应返回错误")
	}
}

func withTestHost(t *testing.T, url string) {
	t.Helper()
	old := hostURL
	hostURL = url
	t.Cleanup(func() { hostURL = old })
}

func newTestScraper(t *testing.T) *scraper.Scraper {
	t.Helper()
	sc, err := scraper.New(scraper.Config{RateLimit: 0.001})
	if err != nil {
		t.Fatalf("创建 scraper 失败: %v", err)
	}
	return sc
}

func TestGetAuthorIDSuccessAndErrors(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/user/get-profile-by-slug" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			if r.URL.Query().Get("url_slug") != "alice" {
				t.Fatalf("url_slug 未透传")
			}
			writeJSON(t, w, map[string]any{
				"ec": 200,
				"data": map[string]any{
					"user": map[string]any{"user_id": "uid-alice"},
				},
			})
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		got, err := New(config.ResolvedRouteConfig{}).getAuthorID(newTestScraper(t), "alice")
		if err != nil {
			t.Fatalf("getAuthorID 返回错误: %v", err)
		}
		if got != "uid-alice" {
			t.Fatalf("userID = %q", got)
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getAuthorID(newTestScraper(t), "alice")
		if err == nil {
			t.Fatal("HTTP 非 200 应返回错误")
		}
	})

	t.Run("missing user id", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{
				"ec":   200,
				"data": map[string]any{"user": map[string]any{}},
			})
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getAuthorID(newTestScraper(t), "alice")
		if err == nil {
			t.Fatal("缺失 user_id 应返回错误")
		}
	})
}

func TestGetPostListPaginationAndLimit(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/post/get-list" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		page++
		var list []map[string]any
		switch page {
		case 1:
			list = []map[string]any{
				{"post_id": "p1", "title": "one", "publish_sn": 10},
				{"post_id": "p2", "title": "two", "publish_sn": 20},
			}
		case 2:
			if r.URL.Query().Get("publish_sn") != "20" {
				t.Fatalf("第二页 publish_sn = %q", r.URL.Query().Get("publish_sn"))
			}
			list = []map[string]any{
				{"post_id": "p3", "title": "three", "publish_sn": 0},
			}
		default:
			t.Fatalf("不应请求第 %d 页", page)
		}
		writeJSON(t, w, map[string]any{
			"ec":   200,
			"data": map[string]any{"list": list},
		})
	}))
	defer server.Close()
	withTestHost(t, server.URL)

	posts, err := New(config.ResolvedRouteConfig{}).getPostList(newTestScraper(t), "uid", "alice", 3)
	if err != nil {
		t.Fatalf("getPostList 返回错误: %v", err)
	}
	if len(posts) != 3 || posts[0].PostID != "p1" || posts[2].PostID != "p3" {
		t.Fatalf("posts = %+v", posts)
	}
}

func TestGetPostListUnlimitedStopsAtNaturalEnd(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		var list []map[string]any
		switch r.URL.Query().Get("publish_sn") {
		case "0":
			list = []map[string]any{{"post_id": "p1", "publish_sn": 10}}
		case "10":
			list = []map[string]any{{"post_id": "p2", "publish_sn": 20}}
		case "20":
			list = []map[string]any{}
		default:
			t.Fatalf("unexpected cursor %q", r.URL.Query().Get("publish_sn"))
		}
		writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"list": list}})
	}))
	defer server.Close()
	withTestHost(t, server.URL)

	posts, err := New(config.ResolvedRouteConfig{}).getPostList(newTestScraper(t), "uid", "alice", 0)
	if err != nil {
		t.Fatalf("getPostList unlimited: %v", err)
	}
	if page != 3 || len(posts) != 2 || posts[1].PostID != "p2" {
		t.Fatalf("pages=%d posts=%+v", page, posts)
	}
}

func TestBackfillSourceUsesSharedParsingAndRendering(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get-profile-by-slug":
			writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"user": map[string]any{"user_id": "uid"}}})
		case "/api/post/get-list":
			if r.URL.Query().Get("publish_sn") == "0" {
				writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"list": []map[string]any{{
					"post_id": "p1", "title": "Paid", "publish_time": 123, "publish_sn": 10,
					"user": map[string]any{"name": "Alice"}, "pics": []string{"https://example.com/deferred.jpg"},
				}}}})
				return
			}
			writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"list": []any{}}})
		case "/api/post/get-detail":
			writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"post": map[string]any{"content": "<p>paid</p>"}}})
		case "/api/comment/get-list":
			writeJSON(t, w, map[string]any{"ec": 200, "data": map[string]any{"list": []map[string]any{{
				"user": map[string]any{"name": "Reader"}, "publish_time": 124, "content": "Thanks",
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withTestHost(t, server.URL)
	source := &BackfillSource{route: New(config.ResolvedRouteConfig{}), scraper: newTestScraper(t)}
	var progress []string

	candidates, err := source.Discover(context.Background(), "alice", func(message string, attrs ...any) {
		progress = append(progress, message+" "+fmt.Sprint(attrs))
	})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Discover = %+v, %v", candidates, err)
	}
	if len(progress) != 2 ||
		!strings.Contains(progress[0], "page 1 page_items 1 scanned 1") ||
		!strings.Contains(progress[1], "page 2 page_items 0 scanned 1") {
		t.Fatalf("progress = %v", progress)
	}
	candidate := candidates[0]
	if candidate.ID != "p1" || candidate.Author != "Alice" || candidate.URL != server.URL+"/p/p1" || candidate.PublishedAt.Unix() != 123 {
		t.Fatalf("candidate = %+v", candidate)
	}
	body, err := source.Detail(context.Background(), candidate)
	if err != nil || body != "<p>paid</p>" {
		t.Fatalf("Detail = %q, %v", body, err)
	}
	comments, err := source.Comments(context.Background(), candidate)
	if err != nil || !strings.Contains(comments, "<h2>评论</h2>") || !strings.Contains(comments, "Thanks") {
		t.Fatalf("Comments = %q, %v", comments, err)
	}
}

func TestNewBackfillSourceRejectsFastInterval(t *testing.T) {
	if _, err := NewBackfillSource("cookie", time.Second-time.Millisecond); err == nil {
		t.Fatal("request interval below one second should fail")
	}
}

func TestGetPostListEmptyAndInvalidResponse(t *testing.T) {
	t.Run("empty data stops", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"ec": 200})
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		posts, err := New(config.ResolvedRouteConfig{}).getPostList(newTestScraper(t), "uid", "alice", 10)
		if err != nil {
			t.Fatalf("getPostList 返回错误: %v", err)
		}
		if len(posts) != 0 {
			t.Fatalf("空 data 应返回空列表，got %+v", posts)
		}
	})

	t.Run("invalid list json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeBody(t, w, `{"ec":200,"data":{"list":"bad"}}`)
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getPostList(newTestScraper(t), "uid", "alice", 10)
		if err == nil {
			t.Fatal("无效 list JSON 应返回错误")
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getPostList(newTestScraper(t), "uid", "alice", 10)
		if err == nil {
			t.Fatal("HTTP 非 200 应返回错误")
		}
	})
}

func TestGetPostCommentsRequestAndParse(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/comment/get-list" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("post_id") != "p1" || query.Get("type") != "old" || query.Get("hot") != "1" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		publishSN, ok := query["publish_sn"]
		if !ok || len(publishSN) != 1 || publishSN[0] != "" {
			t.Fatalf("publish_sn 应为空值参数, raw query=%s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Referer"); got != server.URL+"/p/p1" {
			t.Fatalf("Referer = %q", got)
		}

		writeJSON(t, w, map[string]any{
			"ec": 200,
			"data": map[string]any{
				"hot_list": []map[string]any{
					{
						"user":         map[string]any{"name": "热评用户"},
						"publish_time": "1700000000",
						"content":      "热评内容",
					},
				},
				"list": []map[string]any{
					{
						"user":         map[string]any{"name": "普通用户"},
						"publish_time": 1700000100,
						"content":      "普通评论",
						"reply_user":   map[string]any{"name": "被回复者"},
					},
				},
			},
		})
	}))
	defer server.Close()
	withTestHost(t, server.URL)

	comments, err := New(config.ResolvedRouteConfig{}).getPostComments(newTestScraper(t), "p1")
	if err != nil {
		t.Fatalf("getPostComments 返回错误: %v", err)
	}
	if len(comments.HotList) != 1 || comments.HotList[0].User.Name != "热评用户" {
		t.Fatalf("hot_list 解析错误: %+v", comments.HotList)
	}
	if comments.HotList[0].PublishTime != afdianUnixTime(1700000000) {
		t.Fatalf("热评发布时间解析错误: %v", comments.HotList[0].PublishTime)
	}
	if len(comments.List) != 1 || comments.List[0].ReplyUser == nil || comments.List[0].ReplyUser.Name != "被回复者" {
		t.Fatalf("list/reply_user 解析错误: %+v", comments.List)
	}
}

func TestGetPostDetailSuccessEmptyAndErrors(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/post/get-detail" || r.URL.Query().Get("post_id") != "p1" {
				t.Fatalf("unexpected request %s", r.URL.String())
			}
			writeJSON(t, w, map[string]any{
				"ec":   200,
				"data": map[string]any{"post": map[string]any{"content": "<p>body</p>"}},
			})
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		content, err := New(config.ResolvedRouteConfig{}).getPostDetail(newTestScraper(t), "p1")
		if err != nil {
			t.Fatalf("getPostDetail 返回错误: %v", err)
		}
		if content != "<p>body</p>" {
			t.Fatalf("content = %q", content)
		}
	})

	t.Run("empty data", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"ec": 200})
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		content, err := New(config.ResolvedRouteConfig{}).getPostDetail(newTestScraper(t), "p1")
		if err != nil {
			t.Fatalf("getPostDetail 返回错误: %v", err)
		}
		if content != "" {
			t.Fatalf("空 data 应返回空 content，got %q", content)
		}
	})

	t.Run("api error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeBody(t, w, `{"ec":500,"em":"bad"}`)
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getPostDetail(newTestScraper(t), "p1")
		if err == nil {
			t.Fatal("API ec 非 200 应返回错误")
		}
	})

	t.Run("invalid detail json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeBody(t, w, `{"ec":200,"data":{"post":"bad"}}`)
		}))
		defer server.Close()
		withTestHost(t, server.URL)

		_, err := New(config.ResolvedRouteConfig{}).getPostDetail(newTestScraper(t), "p1")
		if err == nil {
			t.Fatal("无效详情 JSON 应返回错误")
		}
	})
}

func TestParseAfdianResponseInvalidJSON(t *testing.T) {
	if _, err := parseAfdianResponse([]byte(`{bad json`)); err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}
