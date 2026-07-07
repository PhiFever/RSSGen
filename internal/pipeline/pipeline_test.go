package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
)

type pipelineTestRoute struct {
	fetchCount int
	lastOpts   route.FetchOptions
}

func (r *pipelineTestRoute) Name() string        { return "_pipeline_test" }
func (r *pipelineTestRoute) Description() string { return "pipeline test route" }
func (r *pipelineTestRoute) FeedIDField() string { return "user_id" }
func (r *pipelineTestRoute) Fetch(_ route.ArticleStore, _ []string, opts route.FetchOptions) (route.FeedResult, error) {
	r.fetchCount++
	r.lastOpts = opts
	return route.FeedResult{
		Info:  route.FeedInfo{Title: "Pipeline", Link: "https://example.com"},
		Items: []route.FeedItem{{Title: "Item", Link: "https://example.com/1"}},
	}, nil
}

func TestRefreshGeneratesAndCachesVariant(t *testing.T) {
	rt := &pipelineTestRoute{}
	route.Register("_pipeline_variant", func(config.ResolvedRouteConfig) route.Route {
		return rt
	})
	defer route.Unregister("_pipeline_variant")

	feedCache := cache.New(time.Minute)
	p := New(Config{
		FeedCache:    feedCache,
		RoutesConfig: map[string]config.RouteConfig{"_pipeline_variant": {Enabled: true}},
	})

	result, err := p.Refresh("_pipeline_variant", []string{"u1"}, route.FetchOptions{
		Format:  "rss",
		Limit:   7,
		Include: []string{"pin", "answer"},
	})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}
	if result.ItemCount != 1 {
		t.Fatalf("ItemCount = %d, want 1", result.ItemCount)
	}
	if !strings.Contains(result.XML, "<rss") {
		t.Fatalf("format=rss 应生成 RSS XML, got %s", result.XML)
	}
	wantKey := "_pipeline_variant/u1?format=rss&limit=7&include=answer,pin"
	if result.CacheKey != wantKey {
		t.Fatalf("CacheKey = %q, want %q", result.CacheKey, wantKey)
	}
	if cached, ok := feedCache.Get(wantKey); !ok || cached != result.XML {
		t.Fatal("Refresh 应按规范化变体键写入缓存")
	}
	if _, ok := feedCache.Get("_pipeline_variant/u1"); ok {
		t.Fatal("Refresh 不应写入无变体的旧缓存键")
	}
	if rt.lastOpts.Limit != 7 || len(rt.lastOpts.Include) != 2 || rt.lastOpts.Include[0] != "answer" {
		t.Fatalf("FetchOptions 未规范化后传入路由: %+v", rt.lastOpts)
	}
}

func TestRefreshReusesRouteInstance(t *testing.T) {
	factoryCalls := 0
	rt := &pipelineTestRoute{}
	route.Register("_pipeline_reuse", func(config.ResolvedRouteConfig) route.Route {
		factoryCalls++
		return rt
	})
	defer route.Unregister("_pipeline_reuse")

	p := New(Config{
		FeedCache:    cache.New(time.Minute),
		RoutesConfig: map[string]config.RouteConfig{"_pipeline_reuse": {Enabled: true}},
	})

	if _, err := p.Refresh("_pipeline_reuse", []string{"u1"}, route.FetchOptions{}); err != nil {
		t.Fatalf("第一次 Refresh 返回错误: %v", err)
	}
	if _, err := p.Refresh("_pipeline_reuse", []string{"u1"}, route.FetchOptions{}); err != nil {
		t.Fatalf("第二次 Refresh 返回错误: %v", err)
	}

	if factoryCalls != 1 {
		t.Fatalf("同一路由配置应复用 route 实例, factoryCalls = %d", factoryCalls)
	}
	if rt.fetchCount != 2 {
		t.Fatalf("Fetch 调用数 = %d, want 2", rt.fetchCount)
	}
}

func TestCacheKeyUsesConfiguredIncludeWhenRequestOmitsIt(t *testing.T) {
	p := New(Config{
		RoutesConfig: map[string]config.RouteConfig{
			"_pipeline_include": {
				DefaultInclude: []string{"pin", "answer"},
				Feeds: []config.FeedConfig{{
					UserID:  "u1",
					Include: []string{"article"},
				}},
			},
		},
	})

	got := p.CacheKey("_pipeline_include", []string{"u1"}, route.FetchOptions{})
	want := "_pipeline_include/u1?format=atom&limit=20&include=article"
	if got != want {
		t.Fatalf("CacheKey with feed include = %q, want %q", got, want)
	}

	got = p.CacheKey("_pipeline_include", []string{"u2"}, route.FetchOptions{})
	want = "_pipeline_include/u2?format=atom&limit=20&include=answer,pin"
	if got != want {
		t.Fatalf("CacheKey with default include = %q, want %q", got, want)
	}
}
