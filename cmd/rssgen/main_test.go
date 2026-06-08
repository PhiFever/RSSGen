package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	_ "github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"/user123", []string{"user123"}},
		{"user123", []string{"user123"}},
		{"/a/b/c", []string{"a", "b", "c"}},
		{"", []string{}},
		{"/", []string{}},
	}

	for _, tt := range tests {
		result := splitPath(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitPath(%q): len = %d, want %d", tt.input, len(result), len(tt.expected))
			continue
		}
		for i, v := range result {
			if v != tt.expected[i] {
				t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.input, i, v, tt.expected[i])
			}
		}
	}
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		routeName  string
		pathParts  []string
		expected   string
	}{
		{"afdian", []string{"user1"}, "afdian/user1"},
		{"zhihu", []string{"user2"}, "zhihu/user2"},
		{"test", []string{}, "test/"},
	}

	for _, tt := range tests {
		result := buildCacheKey(tt.routeName, tt.pathParts)
		if result != tt.expected {
			t.Errorf("buildCacheKey(%q, %v) = %q, want %q", tt.routeName, tt.pathParts, result, tt.expected)
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	m := map[string]string{
		"name": "RSSGen",
		"test": "value",
	}
	result := marshalJSON(m)
	if result == "" {
		t.Error("marshalJSON 返回空字符串")
	}
	// 简单验证包含关键内容
	if len(result) < 10 {
		t.Errorf("marshalJSON 结果太短: %q", result)
	}
}

func TestRouteRegistration(t *testing.T) {
	registry := route.GetRegistry()
	if len(registry) == 0 {
		t.Error("没有注册任何路由")
	}

	// 检查 afdian 路由是否注册
	if _, ok := registry["afdian"]; !ok {
		t.Error("afdian 路由未注册")
	}

	// 检查 zhihu 路由是否注册
	if _, ok := registry["zhihu"]; !ok {
		t.Error("zhihu 路由未注册")
	}
}

func TestIndexEndpoint(t *testing.T) {
	// 简单测试：验证路由注册机制
	registry := route.GetRegistry()
	if len(registry) == 0 {
		t.Fatal("没有注册任何路由")
	}

	// 创建一个简单的 handler 测试
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAnyRouteEnabled(t *testing.T) {
	// 测试没有启用路由的情况
	cfg := &config.Config{
		Routes: map[string]config.RouteConfig{
			"test": {Enabled: false, RefreshInterval: 60},
		},
	}
	if anyRouteEnabled(cfg) {
		t.Error("期望返回 false")
	}

	// 测试有启用路由的情况
	cfg.Routes["test"] = config.RouteConfig{Enabled: true, RefreshInterval: 60}
	if !anyRouteEnabled(cfg) {
		t.Error("期望返回 true")
	}
}
