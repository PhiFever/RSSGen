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
	"github.com/PhiFever/RSSGen/internal/feed"
	"github.com/PhiFever/RSSGen/internal/notifier"
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

	// 初始化缓存
	feedCache := cache.New(time.Duration(cfg.Cache.FeedTTL) * time.Second)
	articleCache := cache.New(time.Duration(cfg.Cache.ArticleTTL) * time.Second)
	_ = articleCache // 暂未使用

	// 初始化 SQLite 存储
	articleStore := store.New(cfg.Storage.SQLitePath)
	if err := articleStore.Init(); err != nil {
		slog.Error("初始化存储失败", "error", err)
		os.Exit(1)
	}
	defer articleStore.Close()

	// 初始化通知器
	notif := notifier.New(notifier.Config{
		Enabled:     cfg.Notifier.Enabled,
		ServiceURLs: cfg.Notifier.ServiceURLs,
	})

	// 初始化后台刷新器
	var ref *refresher.Refresher
	if anyRouteEnabled(cfg) {
		ref = refresher.New(refresher.Config{
			FeedCache:      feedCache,
			ArticleStore:   articleStore,
			Notifier:       notif,
			StartupDelay:   cfg.Refresher.StartupDelay,
			MaxRetries:     cfg.Refresher.MaxRetries,
			RetryBaseDelay: cfg.Refresher.RetryBaseDelay,
			ScraperConfig:  cfg.Scraper,
			RoutesConfig:   cfg.Routes,
		})
		ref.Start()
		slog.Info("后台刷新器已启动")
	}

	// 创建 chi 路由
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// 首页：列出所有路由
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		registry := route.GetRegistry()
		result := make(map[string]string)
		for name, factory := range registry {
			// 创建临时实例获取 description
			route := factory(config.ResolvedRouteConfig{})
			result[name] = route.Description()
		}
		w.Header().Set("Content-Type", "application/json")
		routesJSON, _ := json.Marshal(result)
		fmt.Fprintf(w, `{"name":"RSSGen","routes":%s}`, routesJSON)
	})

	// Feed 路由：GET /feed/{route_name}/*
	r.Get("/feed/{route_name}/*", func(w http.ResponseWriter, r *http.Request) {
		routeName := chi.URLParam(r, "route_name")
		path := chi.URLParam(r, "*")
		pathParts := splitPath(path)

		registry := route.GetRegistry()
		factory, ok := registry[routeName]
		if !ok {
			http.Error(w, fmt.Sprintf("路由不存在: %s", routeName), http.StatusNotFound)
			return
		}

		cacheKey := buildCacheKey(routeName, pathParts)

		// 检查 feed 是否被禁用
		if notif.IsFeedDisabled(cacheKey) {
			http.Error(w, fmt.Sprintf("订阅源 %s 已禁用（业务错误），重启后恢复", cacheKey), http.StatusBadGateway)
			return
		}

		// 检查缓存
		if cached, ok := feedCache.Get(cacheKey); ok {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Write([]byte(cached))
			return
		}

		// 触发后台刷新（非阻塞）
		if ref != nil {
			queryParams := make(map[string]string)
			for k, v := range r.URL.Query() {
				if len(v) > 0 {
					queryParams[k] = v[0]
				}
			}
			ref.Trigger(routeName, pathParts, queryParams)
		}

		// 同步抓取
		routeInst := factory(config.ResolveRoute(cfg.Scraper, cfg.Routes[routeName]))

		// 解析请求参数
		opts := route.FetchOptions{
			Limit: 20,
		}
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			fmt.Sscanf(limitStr, "%d", &opts.Limit)
		}
		if includeStr := r.URL.Query().Get("include"); includeStr != "" {
			opts.Include = strings.Split(includeStr, ",")
		}

		items, err := routeInst.Fetch(articleStore, pathParts, opts)
		if err != nil {
			slog.Error("路由抓取失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("抓取失败: %v", err), http.StatusBadGateway)
			return
		}

		info, err := routeInst.FeedInfo(pathParts)
		if err != nil {
			slog.Error("获取 feed 信息失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("获取 feed 信息失败: %v", err), http.StatusInternalServerError)
			return
		}

		format := r.URL.Query().Get("format")
		if format == "" {
			format = "atom"
		}

		xml, err := feed.Generate(info, items, format)
		if err != nil {
			slog.Error("生成 feed 失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("生成 feed 失败: %v", err), http.StatusInternalServerError)
			return
		}

		// 写入缓存
		feedCache.Set(cacheKey, xml)

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Write([]byte(xml))
	})

	// 状态接口
	r.Get("/status", func(w http.ResponseWriter, r *http.Request) {
		if ref == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"enabled":false,"message":"后台刷新未启用"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"enabled":true}`)
	})

	// 启动 HTTP 服务
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// 优雅关停
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("RSSGen 启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务异常", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("收到关停信号，正在关闭服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ref != nil {
		ref.Stop()
	}
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("服务关闭异常", "error", err)
	}
	slog.Info("RSSGen 已停止")
}

// anyRouteEnabled 检查是否有任何路由启用了定时刷新。
func anyRouteEnabled(cfg *config.Config) bool {
	for _, rc := range cfg.Routes {
		if rc.Enabled && rc.RefreshInterval > 0 {
			return true
		}
	}
	return false
}

// buildCacheKey 构建缓存键。
func buildCacheKey(routeName string, pathParts []string) string {
	return routeName + "/" + strings.Join(pathParts, "/")
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
