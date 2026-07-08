package route

import (
	"strings"
	"testing"

	"github.com/PhiFever/RSSGen/internal/config"
)

type testRoute struct{}

func (testRoute) Name() string { return "_test_route" }
func (testRoute) Fetch(ArticleStore, []string, FetchOptions) (FeedResult, error) {
	return FeedResult{
		Info:  FeedInfo{Title: "test", Link: "https://example.com"},
		Items: []FeedItem{{Title: "item"}},
	}, nil
}

func TestHTTPErrorString(t *testing.T) {
	err := (&HTTPError{StatusCode: 403, URL: "https://example.com/api"}).Error()
	if !strings.Contains(err, "403") || !strings.Contains(err, "https://example.com/api") {
		t.Fatalf("Error() = %q", err)
	}

	err = (&HTTPError{StatusCode: 500}).Error()
	if !strings.Contains(err, "500") {
		t.Fatalf("Error() = %q", err)
	}
}

func TestRegistrySnapshots(t *testing.T) {
	Register("_test_route", "test route", func(config.ResolvedRouteConfig) Route { return testRoute{} })

	registry := GetRegistry()
	factory, ok := registry["_test_route"]
	if !ok {
		t.Fatal("注册表快照应包含测试路由")
	}
	if factory(config.ResolvedRouteConfig{}).Name() != "_test_route" {
		t.Fatal("注册表工厂应创建测试路由")
	}
	descriptions := GetDescriptions()
	if descriptions["_test_route"] != "test route" {
		t.Fatalf("Description = %q", descriptions["_test_route"])
	}

	delete(registry, "_test_route")
	if _, ok := GetRegistry()["_test_route"]; !ok {
		t.Fatal("修改快照不应影响全局注册表")
	}
	delete(descriptions, "_test_route")
	if _, ok := GetDescriptions()["_test_route"]; !ok {
		t.Fatal("修改描述快照不应影响全局注册表")
	}
}

func TestNormalizeFetchOptions(t *testing.T) {
	opts := NormalizeFetchOptions(FetchOptions{
		Format:  "JSON",
		Include: []string{"pin", "", "answer", "pin"},
	})

	if opts.Format != "atom" {
		t.Fatalf("未知 format 应归一为 atom, got %q", opts.Format)
	}
	if opts.Limit != 20 {
		t.Fatalf("默认 Limit = %d, want 20", opts.Limit)
	}
	if got := strings.Join(opts.Include, ","); got != "answer,pin" {
		t.Fatalf("Include = %q, want answer,pin", got)
	}

	opts = NormalizeFetchOptions(FetchOptions{Format: "RSS", Limit: 5})
	if opts.Format != "rss" || opts.Limit != 5 {
		t.Fatalf("RSS/Limit 规范化失败: %+v", opts)
	}
}
