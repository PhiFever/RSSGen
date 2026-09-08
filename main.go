package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/urfave/cli/v3"

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
	"github.com/PhiFever/RSSGen/internal/route/zhihu"
)

func main() {
	os.Exit(runCLI(os.Args))
}

type commandDependencies struct {
	runServer       func(context.Context) error
	runBackfill     func(context.Context, backfillCLIConfig, io.Writer) error
	loadBackfillEnv func(string) error
	getenv          func(string) string
}

func runCLI(args []string) int {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cmd := newCommand(commandDependencies{
		runServer:       runServer,
		runBackfill:     runBackfill,
		loadBackfillEnv: loadBackfillEnv,
		getenv:          os.Getenv,
	})
	if err := cmd.Run(ctx, args); err != nil {
		slog.Error("命令执行失败", "error", err)
		return commandExitCode(err)
	}
	return 0
}

func commandExitCode(err error) int {
	var exitCoder cli.ExitCoder
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	return 1
}

// helpSectionTitles 把 urfave/cli 帮助模板里的英文段落标题换成中文。
// 用替换而不是整段复制模板：复制会让副本在升级 cli 时悄悄落后于上游的模板改动，
// 替换只动标题词，其余模板逻辑继续跟随上游。"GLOBAL OPTIONS:" 必须先于 "OPTIONS:" 列出。
var helpSectionTitles = strings.NewReplacer(
	"GLOBAL OPTIONS:", "全局选项：",
	"OPTIONS:", "选项：",
	"NAME:", "名称：",
	"USAGE:", "用法：",
	"VERSION:", "版本：",
	"DESCRIPTION:", "说明：",
	"COMMANDS:", "命令：",
	"CATEGORY:", "分类：",
	"COPYRIGHT:", "版权：",
)

// localizeHelp 汉化 urfave/cli 自带的帮助文案。替换是幂等的，重复调用无副作用。
func localizeHelp() {
	// HideDefault 和 Local 沿用上游默认值，否则会多出 "(default: false)" 并传播到子命令。
	cli.HelpFlag = &cli.BoolFlag{
		Name:        "help",
		Aliases:     []string{"h"},
		Usage:       "显示帮助",
		HideDefault: true,
		Local:       true,
	}
	cli.UsageCommandHelp = "列出全部命令，或查看某个命令的帮助"
	cli.ArgsUsageCommandHelp = "[命令]"
	cli.RootCommandHelpTemplate = helpSectionTitles.Replace(cli.RootCommandHelpTemplate)
	cli.CommandHelpTemplate = helpSectionTitles.Replace(cli.CommandHelpTemplate)
	cli.SubcommandHelpTemplate = helpSectionTitles.Replace(cli.SubcommandHelpTemplate)
}

func newCommand(deps commandDependencies) *cli.Command {
	localizeHelp()
	return &cli.Command{
		Name:      "rssgen",
		Usage:     "将网站内容转换为 RSS/Atom，并提供历史回填工具",
		UsageText: "rssgen [全局选项] [命令 [命令选项]]",
		Suggest:   true,
		Commands: []*cli.Command{
			{
				Name:      "server",
				Usage:     "启动 RSS/Atom HTTP 服务",
				UsageText: "rssgen server [选项]",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if err := rejectPositionalArgs(cmd); err != nil {
						return err
					}
					return deps.runServer(ctx)
				},
			},
			newBackfillCommand(deps),
		},
		// cli.Exit 默认会直接调用 os.Exit；交给 main 统一记录错误并决定退出码。
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
}

type backfillSourceSpec struct {
	cookieEnv string
	ownFlags  []string
	newSource func(cfg backfillCLIConfig) (backfill.Source, error)
}

var backfillSources = map[string]backfillSourceSpec{
	"afdian": {
		cookieEnv: "AFDIAN_COOKIE",
		newSource: func(cfg backfillCLIConfig) (backfill.Source, error) {
			return afdian.NewBackfillSource(cfg.sourceCookie, cfg.requestInterval)
		},
	},
	"zhihu": {
		cookieEnv: "ZHIHU_COOKIE",
		ownFlags:  []string{"include", "include-self-interaction", "max-items", "start-before"},
		newSource: func(cfg backfillCLIConfig) (backfill.Source, error) {
			return zhihu.NewBackfillSource(cfg.sourceCookie, cfg.requestInterval, cfg.includeSelfInteraction, cfg.maxItems, cfg.startBefore)
		},
	},
}

func backfillSourceNames() string {
	names := make([]string, 0, len(backfillSources))
	for name := range backfillSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func newBackfillCommand(deps commandDependencies) *cli.Command {
	return &cli.Command{
		Name:      "backfill",
		Usage:     "将数据源完整历史回填到已有 Miniflux feed",
		UsageText: "rssgen backfill [选项]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "source",
				Usage:    "数据源（可选：" + backfillSourceNames() + "）",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "miniflux-url",
				Usage:    "Miniflux 实例的根 URL",
				Required: true,
			},
			&cli.Int64Flag{
				Name:  "feed-id",
				Usage: "目标 Miniflux feed 的 ID",
			},
			&cli.BoolFlag{
				Name:  "list-feeds",
				Usage: "列出该数据源下可回填的 feed",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "只扫描并对账，不写入 Miniflux",
			},
			&cli.StringFlag{
				Name: "include",
				Usage: "覆盖 feed URL 中 include 的类别，逗号分隔（仅知乎）；可选值：" +
					strings.Join(zhihu.KnownBackfillCategories(), ", "),
			},
			&cli.BoolFlag{
				Name:  "include-self-interaction",
				Usage: "保留作者对自己内容的赞同和收藏（仅知乎）",
			},
			&cli.StringFlag{
				Name:  "start-before",
				Usage: "起始毫秒游标或日期（YYYY-MM-DD / RFC3339，仅知乎）",
			},
			&cli.IntFlag{
				Name:  "max-items",
				Usage: "最多扫描的动态条数，0 表示不限（仅知乎）",
				Value: defaultBackfillMaxItems,
				Validator: func(value int) error {
					if value < 0 {
						return fmt.Errorf("--max-items 不能为负数")
					}
					return nil
				},
			},
			&cli.DurationFlag{
				Name:  "request-interval",
				Usage: "数据源请求之间的最小间隔",
				Value: time.Second,
				Validator: func(value time.Duration) error {
					if value < time.Second {
						return fmt.Errorf("--request-interval 不能小于 1s")
					}
					return nil
				},
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := rejectPositionalArgs(cmd); err != nil {
				return err
			}
			if err := deps.loadBackfillEnv(".env"); err != nil {
				return fmt.Errorf("加载 backfill 环境变量: %w", err)
			}
			cfg, err := backfillConfigFromCommand(cmd, deps.getenv)
			if err != nil {
				return err
			}
			return deps.runBackfill(ctx, cfg, cmd.Root().Writer)
		},
	}
}

func rejectPositionalArgs(cmd *cli.Command) error {
	if cmd.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("不接受位置参数: %s", strings.Join(cmd.Args().Slice(), " "))
}

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

// defaultBackfillMaxItems 限制单次历史扫描的动态条数。知乎动态流不会自然收敛
// （实测 7674 条仍未出现 is_end），无上限的回填等于上千次上游请求。
const defaultBackfillMaxItems = 1000

type backfillCLIConfig struct {
	includeCategories      []string
	includeSelfInteraction bool
	maxItems               int
	startBefore            int64
	source                 string
	action                 backfill.Action
	minifluxURL            string
	feedID                 int64
	requestInterval        time.Duration
	minifluxToken          string
	sourceCookie           string
}

func backfillConfigFromCommand(cmd *cli.Command, getenv func(string) string) (backfillCLIConfig, error) {
	cfg := backfillCLIConfig{
		includeCategories:      backfill.ParseCategories(cmd.String("include")),
		includeSelfInteraction: cmd.Bool("include-self-interaction"),
		maxItems:               cmd.Int("max-items"),
		source:                 cmd.String("source"),
		minifluxURL:            strings.TrimSpace(cmd.String("miniflux-url")),
		feedID:                 cmd.Int64("feed-id"),
		requestInterval:        cmd.Duration("request-interval"),
	}
	spec, ok := backfillSources[cfg.source]
	if !ok {
		return cfg, fmt.Errorf("未知数据源 %q，可用值: %s", cfg.source, backfillSourceNames())
	}
	for name, other := range backfillSources {
		if name == cfg.source {
			continue
		}
		for _, flag := range other.ownFlags {
			if cmd.IsSet(flag) {
				return cfg, fmt.Errorf("--%s 只适用于数据源 %s", flag, name)
			}
		}
	}
	if cfg.minifluxURL == "" {
		return cfg, fmt.Errorf("必须显式传入 --miniflux-url")
	}
	if cfg.requestInterval < time.Second {
		return cfg, fmt.Errorf("--request-interval 不能小于 1s")
	}
	listFeeds := cmd.Bool("list-feeds")
	dryRun := cmd.Bool("dry-run")
	if listFeeds && dryRun {
		return cfg, fmt.Errorf("--list-feeds 与 --dry-run 不能同时使用")
	}
	if listFeeds {
		if cmd.IsSet("feed-id") {
			return cfg, fmt.Errorf("--list-feeds 不接受 --feed-id")
		}
		// --list-feeds 只枚举 Miniflux feed，不扫描数据源历史，数据源专属过滤参数在此无效。
		for _, flag := range spec.ownFlags {
			if cmd.IsSet(flag) {
				return cfg, fmt.Errorf("--list-feeds 不接受 --%s", flag)
			}
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
	if cmd.IsSet("start-before") {
		var err error
		cfg.startBefore, err = zhihu.ParseStartBefore(cmd.String("start-before"))
		if err != nil {
			return cfg, err
		}
	}
	cfg.minifluxToken = strings.TrimSpace(getenv("MINIFLUX_API_TOKEN"))
	if cfg.minifluxToken == "" {
		return cfg, fmt.Errorf("必须设置 MINIFLUX_API_TOKEN")
	}
	if cfg.action != backfill.ActionList {
		cfg.sourceCookie = strings.TrimSpace(getenv(spec.cookieEnv))
		if cfg.sourceCookie == "" {
			return cfg, fmt.Errorf("必须设置 %s", spec.cookieEnv)
		}
	}
	return cfg, nil
}

func runBackfill(ctx context.Context, cfg backfillCLIConfig, output io.Writer) error {
	destination, err := miniflux.New(cfg.minifluxURL, cfg.minifluxToken)
	if err != nil {
		return err
	}
	spec, ok := backfillSources[cfg.source]
	if !ok {
		return fmt.Errorf("未知数据源 %q，可用值: %s", cfg.source, backfillSourceNames())
	}
	source, err := spec.newSource(cfg)
	if err != nil {
		return err
	}
	deps := backfill.Dependencies{Source: source, Destination: destination, Progress: slog.Info}
	slog.Info("开始历史回填", "source", cfg.source, "action", cfg.action, "feed_id", cfg.feedID)
	result, runErr := backfill.Run(ctx, backfill.Request{Action: cfg.action, FeedID: cfg.feedID, IncludeCategories: cfg.includeCategories}, deps)
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
	_, _ = fmt.Fprintf(output, "feed_id=%d source_id=%s scanned=%d existing=%d missing=%d duplicates=%d imported=%d duplicate_imports=%d comment_failures=%d\n",
		result.Feed.ID, result.SourceID, result.Scanned, result.Existing, result.Missing,
		result.DuplicateCandidates, result.Imported, result.DuplicateImports, result.CommentFailures)
	if len(result.MissingByCategory) > 0 {
		categories := make([]string, 0, len(result.MissingByCategory))
		for category := range result.MissingByCategory {
			categories = append(categories, category)
		}
		sort.Strings(categories)
		counts := make([]string, 0, len(categories))
		for _, category := range categories {
			counts = append(counts, fmt.Sprintf("%s=%d", category, result.MissingByCategory[category]))
		}
		_, _ = fmt.Fprintf(output, "missing_by_category=%s\n", strings.Join(counts, ","))
	}
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
