package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/feedcatalog"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/media"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/refresher"
)

func runServer(ctx context.Context) error {
	cfg, err := config.Load("config.yml")
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	app, err := buildRuntime(cfg)
	if err != nil {
		return fmt.Errorf("初始化运行时: %w", err)
	}
	return serveRuntime(ctx, app)
}

func serveRuntime(ctx context.Context, app *runtimeApp) error {
	refresherStopped := false
	stopRefresher := func() {
		if !refresherStopped && app.refresher != nil {
			app.refresher.Stop()
			refresherStopped = true
		}
	}
	defer stopRefresher()

	listener, err := net.Listen("tcp", app.server.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", app.server.Addr, err)
	}
	slog.Info("RSSGen 启动", "addr", listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- app.server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常: %w", err)
		}
		return nil
	case <-ctx.Done():
		slog.Info("收到关停信号，正在关闭服务...")
	}

	stopRefresher()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("关闭 HTTP 服务: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP 服务异常: %w", err)
	}
	slog.Info("RSSGen 已停止")
	return nil
}

type runtimeApp struct {
	server    *http.Server
	refresher *refresher.Refresher
}

func buildRuntime(cfg *config.Config) (*runtimeApp, error) {
	feedCache := cache.New(time.Duration(cfg.Cache.FeedTTL) * time.Second)

	notif, err := notifier.New(notifier.Config{
		Enabled:     cfg.Notifier.Enabled,
		ServiceURLs: cfg.Notifier.ServiceURLs,
	})
	if err != nil {
		return nil, fmt.Errorf("创建通知器: %w", err)
	}
	feedHealth := health.New(health.Config{
		BusinessErrorCodes: cfg.Notifier.BusinessErrorCodes,
		OnDisabled:         notif.Notify,
	})

	var archiver *media.Archiver
	if cfg.Media != nil {
		archiver = media.New(*cfg.Media)
	}
	pipe := pipeline.New(pipeline.Config{
		FeedCache:     feedCache,
		ScraperConfig: cfg.Scraper,
		RoutesConfig:  cfg.Routes,
		Media:         archiver,
	})
	dynamicFeedLimit := config.DefaultDynamicFeedLimit
	if cfg.Refresher.DynamicFeedLimit != nil {
		dynamicFeedLimit = *cfg.Refresher.DynamicFeedLimit
	}
	feedCatalog := feedcatalog.New(dynamicFeedLimit)

	var ref *refresher.Refresher
	if anyRouteEnabled(cfg) {
		ref = refresher.New(refresher.Config{
			FeedHealth:     feedHealth,
			Pipeline:       pipe,
			FeedCatalog:    feedCatalog,
			StartupDelay:   cfg.Refresher.StartupDelay,
			MaxRetries:     cfg.Refresher.MaxRetries,
			RetryBaseDelay: cfg.Refresher.RetryBaseDelay,
			PreinitURL:     cfg.Refresher.PreinitURL,
			RoutesConfig:   cfg.Routes,
		})
		ref.Start()
		slog.Info("后台刷新器已启动")
	}

	router := makeRouter(feedHealth, feedCache, ref, pipe, feedCatalog, archiver)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	return &runtimeApp{
		server: &http.Server{
			Addr:    addr,
			Handler: router,
		},
		refresher: ref,
	}, nil
}

// anyRouteEnabled 检查是否有任何路由需要后台调度。
func anyRouteEnabled(cfg *config.Config) bool {
	for _, rc := range cfg.Routes {
		if rc.NeedsScheduling() {
			return true
		}
	}
	return false
}
