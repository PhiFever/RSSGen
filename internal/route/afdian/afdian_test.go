package afdian

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if r.Description() != "爱发电创作者动态订阅" {
		t.Errorf("Description() = %q, want %q", r.Description(), "爱发电创作者动态订阅")
	}
	if r.FeedIDField() != "user_id" {
		t.Errorf("FeedIDField() = %q, want %q", r.FeedIDField(), "user_id")
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
	info, err := r.FeedInfo([]string{"test_user"})
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
	_, err = r.FeedInfo([]string{})
	if err == nil {
		t.Error("期望返回错误，但没有")
	}
}

func TestParseCookieString(t *testing.T) {
	tests := []struct {
		input    string
		expected map[string]string
	}{
		{
			input:    "key1=val1; key2=val2",
			expected: map[string]string{"key1": "val1", "key2": "val2"},
		},
		{
			input:    "",
			expected: map[string]string{},
		},
		{
			input:    "  key=val  ",
			expected: map[string]string{"key": "val"},
		},
	}

	for _, tt := range tests {
		result := parseCookieString(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseCookieString(%q): len = %d, want %d", tt.input, len(result), len(tt.expected))
			continue
		}
		for k, v := range tt.expected {
			if result[k] != v {
				t.Errorf("parseCookieString(%q)[%q] = %q, want %q", tt.input, k, result[k], v)
			}
		}
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
			json.NewEncoder(w).Encode(resp)

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
			json.NewEncoder(w).Encode(resp)

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
			json.NewEncoder(w).Encode(resp)

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
	_, err := r.Fetch(nil, []string{"test_user"}, route.FetchOptions{Limit: 5})
	// 期望错误（因为 URL 是硬编码的，无法连接）
	if err == nil {
		t.Log("Fetch 成功（可能连接到了真实服务器）")
	} else {
		t.Logf("Fetch 错误（预期）: %v", err)
	}
}

// --- Store 缓存测试（迁移自 Python test_afdian_caching.py） ---

// mockStore 是一个简单的内存 ArticleStore 实现，用于测试。
type mockStore struct {
	data map[string]string // key: "route:id" → content
}

func newMockStore() *mockStore {
	return &mockStore{data: make(map[string]string)}
}

func (s *mockStore) Get(routeName, articleID string) (string, bool, error) {
	key := routeName + ":" + articleID
	content, ok := s.data[key]
	return content, ok, nil
}

func (s *mockStore) Save(routeName, articleID, content string) error {
	key := routeName + ":" + articleID
	s.data[key] = content
	return nil
}

func (s *mockStore) HasArticles(routeName string) (bool, error) {
	prefix := routeName + ":"
	for k := range s.data {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			return true, nil
		}
	}
	return false, nil
}

// makePost 构造帖子数据。
func makePost(postID string) map[string]interface{} {
	return map[string]interface{}{
		"post_id":      postID,
		"title":        "title-" + postID,
		"publish_time": float64(1700000000),
		"pics":         []interface{}{},
		"user":         map[string]interface{}{"name": "作者"},
	}
}

func TestFetchStoreHitSkipsAPI(t *testing.T) {
	store := newMockStore()
	// 预填充缓存
	store.Save("afdian", "post1", "<p>cached content</p>")

	detailCalled := false
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid1", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{makePost("post1")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		detailCalled = true
		return "<p>should not be called</p>", nil
	}

	items, err := r.Fetch(store, []string{"slug1"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if detailCalled {
		t.Error("store 命中时不应调用 getPostDetail")
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 个 item, 实得 %d", len(items))
	}
	if items[0].Content != "<p>cached content</p>" {
		t.Errorf("Content = %q, want cached content", items[0].Content)
	}
}

func TestFetchStoreMissCallsAPIAndSaves(t *testing.T) {
	store := newMockStore()

	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid1", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{makePost("post2")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "<p>fresh</p>", nil
	}

	items, err := r.Fetch(store, []string{"slug1"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if items[0].Content != "<p>fresh</p>" {
		t.Errorf("Content = %q", items[0].Content)
	}
	// 验证已落库
	saved, found, _ := store.Get("afdian", "post2")
	if !found || saved != "<p>fresh</p>" {
		t.Errorf("store 应保存 post2, found=%v, content=%q", found, saved)
	}
}

func TestFetchNoStoreStillWorks(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid1", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{makePost("post3")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "<p>detail</p>", nil
	}

	items, err := r.Fetch(nil, []string{"slug1"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if items[0].Content != "<p>detail</p>" {
		t.Errorf("Content = %q", items[0].Content)
	}
}

// --- Pipeline 测试（迁移自 Python test_afdian_pipeline.py） ---

func TestFetchPipelineOrderPreserved(t *testing.T) {
	store := newMockStore()

	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{
			makePost("p1"), makePost("p2"), makePost("p3"),
			makePost("p4"), makePost("p5"),
		}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, postID string) (string, error) {
		return fmt.Sprintf("<p>%s</p>", postID), nil
	}

	items, err := r.Fetch(store, []string{"slug"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("应返回 5 个 item, 实得 %d", len(items))
	}
	expectedGUIDs := []string{"p1", "p2", "p3", "p4", "p5"}
	for i, item := range items {
		if item.GUID != expectedGUIDs[i] {
			t.Errorf("items[%d].GUID = %q, want %q", i, item.GUID, expectedGUIDs[i])
		}
		if item.Content != fmt.Sprintf("<p>%s</p>", expectedGUIDs[i]) {
			t.Errorf("items[%d].Content = %q", i, item.Content)
		}
	}
}

func TestFetchPartialDetailFailure(t *testing.T) {
	store := newMockStore()

	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{
			makePost("p1"), makePost("p2"), makePost("p3"), makePost("p4"),
		}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, postID string) (string, error) {
		if postID == "p3" {
			return "", fmt.Errorf("simulated detail failure")
		}
		return fmt.Sprintf("<p>%s</p>", postID), nil
	}

	items, err := r.Fetch(store, []string{"slug"}, route.FetchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	// p3 失败应使用空内容，但仍保留在结果中
	if len(items) != 4 {
		t.Fatalf("应返回 4 个 item, 实得 %d", len(items))
	}
	// p3 内容应为空（失败降级）
	if items[2].Content != "" {
		t.Errorf("p3 失败后 Content 应为空, 实得 %q", items[2].Content)
	}
	// 成功的应已落库
	for _, postID := range []string{"p1", "p2", "p4"} {
		saved, found, _ := store.Get("afdian", postID)
		if !found || saved != fmt.Sprintf("<p>%s</p>", postID) {
			t.Errorf("store 应保存 %s, found=%v, content=%q", postID, found, saved)
		}
	}
	// p3 不应落库
	_, found, _ := store.Get("afdian", "p3")
	if found {
		t.Error("p3 失败后不应落库")
	}
}

func TestFetchDetailFailureUsesEmptyContent(t *testing.T) {
	// 验证详情失败时 content 为空字符串而非 panic
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return []map[string]interface{}{makePost("p1")}, nil
	}
	r.getPostDetailFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "", &route.HTTPError{StatusCode: 403, URL: "test"}
	}

	items, err := r.Fetch(nil, []string{"slug"}, route.FetchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Fetch 不应因详情失败而报错: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 个 item, 实得 %d", len(items))
	}
	if items[0].Content != "" {
		t.Errorf("详情失败后 Content 应为空, 实得 %q", items[0].Content)
	}
}

func TestFetchGetAuthorIDError(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "", fmt.Errorf("用户不存在")
	}

	_, err := r.Fetch(nil, []string{"slug"}, route.FetchOptions{Limit: 5})
	if err == nil {
		t.Fatal("getAuthorID 失败应返回错误")
	}
}

func TestFetchGetPostListError(t *testing.T) {
	r := New(config.ResolvedRouteConfig{Cookie: "test"})
	r.getAuthorIDFn = func(_ *scraper.Scraper, _ string) (string, error) {
		return "uid", nil
	}
	r.getPostListFn = func(_ *scraper.Scraper, _, _ string, _ int) ([]map[string]interface{}, error) {
		return nil, &route.HTTPError{StatusCode: 403, URL: "test"}
	}

	_, err := r.Fetch(nil, []string{"slug"}, route.FetchOptions{Limit: 5})
	if err == nil {
		t.Fatal("getPostList 失败应返回错误")
	}
}
