package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/feedcatalog"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/miniflux"
	"github.com/PhiFever/RSSGen/internal/notifier"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/refresher"
	"github.com/PhiFever/RSSGen/internal/route"

	// 注册路由（init 自动注册）
	"github.com/PhiFever/RSSGen/internal/route/afdian"
	_ "github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func main() {
	// 配置结构化日志
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	if len(os.Args) > 1 {
		if os.Args[1] != "afdian-backfill" {
			slog.Error("未知命令", "command", os.Args[1])
			os.Exit(2)
		}
		if err := loadBackfillEnv(".env"); err != nil {
			slog.Error("加载 backfill 环境变量失败", "error", err)
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runAfdianBackfill(ctx, os.Args[2:], os.Getenv, os.Stdout); err != nil {
			slog.Error("Afdian 历史回填失败", "error", err)
			os.Exit(1)
		}
		return
	}

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

// loadBackfillEnv 加载可选的 dotenv 文件，进程环境变量保持更高优先级。
func loadBackfillEnv(filename string) error {
	if err := godotenv.Load(filename); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("加载 %s: %w", filename, err)
	}
	return nil
}

type runtimeApp struct {
	server    *http.Server
	refresher *refresher.Refresher
}

type feedObserver interface {
	Observe(ref pipeline.FeedRef)
}

type feedNotifier interface {
	Notify(feedKey string, statusCode int, errorMessage string)
}

func buildRuntime(cfg *config.Config) (*runtimeApp, error) {
	feedCache := cache.New(time.Duration(cfg.Cache.FeedTTL) * time.Second)

	var notifierServices []notifier.ServiceConfig
	for _, svc := range cfg.Notifier.Services {
		notifierServices = append(notifierServices, notifier.ServiceConfig{
			Type:       svc.Type,
			WebhookURL: svc.WebhookURL,
			Secret:     svc.Secret,
		})
	}
	notif := notifier.New(notifier.Config{
		Enabled:     cfg.Notifier.Enabled,
		ServiceURLs: cfg.Notifier.ServiceURLs,
		Services:    notifierServices,
	})
	feedHealth := health.New(health.Config{
		BusinessErrorCodes: cfg.Notifier.BusinessErrorCodes,
	})

	pipe := pipeline.New(pipeline.Config{
		FeedCache:     feedCache,
		ScraperConfig: cfg.Scraper,
		RoutesConfig:  cfg.Routes,
	})
	dynamicFeedLimit := config.DefaultDynamicFeedLimit
	if cfg.Refresher.DynamicFeedLimit != nil {
		dynamicFeedLimit = *cfg.Refresher.DynamicFeedLimit
	}
	feedCatalog := feedcatalog.New(dynamicFeedLimit)

	var ref *refresher.Refresher
	if anyRouteEnabled(cfg) {
		ref = refresher.New(refresher.Config{
			FeedCache:      feedCache,
			Notifier:       notif,
			FeedHealth:     feedHealth,
			Pipeline:       pipe,
			FeedCatalog:    feedCatalog,
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

	router := makeRouter(feedHealth, feedCache, ref, cfg, pipe, feedCatalog, notif)
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
				logResponseWriteError("写入状态响应失败", err)
			}
			return
		}
		resp := map[string]any{
			"enabled": true,
			"feeds":   ref.GetStatus(),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			logResponseWriteError("编码状态响应失败", err)
		}
	}
}

func makeRouter(
	feedHealth *health.FeedHealth,
	feedCache *cache.TTLCache,
	ref *refresher.Refresher,
	cfg *config.Config,
	pipe *pipeline.Pipeline,
	feedCatalog feedObserver,
	feedNotifier feedNotifier,
) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// 不接入已弃用的 RealIP；当前服务不基于客户端 IP 做信任决策。

	// 首页：列出所有路由
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		result := route.GetDescriptions()
		w.Header().Set("Content-Type", "application/json")
		routesJSON, err := json.Marshal(result)
		if err != nil {
			slog.Error("编码路由列表失败", "error", err)
			http.Error(w, "编码路由列表失败", http.StatusInternalServerError)
			return
		}
		if _, err := fmt.Fprintf(w, `{"name":"RSSGen","routes":%s}`, routesJSON); err != nil {
			logResponseWriteError("写入首页响应失败", err)
		}
	})

	// Feed 路由：GET /feed/{route_name}/*
	r.Get("/feed/{route_name}/*", makeFeedHandlerWithPipeline(feedHealth, feedCache, pipe, feedCatalog, feedNotifier))

	// 状态接口
	r.Get("/status", makeStatusHandler(ref))

	return r
}

type afdianBackfillCLIConfig struct {
	action          backfill.Action
	minifluxURL     string
	feedID          int64
	requestInterval time.Duration
	minifluxToken   string
	afdianCookie    string
}

func parseAfdianBackfillArgs(args []string, getenv func(string) string) (afdianBackfillCLIConfig, error) {
	fs := flag.NewFlagSet("afdian-backfill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cfg afdianBackfillCLIConfig
	var listFeeds bool
	var dryRun bool
	fs.StringVar(&cfg.minifluxURL, "miniflux-url", "", "Miniflux instance root URL")
	fs.Int64Var(&cfg.feedID, "feed-id", 0, "existing Miniflux feed ID")
	fs.BoolVar(&listFeeds, "list-feeds", false, "list eligible Afdian feeds")
	fs.BoolVar(&dryRun, "dry-run", false, "discover and reconcile without importing")
	fs.DurationVar(&cfg.requestInterval, "request-interval", time.Second, "minimum interval between Afdian requests")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() != 0 {
		return cfg, fmt.Errorf("不接受位置参数: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(cfg.minifluxURL) == "" {
		return cfg, fmt.Errorf("必须显式传入 --miniflux-url")
	}
	if cfg.requestInterval < time.Second {
		return cfg, fmt.Errorf("--request-interval 不能小于 1s")
	}
	if listFeeds && dryRun {
		return cfg, fmt.Errorf("--list-feeds 与 --dry-run 不能同时使用")
	}
	if listFeeds {
		if cfg.feedID != 0 {
			return cfg, fmt.Errorf("--list-feeds 不接受 --feed-id")
		}
		cfg.action = backfill.ActionList
	} else {
		if cfg.feedID <= 0 {
			return cfg, fmt.Errorf("必须传入正整数 --feed-id")
		}
		if dryRun {
			cfg.action = backfill.ActionDryRun
		} else {
			cfg.action = backfill.ActionExecute
		}
	}
	cfg.minifluxToken = strings.TrimSpace(getenv("MINIFLUX_API_TOKEN"))
	if cfg.minifluxToken == "" {
		return cfg, fmt.Errorf("必须设置 MINIFLUX_API_TOKEN")
	}
	if cfg.action != backfill.ActionList {
		cfg.afdianCookie = strings.TrimSpace(getenv("AFDIAN_COOKIE"))
		if cfg.afdianCookie == "" {
			return cfg, fmt.Errorf("必须设置 AFDIAN_COOKIE")
		}
	}
	return cfg, nil
}

func runAfdianBackfill(ctx context.Context, args []string, getenv func(string) string, output io.Writer) error {
	cfg, err := parseAfdianBackfillArgs(args, getenv)
	if err != nil {
		return err
	}
	destination, err := miniflux.New(cfg.minifluxURL, cfg.minifluxToken)
	if err != nil {
		return err
	}
	source, err := afdian.NewBackfillSource(cfg.afdianCookie, cfg.requestInterval)
	if err != nil {
		return err
	}
	deps := backfill.Dependencies{Source: source, Destination: destination}
	slog.Info("开始 Afdian 历史回填", "action", cfg.action, "feed_id", cfg.feedID)
	result, runErr := backfill.Run(ctx, backfill.Request{Action: cfg.action, FeedID: cfg.feedID}, deps)
	if runErr == nil || result.Feed.ID != 0 || result.Scanned != 0 || result.Imported != 0 {
		printBackfillResult(output, cfg.action, result)
	}
	return runErr
}

func printBackfillResult(output io.Writer, action backfill.Action, result backfill.Result) {
	if action == backfill.ActionList {
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "ID\tTITLE\tFEED_URL")
		for _, feed := range result.Feeds {
			_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\n", feed.ID, feed.Title, feed.FeedURL)
		}
		_ = writer.Flush()
		return
	}
	_, _ = fmt.Fprintf(output, "feed_id=%d author_slug=%s scanned=%d existing=%d missing=%d duplicates=%d imported=%d duplicate_imports=%d comment_failures=%d\n",
		result.Feed.ID, result.SourceID, result.Scanned, result.Existing, result.Missing,
		result.DuplicateCandidates, result.Imported, result.DuplicateImports, result.CommentFailures)
	if result.MissingOldest != nil && result.MissingNewest != nil {
		_, _ = fmt.Fprintf(output, "missing_range=%s..%s\n",
			result.MissingOldest.UTC().Format(time.RFC3339), result.MissingNewest.UTC().Format(time.RFC3339))
	}
	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(output, "warning post_id=%s operation=%s error=%v\n", warning.CandidateID, warning.Operation, warning.Err)
	}
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

func makeFeedHandlerWithPipeline(
	feedHealth *health.FeedHealth,
	feedCache *cache.TTLCache,
	pipe *pipeline.Pipeline,
	feedCatalog feedObserver,
	feedNotifier feedNotifier,
) http.HandlerFunc {
	if feedHealth == nil {
		feedHealth = health.New(health.Config{})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		routeName := chi.URLParam(r, "route_name")
		path := chi.URLParam(r, "*")
		pathParts := splitPath(path)

		registry := route.GetRegistry()
		if _, ok := registry[routeName]; !ok {
			http.Error(w, fmt.Sprintf("路由不存在: %s", routeName), http.StatusNotFound)
			return
		}

		opts := pipeline.OptionsFromQuery(r.URL.Query())
		ref := pipe.FeedRef(routeName, pathParts, opts)

		// 检查 feed 是否被禁用
		if feedHealth.IsFeedDisabled(ref.HealthKey) {
			http.Error(w, fmt.Sprintf("订阅源 %s 已禁用（业务错误），重启后恢复", ref.HealthKey), http.StatusBadGateway)
			return
		}
		if feedCatalog != nil && pipe.RouteAllowsDynamicObservation(routeName) {
			feedCatalog.Observe(ref)
		}

		// 检查缓存
		if cached, ok := feedCache.Get(ref.CacheKey); ok {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			if _, err := w.Write([]byte(cached)); err != nil {
				logResponseWriteError("写入缓存 feed 响应失败", err, "route", routeName)
			}
			return
		}

		result, err := pipe.Refresh(routeName, pathParts, opts)
		if err != nil {
			statusCode, justDisabled := feedHealth.RecordFailure(ref.HealthKey, err)
			if justDisabled {
				if feedNotifier != nil {
					feedNotifier.Notify(ref.HealthKey, statusCode, fmt.Sprintf("%v", err))
				}
				slog.Warn("feed 已禁用（业务错误）", "key", ref.HealthKey, "status", statusCode)
			}
			slog.Error("路由抓取失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("抓取失败: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		if _, err := w.Write([]byte(result.XML)); err != nil {
			logResponseWriteError("写入 feed 响应失败", err, "route", routeName)
		}
	}
}

func logResponseWriteError(message string, err error, attrs ...any) {
	attrs = append(attrs, "error", err)
	if isClientDisconnectError(err) {
		slog.Warn(message+"（客户端已断开）", attrs...)
		return
	}
	slog.Error(message, attrs...)
}

func isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "client disconnected") ||
		strings.Contains(msg, "use of closed network connection")
}
