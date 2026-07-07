package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/refresher"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/store"

	// 注册路由（init 自动注册）
	_ "github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func main() {
	// 配置结构化日志
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 加载配置
	cfg, err := config.Load("config.yml")
	if err != nil {
		slog.Error("加载配置失败", "error", err)
		os.Exit(1)
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		slog.Error("初始化运行时失败", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := app.articleStore.Close(); err != nil {
			slog.Error("关闭文章存储失败", "error", err)
		}
	}()

	// 优雅关停
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("RSSGen 启动", "addr", app.server.Addr)
		if err := app.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务异常", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("收到关停信号，正在关闭服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if app.refresher != nil {
		app.refresher.Stop()
	}
	if err := app.server.Shutdown(ctx); err != nil {
		slog.Error("服务关闭异常", "error", err)
	}
	slog.Info("RSSGen 已停止")
}

type runtimeApp struct {
	server       *http.Server
	refresher    *refresher.Refresher
	articleStore *store.ArticleStore
}

func buildRuntime(cfg *config.Config) (*runtimeApp, error) {
	feedCache := cache.New(time.Duration(cfg.Cache.FeedTTL) * time.Second)

	articleStore := store.New(cfg.Storage.SQLitePath)
	if err := articleStore.Init(); err != nil {
		return nil, fmt.Errorf("初始化存储失败: %w", err)
	}

	var notifierServices []notifier.ServiceConfig
	for _, svc := range cfg.Notifier.Services {
		notifierServices = append(notifierServices, notifier.ServiceConfig{
			Type:       svc.Type,
			WebhookURL: svc.WebhookURL,
			Secret:     svc.Secret,
		})
	}
	notif := notifier.New(notifier.Config{
		Enabled:            cfg.Notifier.Enabled,
		ServiceURLs:        cfg.Notifier.ServiceURLs,
		BusinessErrorCodes: cfg.Notifier.BusinessErrorCodes,
		Services:           notifierServices,
	})

	pipe := pipeline.New(pipeline.Config{
		FeedCache:     feedCache,
		ArticleStore:  articleStore,
		ScraperConfig: cfg.Scraper,
		RoutesConfig:  cfg.Routes,
	})

	var ref *refresher.Refresher
	if anyRouteEnabled(cfg) {
		ref = refresher.New(refresher.Config{
			FeedCache:      feedCache,
			ArticleStore:   articleStore,
			Notifier:       notif,
			Pipeline:       pipe,
			StartupDelay:   cfg.Refresher.StartupDelay,
			MaxRetries:     cfg.Refresher.MaxRetries,
			RetryBaseDelay: cfg.Refresher.RetryBaseDelay,
			PreinitURL:     cfg.Refresher.PreinitURL,
			ScraperConfig:  cfg.Scraper,
			RoutesConfig:   cfg.Routes,
		})
		ref.Start()
		slog.Info("后台刷新器已启动")
	}

	router := makeRouter(notif, feedCache, ref, articleStore, cfg, pipe)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	return &runtimeApp{
		server: &http.Server{
			Addr:    addr,
			Handler: router,
		},
		refresher:    ref,
		articleStore: articleStore,
	}, nil
}

// anyRouteEnabled 检查是否有任何路由需要后台调度。
func anyRouteEnabled(cfg *config.Config) bool {
	for _, rc := range cfg.Routes {
		if rc.Enabled && (rc.RefreshInterval > 0 || rc.PreheatOnStartup) {
			return true
		}
	}
	return false
}

func makeStatusHandler(ref *refresher.Refresher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ref == nil {
			if _, err := w.Write([]byte(`{"enabled":false,"message":"后台刷新未启用"}`)); err != nil {
				slog.Error("写入状态响应失败", "error", err)
			}
			return
		}
		resp := map[string]any{
			"enabled": true,
			"feeds":   ref.GetStatus(),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Error("编码状态响应失败", "error", err)
		}
	}
}

func makeRouter(
	notif *notifier.Notifier,
	feedCache *cache.TTLCache,
	ref *refresher.Refresher,
	articleStore *store.ArticleStore,
	cfg *config.Config,
	pipe *pipeline.Pipeline,
) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// 不接入已弃用的 RealIP；当前服务不基于客户端 IP 做信任决策。

	// 首页：列出所有路由
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		registry := route.GetRegistry()
		result := make(map[string]string)
		for name, factory := range registry {
			routeInst := factory(config.ResolvedRouteConfig{})
			result[name] = routeInst.Description()
		}
		w.Header().Set("Content-Type", "application/json")
		routesJSON, err := json.Marshal(result)
		if err != nil {
			slog.Error("编码路由列表失败", "error", err)
			http.Error(w, "编码路由列表失败", http.StatusInternalServerError)
			return
		}
		if _, err := fmt.Fprintf(w, `{"name":"RSSGen","routes":%s}`, routesJSON); err != nil {
			slog.Error("写入首页响应失败", "error", err)
		}
	})

	// Feed 路由：GET /feed/{route_name}/*
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(notif, feedCache, pipe))

	// 状态接口
	r.Get("/status", makeStatusHandler(ref))

	return r
}

// splitPath 将路径分割为非空段。
func splitPath(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var result []string
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// makeFeedHandler 创建 feed HTTP handler，便于测试。
func makeFeedHandler(
	notif *notifier.Notifier,
	feedCache *cache.TTLCache,
	articleStore *store.ArticleStore,
	cfg *config.Config,
) http.HandlerFunc {
	return makeFeedHandlerWithPipeline(notif, feedCache, pipeline.New(pipeline.Config{
		FeedCache:     feedCache,
		ArticleStore:  articleStore,
		ScraperConfig: cfg.Scraper,
		RoutesConfig:  cfg.Routes,
	}))
}

func makeFeedHandlerWithPipeline(
	notif *notifier.Notifier,
	feedCache *cache.TTLCache,
	pipe *pipeline.Pipeline,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeName := chi.URLParam(r, "route_name")
		path := chi.URLParam(r, "*")
		pathParts := splitPath(path)

		registry := route.GetRegistry()
		if _, ok := registry[routeName]; !ok {
			http.Error(w, fmt.Sprintf("路由不存在: %s", routeName), http.StatusNotFound)
			return
		}

		feedKey := cache.BuildCacheKey(routeName, pathParts)
		opts := pipeline.OptionsFromQuery(r.URL.Query())
		cacheKey := pipe.CacheKey(routeName, pathParts, opts)

		// 检查 feed 是否被禁用
		if notif.IsFeedDisabled(feedKey) {
			http.Error(w, fmt.Sprintf("订阅源 %s 已禁用（业务错误），重启后恢复", feedKey), http.StatusBadGateway)
			return
		}

		// 检查缓存
		if cached, ok := feedCache.Get(cacheKey); ok {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			if _, err := w.Write([]byte(cached)); err != nil {
				slog.Error("写入缓存 feed 响应失败", "route", routeName, "error", err)
			}
			return
		}

		result, err := pipe.Refresh(routeName, pathParts, opts)
		if err != nil {
			slog.Error("路由抓取失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("抓取失败: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		if _, err := w.Write([]byte(result.XML)); err != nil {
			slog.Error("写入 feed 响应失败", "route", routeName, "error", err)
		}
	}
}
