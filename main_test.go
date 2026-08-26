package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/urfave/cli/v3"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/refresher"
	"github.com/PhiFever/RSSGen/internal/route"
	_ "github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func TestLoadBackfillEnv(t *testing.T) {
	loadedKey := "RSSGEN_TEST_DOTENV_LOADED"
	previous, existed := os.LookupEnv(loadedKey)
	if err := os.Unsetenv(loadedKey); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(loadedKey, previous)
		} else {
			_ = os.Unsetenv(loadedKey)
		}
	})

	existingKey := "RSSGEN_TEST_DOTENV_EXISTING"
	t.Setenv(existingKey, "from-process")
	envPath := filepath.Join(t.TempDir(), ".env")
	contents := loadedKey + "=from-file\n" + existingKey + "=from-file\n"
	if err := os.WriteFile(envPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := loadBackfillEnv(envPath); err != nil {
		t.Fatalf("loadBackfillEnv: %v", err)
	}
	if got := os.Getenv(loadedKey); got != "from-file" {
		t.Fatalf("loaded env = %q, want from-file", got)
	}
	if got := os.Getenv(existingKey); got != "from-process" {
		t.Fatalf("existing env = %q, want from-process", got)
	}
	if err := loadBackfillEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatalf("missing .env should be optional: %v", err)
	}
}

func TestLoadBackfillEnvRejectsMalformedFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("BROKEN='unterminated\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := loadBackfillEnv(envPath); err == nil {
		t.Fatal("malformed .env should fail")
	}
}

func TestCommandWithoutSubcommandShowsHelp(t *testing.T) {
	var output bytes.Buffer
	serverCalled := false
	backfillCalled := false
	cmd := newCommand(commandDependencies{
		runServer: func(context.Context) error {
			serverCalled = true
			return nil
		},
		runAfdianBackfill: func(context.Context, afdianBackfillCLIConfig, io.Writer) error {
			backfillCalled = true
			return nil
		},
		loadBackfillEnv: func(string) error {
			t.Fatal("根帮助不应加载 backfill .env")
			return nil
		},
		getenv: func(string) string {
			t.Fatal("根帮助不应读取环境变量")
			return ""
		},
	})
	cmd.Writer = &output
	cmd.ErrWriter = &output

	if err := cmd.Run(context.Background(), []string{"rssgen"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if serverCalled || backfillCalled {
		t.Fatalf("无子命令不应运行业务 action: server=%v backfill=%v", serverCalled, backfillCalled)
	}
	if got := output.String(); !strings.Contains(got, "COMMANDS:") || !strings.Contains(got, "server") || !strings.Contains(got, "afdian-backfill") {
		t.Fatalf("根帮助输出不完整: %q", got)
	}
}

func TestAfdianBackfillHelpHasNoSideEffects(t *testing.T) {
	var output bytes.Buffer
	cmd := newCommand(commandDependencies{
		runServer: func(context.Context) error {
			t.Fatal("backfill help 不应启动 server")
			return nil
		},
		runAfdianBackfill: func(context.Context, afdianBackfillCLIConfig, io.Writer) error {
			t.Fatal("backfill help 不应运行 backfill")
			return nil
		},
		loadBackfillEnv: func(string) error {
			t.Fatal("backfill help 不应加载 .env")
			return nil
		},
		getenv: func(string) string {
			t.Fatal("backfill help 不应读取环境变量")
			return ""
		},
	})
	cmd.Writer = &output
	cmd.ErrWriter = &output

	if err := cmd.Run(context.Background(), []string{"rssgen", "afdian-backfill", "--help"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := output.String(); !strings.Contains(got, "--miniflux-url") || !strings.Contains(got, "--dry-run") {
		t.Fatalf("backfill 帮助输出不完整: %q", got)
	}
}

func TestCommandDispatchesExplicitServer(t *testing.T) {
	serverCalled := false
	cmd := newCommand(commandDependencies{
		runServer: func(context.Context) error {
			serverCalled = true
			return nil
		},
		runAfdianBackfill: func(context.Context, afdianBackfillCLIConfig, io.Writer) error {
			t.Fatal("server 命令不应运行 backfill")
			return nil
		},
		loadBackfillEnv: func(string) error {
			t.Fatal("server 命令不应加载 backfill .env")
			return nil
		},
		getenv: func(string) string { return "" },
	})

	if err := cmd.Run(context.Background(), []string{"rssgen", "server"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !serverCalled {
		t.Fatal("server action 未运行")
	}
}

func TestCommandRejectsUnknownCommandAndPositionalArguments(t *testing.T) {
	newTestCommand := func() *cli.Command {
		return newCommand(commandDependencies{
			runServer:         func(context.Context) error { return nil },
			runAfdianBackfill: func(context.Context, afdianBackfillCLIConfig, io.Writer) error { return nil },
			loadBackfillEnv:   func(string) error { return nil },
			getenv:            func(string) string { return "present" },
		})
	}

	if err := newTestCommand().Run(context.Background(), []string{"rssgen", "serve"}); err == nil || commandExitCode(err) != 3 {
		t.Fatalf("未知命令 err=%v exit=%d, want exit 3", err, commandExitCode(err))
	}
	if err := newTestCommand().Run(context.Background(), []string{"rssgen", "server", "extra"}); err == nil || !strings.Contains(err.Error(), "不接受位置参数") {
		t.Fatalf("server 位置参数 err=%v", err)
	}
}

func TestServeRuntimeStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveRuntime(ctx, &runtimeApp{server: &http.Server{
			Addr:    "127.0.0.1:0",
			Handler: http.NewServeMux(),
		}})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveRuntime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveRuntime 未在 context 取消后停止")
	}
}

func TestServeRuntimeReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	err = serveRuntime(context.Background(), &runtimeApp{server: &http.Server{
		Addr:    listener.Addr().String(),
		Handler: http.NewServeMux(),
	}})
	if err == nil || !strings.Contains(err.Error(), "监听") {
		t.Fatalf("serveRuntime err=%v", err)
	}
}

func runBackfillCommandForTest(
	t *testing.T,
	args []string,
	getenv func(string) string,
) (afdianBackfillCLIConfig, error) {
	t.Helper()
	var got afdianBackfillCLIConfig
	cmd := newCommand(commandDependencies{
		runServer: func(context.Context) error {
			t.Fatal("backfill 命令不应启动 server")
			return nil
		},
		runAfdianBackfill: func(_ context.Context, cfg afdianBackfillCLIConfig, _ io.Writer) error {
			got = cfg
			return nil
		},
		loadBackfillEnv: func(filename string) error {
			if filename != ".env" {
				t.Fatalf("dotenv filename = %q", filename)
			}
			return nil
		},
		getenv: getenv,
	})
	var output bytes.Buffer
	cmd.Writer = &output
	cmd.ErrWriter = &output
	argv := append([]string{"rssgen", "afdian-backfill"}, args...)
	err := cmd.Run(context.Background(), argv)
	return got, err
}

func TestAfdianBackfillCommandBuildsConfig(t *testing.T) {
	getenv := func(key string) string {
		return map[string]string{
			"MINIFLUX_API_TOKEN": "token",
			"AFDIAN_COOKIE":      "auth=paid",
		}[key]
	}

	t.Run("list only needs Miniflux token", func(t *testing.T) {
		cfg, err := runBackfillCommandForTest(t, []string{"--miniflux-url", "https://miniflux.example", "--list-feeds"}, func(key string) string {
			if key == "MINIFLUX_API_TOKEN" {
				return "token"
			}
			return ""
		})
		if err != nil || cfg.action != backfill.ActionList || cfg.afdianCookie != "" {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("execute defaults to one second", func(t *testing.T) {
		cfg, err := runBackfillCommandForTest(t, []string{"--miniflux-url", "https://miniflux.example", "--feed-id", "42"}, getenv)
		if err != nil || cfg.action != backfill.ActionExecute || cfg.feedID != 42 || cfg.requestInterval != time.Second {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("dry run and slower interval", func(t *testing.T) {
		cfg, err := runBackfillCommandForTest(t, []string{
			"--miniflux-url", "https://miniflux.example", "--feed-id", "42", "--dry-run", "--request-interval", "2s",
		}, getenv)
		if err != nil || cfg.action != backfill.ActionDryRun || cfg.requestInterval != 2*time.Second {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})
}

func TestAfdianBackfillCommandRejectsUnsafeOrMissingInputs(t *testing.T) {
	validEnv := func(key string) string { return "present" }
	tests := [][]string{
		{"--miniflux-url", "https://miniflux.example", "--feed-id", "1", "--request-interval", "999ms"},
		{"--miniflux-url", "https://miniflux.example", "--list-feeds", "--feed-id", "1"},
		{"--miniflux-url", "https://miniflux.example", "--list-feeds", "--dry-run"},
		{"--miniflux-url", "https://miniflux.example"},
	}
	for _, args := range tests {
		if _, err := runBackfillCommandForTest(t, args, validEnv); err == nil {
			t.Fatalf("args %v should fail", args)
		}
	}
	if _, err := runBackfillCommandForTest(t,
		[]string{"--miniflux-url", "https://miniflux.example", "--feed-id", "1"},
		func(key string) string {
			if key == "MINIFLUX_API_TOKEN" {
				return "token"
			}
			return ""
		},
	); err == nil {
		t.Fatal("execute without AFDIAN_COOKIE should fail")
	}
}

func TestRunAfdianBackfillListsEligibleFeedsWithoutServerConfig(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/feeds" || r.Header.Get("X-Auth-Token") != "token" {
			t.Fatalf("request = %s token=%q", r.URL.Path, r.Header.Get("X-Auth-Token"))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 42, "title": "Alice", "feed_url": "http://rssgen:8000/feed/afdian/alice"},
			{"id": 43, "title": "Bob", "feed_url": "http://rssgen:8000/feed/zhihu/bob"},
		})
	}))
	defer server.Close()

	var output bytes.Buffer
	err := runAfdianBackfill(context.Background(), afdianBackfillCLIConfig{
		action:          backfill.ActionList,
		minifluxURL:     server.URL,
		requestInterval: time.Second,
		minifluxToken:   "token",
	}, &output)
	if err != nil {
		t.Fatalf("runAfdianBackfill: %v", err)
	}
	if !strings.Contains(output.String(), "42") || !strings.Contains(output.String(), "Alice") || strings.Contains(output.String(), "Bob") {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(logs.String(), "Miniflux feed 列表读取完成") || !strings.Contains(logs.String(), "eligible=1") {
		t.Fatalf("progress logs = %q", logs.String())
	}
}

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
		routeName string
		pathParts []string
		expected  string
	}{
		{"afdian", []string{"user1"}, "afdian/user1"},
		{"zhihu", []string{"user2"}, "zhihu/user2"},
		{"test", []string{}, "test"},
	}

	for _, tt := range tests {
		result := cache.BuildCacheKey(tt.routeName, tt.pathParts)
		if result != tt.expected {
			t.Errorf("BuildCacheKey(%q, %v) = %q, want %q", tt.routeName, tt.pathParts, result, tt.expected)
		}
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
	route.Register("_main_index", "main test route", func(config.ResolvedRouteConfig) route.Route {
		return &mainTestRoute{name: "_main_index"}
	})

	cfg := &config.Config{Routes: map[string]config.RouteConfig{}}
	feedCache := cache.New(10 * time.Second)
	handler := makeRouter(
		health.New(health.Config{}),
		feedCache,
		nil,
		cfg,
		pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.Routes}),
		nil,
		nil,
	)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
	var body struct {
		Name   string            `json:"name"`
		Routes map[string]string `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("首页响应不是 JSON: %v", err)
	}
	if body.Name != "RSSGen" || body.Routes["_main_index"] != "main test route" {
		t.Fatalf("首页响应 = %+v", body)
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

	cfg.Routes["test"] = config.RouteConfig{Enabled: true, PreheatOnStartup: true}
	if !anyRouteEnabled(cfg) {
		t.Error("只启用预热的路由也应启动后台刷新器")
	}
}

func TestBuildRuntime(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Cache:  config.CacheConfig{FeedTTL: 60},
		Notifier: config.NotifierConfig{
			Enabled: true,
			Services: []config.NotifierServiceConfig{{
				Type:       "feishu",
				WebhookURL: "https://example.com/hook",
			}},
		},
		Routes: map[string]config.RouteConfig{
			"_disabled": {Enabled: false},
		},
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		t.Fatalf("buildRuntime 返回错误: %v", err)
	}
	if app.server == nil || app.server.Handler == nil {
		t.Fatal("buildRuntime 应创建 HTTP server 和 handler")
	}
	if app.server.Addr != "127.0.0.1:0" {
		t.Fatalf("server addr = %q", app.server.Addr)
	}
	if app.refresher != nil {
		t.Fatal("没有启用调度的路由时不应创建 refresher")
	}

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	app.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runtime router /status = %d", w.Code)
	}
}

func TestBuildRuntimeStartsRefresherWhenRouteScheduled(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{Host: "127.0.0.1", Port: 0},
		Cache:     config.CacheConfig{FeedTTL: 60},
		Refresher: config.RefresherConfig{StartupDelay: 1},
		Routes: map[string]config.RouteConfig{
			"_scheduled": {Enabled: true, RefreshInterval: 60},
		},
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		t.Fatalf("buildRuntime 返回错误: %v", err)
	}
	if app.refresher == nil {
		t.Fatal("启用后台调度的路由应创建 refresher")
	}
	app.refresher.Stop()
}

func TestStatusHandlerIncludesFeeds(t *testing.T) {
	ref := refresher.New(refresher.Config{
		FeedCache:    cache.New(10 * time.Second),
		Notifier:     notifier.New(notifier.Config{}),
		RoutesConfig: map[string]config.RouteConfig{},
	})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	makeStatusHandler(ref).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want %d", w.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("状态响应不是 JSON: %v", err)
	}
	if body["enabled"] != true {
		t.Fatalf("enabled = %v, want true", body["enabled"])
	}
	if _, ok := body["feeds"]; !ok {
		t.Fatal("/status 应包含 feeds 字段")
	}
}

// --- Disabled Feed HTTP 行为测试（迁移自 Python test_app_feed.py） ---

// setupTestRouter 创建用于测试的 chi router，包含 feed handler。
func setupTestRouter(feedHealth *health.FeedHealth, feedCache *cache.TTLCache) *chi.Mux {
	r := chi.NewRouter()
	cfg := &config.Config{
		Scraper: config.ScraperConfig{},
		Routes:  map[string]config.RouteConfig{},
	}
	r.Get("/feed/{route_name}/*", makeTestFeedHandler(feedHealth, feedCache, cfg))
	return r
}

func makeTestFeedHandler(
	feedHealth *health.FeedHealth,
	feedCache *cache.TTLCache,
	cfg *config.Config,
) http.HandlerFunc {
	return makeFeedHandlerWithPipeline(feedHealth, feedCache, pipeline.New(pipeline.Config{
		FeedCache:     feedCache,
		ScraperConfig: cfg.Scraper,
		RoutesConfig:  cfg.Routes,
	}), nil, nil)
}

func disableTestFeed(feedHealth *health.FeedHealth, feedKey string) {
	feedHealth.RecordFailure(feedKey, route.NewHTTPError(http.StatusForbidden, ""))
}

func TestDisabledFeedReturns502(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)
	disableTestFeed(feedHealth, "afdian/author1")

	router := setupTestRouter(feedHealth, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/author1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("被禁用的 feed 应返回 502, 实得 %d", w.Code)
	}
}

func TestSiblingFeedNotBlocked(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)
	disableTestFeed(feedHealth, "afdian/author1")

	// 预填充缓存，避免走真实 fetch
	feedCache.Set(pipeline.CacheKey("afdian", []string{"author2"}, route.FetchOptions{}), "<feed/>")

	router := setupTestRouter(feedHealth, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/author2", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("同路由下未禁用的 feed 应返回 200, 实得 %d", w.Code)
	}
	if w.Body.String() != "<feed/>" {
		t.Errorf("应返回缓存内容, 实得 %q", w.Body.String())
	}
}

func TestFeedCacheHitReturnsXML(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)
	feedCache.Set(pipeline.CacheKey("afdian", []string{"user1"}, route.FetchOptions{}), `<?xml version="1.0"?><feed/>`)

	router := setupTestRouter(feedHealth, feedCache)

	req := httptest.NewRequest("GET", "/feed/afdian/user1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("缓存命中应返回 200, 实得 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
}

func TestFeedHandlerObservesValidCacheHit(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"afdian": {Enabled: true}}}
	feedCache.Set(pipeline.CacheKey("afdian", []string{"user1"}, route.FetchOptions{}), `<?xml version="1.0"?><feed/>`)
	observer := &recordingFeedObserver{}
	pipe := pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.Routes})
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(feedHealth, feedCache, pipe, observer, nil))

	req := httptest.NewRequest("GET", "/feed/afdian/user1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if len(observer.refs) != 1 {
		t.Fatalf("Observe 调用数 = %d, want 1", len(observer.refs))
	}
	wantKey := pipeline.CacheKey("afdian", []string{"user1"}, route.FetchOptions{})
	if observer.refs[0].CacheKey != wantKey {
		t.Fatalf("观察到的 CacheKey = %q, want %q", observer.refs[0].CacheKey, wantKey)
	}
}

func TestFeedHandlerDoesNotObserveUnknownRouteDisabledRouteOrDisabledFeed(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)
	observer := &recordingFeedObserver{}
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"afdian": {Enabled: false}}}
	pipe := pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.Routes})
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(feedHealth, feedCache, pipe, observer, nil))

	req := httptest.NewRequest("GET", "/feed/nonexistent/user1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if len(observer.refs) != 0 {
		t.Fatalf("未知 route 不应 Observe, got %d", len(observer.refs))
	}

	feedCache.Set(pipeline.CacheKey("afdian", []string{"user1"}, route.FetchOptions{}), "<feed/>")
	req = httptest.NewRequest("GET", "/feed/afdian/user1", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if len(observer.refs) != 0 {
		t.Fatalf("禁用 route 不应 Observe, got %d", len(observer.refs))
	}

	cfg.Routes["afdian"] = config.RouteConfig{Enabled: true}
	disableTestFeed(feedHealth, "afdian/user1")
	req = httptest.NewRequest("GET", "/feed/afdian/user1", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if len(observer.refs) != 0 {
		t.Fatalf("禁用 feed 不应 Observe, got %d", len(observer.refs))
	}
}

func TestFeedUnknownRouteReturns404(t *testing.T) {
	feedHealth := health.New(health.Config{})
	feedCache := cache.New(10 * time.Second)

	router := setupTestRouter(feedHealth, feedCache)

	req := httptest.NewRequest("GET", "/feed/nonexistent/user1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("未知路由应返回 404, 实得 %d", w.Code)
	}
}

type mainTestRoute struct {
	name        string
	fetchErr    error
	infoErr     error
	info        *route.FeedInfo
	items       []route.FeedItem
	capturedOpt route.FetchOptions
	fetchCount  atomic.Int32
}

type recordingFeedObserver struct {
	refs []pipeline.FeedRef
}

type writeErrorResponseWriter struct {
	header http.Header
	err    error
}

func (w *writeErrorResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *writeErrorResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func (w *writeErrorResponseWriter) WriteHeader(int) {}

func (o *recordingFeedObserver) Observe(ref pipeline.FeedRef) {
	o.refs = append(o.refs, ref)
}

func (r *mainTestRoute) Name() string { return r.name }
func (r *mainTestRoute) Fetch(_ []string, opts route.FetchOptions) (route.FeedResult, error) {
	r.fetchCount.Add(1)
	r.capturedOpt = opts
	if r.fetchErr != nil {
		return route.FeedResult{}, r.fetchErr
	}
	if r.infoErr != nil {
		return route.FeedResult{}, r.infoErr
	}
	info := route.FeedInfo{Title: "Test", Link: "https://example.com"}
	if r.info != nil {
		info = *r.info
	}
	items := []route.FeedItem{{Title: "Item", Link: "https://example.com/1"}}
	if r.items != nil {
		items = r.items
	}
	return route.FeedResult{Info: info, Items: items}, nil
}

func TestFeedHandlerSyncFetchSuccessAndQueryParams(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_success"}
	route.Register("_main_success", "_main_success", func(config.ResolvedRouteConfig) route.Route { return testRoute })

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_success": {Enabled: true}}}
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeTestFeedHandler(feedHealth, feedCache, cfg))

	req := httptest.NewRequest("GET", "/feed/_main_success/u1?limit=7&include=a,b&format=rss", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", w.Code, w.Body.String())
	}
	if testRoute.capturedOpt.Limit != 7 {
		t.Fatalf("Limit = %d", testRoute.capturedOpt.Limit)
	}
	if len(testRoute.capturedOpt.Include) != 2 || testRoute.capturedOpt.Include[0] != "a" {
		t.Fatalf("Include = %+v", testRoute.capturedOpt.Include)
	}
	cacheKey := pipeline.CacheKey("_main_success", []string{"u1"}, route.FetchOptions{
		Format:  "rss",
		Limit:   7,
		Include: []string{"a", "b"},
	})
	if cached, ok := feedCache.Get(cacheKey); !ok || cached == "" {
		t.Fatal("同步抓取成功后应写入缓存")
	}
}

func TestFeedHandlerCacheVariantsDoNotPollute(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_variant"}
	route.Register("_main_variant", "_main_variant", func(config.ResolvedRouteConfig) route.Route { return testRoute })

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	rssKey := pipeline.CacheKey("_main_variant", []string{"u1"}, route.FetchOptions{Format: "rss"})
	feedCache.Set(rssKey, "<rss/>")

	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_variant": {Enabled: true}}}
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeTestFeedHandler(feedHealth, feedCache, cfg))

	req := httptest.NewRequest("GET", "/feed/_main_variant/u1?format=atom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() == "<rss/>" {
		t.Fatal("atom 请求不应命中 rss 变体缓存")
	}
	if testRoute.fetchCount.Load() != 1 {
		t.Fatalf("atom 变体未命中时应同步抓取一次, got %d", testRoute.fetchCount.Load())
	}
}

func TestFeedHandlerCacheMissDoesNotTriggerBackgroundFetch(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_no_trigger"}
	route.Register("_main_no_trigger", "_main_no_trigger", func(config.ResolvedRouteConfig) route.Route { return testRoute })

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_no_trigger": {Enabled: true}}}
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeTestFeedHandler(feedHealth, feedCache, cfg))

	req := httptest.NewRequest("GET", "/feed/_main_no_trigger/u1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	time.Sleep(50 * time.Millisecond)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", w.Code, w.Body.String())
	}
	if testRoute.fetchCount.Load() != 1 {
		t.Fatalf("缓存未命中时不应额外触发后台抓取, Fetch 调用数 = %d", testRoute.fetchCount.Load())
	}
}

func TestFeedHandlerFetchAndInfoErrors(t *testing.T) {
	t.Run("fetch error", func(t *testing.T) {
		testRoute := &mainTestRoute{name: "_main_fetch_err", fetchErr: errors.New("boom")}
		route.Register("_main_fetch_err", "_main_fetch_err", func(config.ResolvedRouteConfig) route.Route { return testRoute })

		cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_fetch_err": {Enabled: true}}}
		r := chi.NewRouter()
		r.Get("/feed/{route_name}/*", makeTestFeedHandler(health.New(health.Config{}), cache.New(10*time.Second), cfg))

		req := httptest.NewRequest("GET", "/feed/_main_fetch_err/u1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("Fetch 错误应返回 502，got %d", w.Code)
		}
	})

	t.Run("result info error", func(t *testing.T) {
		testRoute := &mainTestRoute{name: "_main_info_err", infoErr: errors.New("bad info")}
		route.Register("_main_info_err", "_main_info_err", func(config.ResolvedRouteConfig) route.Route { return testRoute })

		cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_info_err": {Enabled: true}}}
		r := chi.NewRouter()
		r.Get("/feed/{route_name}/*", makeTestFeedHandler(health.New(health.Config{}), cache.New(10*time.Second), cfg))

		req := httptest.NewRequest("GET", "/feed/_main_info_err/u1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("Fetch 错误应返回 502，got %d", w.Code)
		}
	})
}

func TestFeedHandlerBusinessErrorNotifiesAndDisablesFeed(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_fetch_biz_err", fetchErr: route.NewHTTPError(http.StatusForbidden, "https://upstream.example/api")}
	route.Register("_main_fetch_biz_err", "_main_fetch_biz_err", func(config.ResolvedRouteConfig) route.Route { return testRoute })

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		if _, err := w.Write([]byte(`{"code":0,"msg":"success"}`)); err != nil {
			t.Fatalf("写入响应失败: %v", err)
		}
	}))
	defer server.Close()

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_fetch_biz_err": {Enabled: true}}}
	pipe := pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.Routes})
	notif := notifier.New(notifier.Config{
		Enabled:  true,
		Services: []notifier.ServiceConfig{{Type: "feishu", WebhookURL: server.URL}},
	})
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(feedHealth, feedCache, pipe, nil, notif))

	req := httptest.NewRequest("GET", "/feed/_main_fetch_biz_err/u1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("业务错误应返回 502，got %d", w.Code)
	}
	if !feedHealth.IsFeedDisabled("_main_fetch_biz_err/u1") {
		t.Fatal("同步业务错误后 feed 应被禁用")
	}

	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("同步业务错误应触发通知")
	}
}

func TestFeedHandlerClientDisconnectWriteErrorNotLoggedAsError(t *testing.T) {
	testRoute := &mainTestRoute{name: "_main_write_broken_pipe"}
	route.Register("_main_write_broken_pipe", "_main_write_broken_pipe", func(config.ResolvedRouteConfig) route.Route { return testRoute })

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	feedCache := cache.New(10 * time.Second)
	feedHealth := health.New(health.Config{})
	cfg := &config.Config{Routes: map[string]config.RouteConfig{"_main_write_broken_pipe": {Enabled: true}}}
	pipe := pipeline.New(pipeline.Config{FeedCache: feedCache, RoutesConfig: cfg.Routes})
	r := chi.NewRouter()
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(feedHealth, feedCache, pipe, nil, nil))

	req := httptest.NewRequest("GET", "/feed/_main_write_broken_pipe/u1", nil)
	w := &writeErrorResponseWriter{err: &net.OpError{Op: "write", Err: syscall.EPIPE}}
	r.ServeHTTP(w, req)

	logOutput := logs.String()
	if strings.Contains(logOutput, "level=ERROR") {
		t.Fatalf("客户端断开不应输出 ERROR 日志: %s", logOutput)
	}
	if !strings.Contains(logOutput, "level=WARN") || !strings.Contains(logOutput, "客户端已断开") {
		t.Fatalf("客户端断开应输出 WARN 诊断日志, 实得: %s", logOutput)
	}
}

func TestStatusHandlerDisabled(t *testing.T) {
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	makeStatusHandler(nil).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("状态响应不是 JSON: %v", err)
	}
	if body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", body["enabled"])
	}
}
