package route

import (
	"strings"
	"testing"

	"github.com/PhiFever/RSSGen/internal/config"
)

type testRoute struct{}

func (testRoute) Name() string        { return "_test_route" }
func (testRoute) Description() string { return "test route" }
func (testRoute) FeedIDField() string { return "user_id" }
func (testRoute) FeedInfo([]string) (*FeedInfo, error) {
	return &FeedInfo{Title: "test", Link: "https://example.com"}, nil
}
func (testRoute) Fetch(ArticleStore, []string, FetchOptions) ([]FeedItem, error) {
	return []FeedItem{{Title: "item"}}, nil
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

func TestRegistrySnapshotAndUnregister(t *testing.T) {
	Register("_test_route", func(config.ResolvedRouteConfig) Route { return testRoute{} })
	defer Unregister("_test_route")

	registry := GetRegistry()
	factory, ok := registry["_test_route"]
	if !ok {
		t.Fatal("注册表快照应包含测试路由")
	}
	if factory(config.ResolvedRouteConfig{}).Description() != "test route" {
		t.Fatal("注册表工厂应创建测试路由")
	}

	delete(registry, "_test_route")
	if _, ok := GetRegistry()["_test_route"]; !ok {
		t.Fatal("修改快照不应影响全局注册表")
	}

	Unregister("_test_route")
	if _, ok := GetRegistry()["_test_route"]; ok {
		t.Fatal("Unregister 后全局注册表不应包含测试路由")
	}
}
