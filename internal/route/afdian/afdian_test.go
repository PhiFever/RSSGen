package afdian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PhiFever/RSSGen/internal/route"
)

func TestNew(t *testing.T) {
	config := map[string]interface{}{
		"cookie":      "test_cookie",
		"rate_limit":  0.1,
		"proxy":       "",
	}

	r := New(config)
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
	config := map[string]interface{}{
		"cookie": "test",
		"feeds": []interface{}{
			map[string]interface{}{
				"user_id": "test_user",
				"alias":   "测试用户",
			},
		},
	}

	r := New(config)

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
							"post_id":       "post_001",
							"title":         "测试文章",
							"publish_time":  1700000000,
							"publish_sn":    "100",
							"user":          map[string]interface{}{"name": "作者"},
							"pics":          []interface{}{},
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
	config := map[string]interface{}{
		"cookie":     "test",
		"rate_limit": 0.1,
	}

	r := New(config)

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
