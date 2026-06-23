package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRoute(t *testing.T) {
	global := ScraperConfig{
		Proxy:       "http://global-proxy:8080",
		RateLimit:   1.0,
		Impersonate: "chrome_131",
	}

	t.Run("仅全局配置时路由继承全局默认值", func(t *testing.T) {
		// 回归 bug #3：后台刷新路径曾丢失全局 proxy/impersonate/rate_limit。
		rc := RouteConfig{Cookie: "c"}
		got := ResolveRoute(global, rc)

		if got.Proxy != "http://global-proxy:8080" {
			t.Errorf("Proxy = %q, want 全局值", got.Proxy)
		}
		if got.Impersonate != "chrome_131" {
			t.Errorf("Impersonate = %q, want 全局值", got.Impersonate)
		}
		if got.RateLimit != 1.0 {
			t.Errorf("RateLimit = %v, want 1.0", got.RateLimit)
		}
		if got.Cookie != "c" {
			t.Errorf("Cookie = %q, want %q", got.Cookie, "c")
		}
	})

	t.Run("路由级覆盖优先于全局", func(t *testing.T) {
		rate := 0.5
		proxy := "http://route-proxy:1080"
		imp := "chrome_124"
		self := true
		rc := RouteConfig{
			Cookie:                 "c",
			RateLimit:              &rate,
			Proxy:                  &proxy,
			Impersonate:            &imp,
			IncludeSelfInteraction: &self,
			DefaultInclude:         []string{"answer"},
			Feeds:                  []FeedConfig{{UserID: "u1", Alias: "用户1"}},
		}
		got := ResolveRoute(global, rc)

		if got.RateLimit != 0.5 {
			t.Errorf("RateLimit = %v, want 0.5（路由覆盖）", got.RateLimit)
		}
		if got.Proxy != "http://route-proxy:1080" {
			t.Errorf("Proxy = %q, want 路由覆盖值", got.Proxy)
		}
		if got.Impersonate != "chrome_124" {
			t.Errorf("Impersonate = %q, want 路由覆盖值", got.Impersonate)
		}
		if !got.IncludeSelfInteraction {
			t.Error("IncludeSelfInteraction = false, want true")
		}
		if len(got.Feeds) != 1 || got.Feeds[0].UserID != "u1" {
			t.Errorf("Feeds 透传错误: %+v", got.Feeds)
		}
	})
}

func TestLoadParsesPreinitURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`
refresher:
  preinit_url: "https://example.com/warmup"
routes: {}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Refresher.PreinitURL != "https://example.com/warmup" {
		t.Fatalf("PreinitURL = %q", cfg.Refresher.PreinitURL)
	}
}

func TestLoadParsesRouteRefreshJitter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`
routes:
  zhihu:
    enabled: true
    refresh_interval: 14400
    refresh_jitter: 900
    feeds:
      - user_id: "u1"
        limit: 20
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Routes["zhihu"].RefreshJitter != 900 {
		t.Fatalf("RefreshJitter = %d, want 900", cfg.Routes["zhihu"].RefreshJitter)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("缺失配置文件应返回错误")
	}

	path := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(path, []byte("server:\n  host: [bad"), 0o600); err != nil {
		t.Fatalf("写入坏配置失败: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("无效 YAML 应返回错误")
	}
}
