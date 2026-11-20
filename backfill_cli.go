package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/media"
	"github.com/PhiFever/RSSGen/internal/miniflux"
	"github.com/PhiFever/RSSGen/internal/route"

	// 注册路由（init 自动注册），同时提供回填适配器。
	"github.com/PhiFever/RSSGen/internal/route/afdian"
	"github.com/PhiFever/RSSGen/internal/route/zhihu"
)

type backfillSourceSpec struct {
	ownFlags  []string
	newSource func(cfg backfillCLIConfig) (backfill.Source, error)
}

var backfillSources = map[string]backfillSourceSpec{
	"afdian": {
		newSource: func(cfg backfillCLIConfig) (backfill.Source, error) {
			return afdian.NewBackfillSource(cfg.sourceCookie, cfg.requestInterval)
		},
	},
	"zhihu": {
		ownFlags: []string{"include", "include-self-interaction", "max-items", "start-before", "stop-after"},
		newSource: func(cfg backfillCLIConfig) (backfill.Source, error) {
			return zhihu.NewBackfillSource(cfg.sourceCookie, cfg.requestInterval, cfg.includeSelfInteraction, cfg.maxItems, cfg.startBefore, cfg.stopAfter)
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
	run := func(ctx context.Context, cmd *cli.Command) error {
		if err := rejectPositionalArgs(cmd); err != nil {
			return err
		}
		cfg, err := backfillConfigFromCommand(cmd)
		if err != nil {
			return err
		}
		return deps.runBackfill(ctx, cfg, cmd.Root().Writer)
	}
	return &cli.Command{
		Name:      "backfill",
		Usage:     "列出订阅源、补齐历史或重刷已有条目",
		UsageText: "rssgen backfill [公共选项] <list|execute|refresh> [选项]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "source", Usage: "数据源（可选：" + backfillSourceNames() + "）", Required: true},
			&cli.StringFlag{Name: "config", Value: "config.yml", Usage: "配置文件路径；读取 backfill 段、路由 cookie 和可选 media 段"},
			&cli.DurationFlag{
				Name: "request-interval", Usage: "数据源请求之间的最小间隔", Value: backfill.MinRequestInterval,
				Validator: backfill.ValidateRequestInterval,
			},
		},
		Commands: []*cli.Command{
			{Name: "list", Usage: "列出该数据源下可回填的 feed", Action: run},
			{Name: "execute", Usage: "补齐 Miniflux 缺失的历史条目", Flags: backfillScanFlags(), Action: run},
			{
				Name: "refresh", Usage: "重刷已有条目的标题与正文（仅知乎，必须指定扫描范围）",
				Flags: append(backfillScanFlags(), &cli.Int64Flag{
					Name: "entry-id", Usage: "只重刷该 Miniflux 条目",
					Validator: func(id int64) error {
						if id <= 0 {
							return fmt.Errorf("--entry-id 必须为正整数")
						}
						return nil
					},
				}),
				Action: run,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := rejectPositionalArgs(cmd); err != nil {
				return err
			}
			return cli.ShowSubcommandHelp(cmd)
		},
	}
}

// backfillScanFlags 为两种扫描操作分别创建参数，避免共享可变的 flag 状态。
func backfillScanFlags() []cli.Flag {
	return []cli.Flag{
		&cli.Int64Flag{Name: "feed-id", Usage: "目标 Miniflux feed 的 ID", Required: true},
		&cli.BoolFlag{Name: "dry-run", Usage: "只扫描并对账，不写入 Miniflux"},
		&cli.StringFlag{Name: "include", Usage: "覆盖 feed URL 中 include 的类别，逗号分隔（仅知乎）；可选值：" + strings.Join(zhihu.KnownBackfillCategories(), ", ")},
		&cli.BoolFlag{Name: "include-self-interaction", Usage: "保留作者对自己内容的赞同和收藏（仅知乎）"},
		&cli.StringFlag{Name: "start-before", Usage: "扫描上界：毫秒游标或日期（YYYY-MM-DD / RFC3339，仅知乎）"},
		&cli.StringFlag{Name: "stop-after", Usage: "扫描下界（包含）：毫秒游标或日期（YYYY-MM-DD / RFC3339，仅知乎）"},
		&cli.IntFlag{
			Name: "max-items", Usage: "最多扫描的动态条数，0 表示不限；重刷须指定下界或显式正整数上限（仅知乎）", Value: defaultBackfillMaxItems,
			Validator: func(value int) error {
				if value < 0 {
					return fmt.Errorf("--max-items 不能为负数")
				}
				return nil
			},
		},
	}
}

// defaultBackfillMaxItems 限制单次历史扫描的动态条数。知乎动态流不会自然收敛
// （实测 7674 条仍未出现 is_end），无上限的回填等于上千次上游请求。
const defaultBackfillMaxItems = 1000

type backfillCLIConfig struct {
	configPath             string
	mediaConfig            *config.MediaConfig
	archiveRoute           config.ResolvedRouteConfig
	includeCategories      []string
	includeSelfInteraction bool
	maxItems               int
	startBefore            int64
	stopAfter              int64
	dryRun                 bool
	rangeConfirmed         bool
	source                 string
	action                 backfill.Action
	minifluxURL            string
	feedID                 int64
	requestInterval        time.Duration
	minifluxToken          string
	sourceCookie           string
	entryID                int64
}

func backfillConfigFromCommand(cmd *cli.Command) (backfillCLIConfig, error) {
	cfg := backfillCLIConfig{
		configPath:             cmd.String("config"),
		includeCategories:      backfill.ParseCategories(cmd.String("include")),
		includeSelfInteraction: cmd.Bool("include-self-interaction"),
		maxItems:               cmd.Int("max-items"),
		source:                 cmd.String("source"),
		action:                 backfill.Action(cmd.Name),
		dryRun:                 cmd.Bool("dry-run"),
		feedID:                 cmd.Int64("feed-id"),
		entryID:                cmd.Int64("entry-id"),
		requestInterval:        cmd.Duration("request-interval"),
	}
	spec, ok := backfillSources[cfg.source]
	if !ok {
		return cfg, fmt.Errorf("未知数据源 %q，可用值: %s", cfg.source, backfillSourceNames())
	}
	if cfg.action == backfill.ActionRefresh {
		source, err := spec.newSource(cfg)
		if err != nil {
			return cfg, err
		}
		if !source.SupportsRefresh() {
			return cfg, fmt.Errorf("数据源 %s 不支持重刷", cfg.source)
		}
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
	if cfg.action != backfill.ActionList && cfg.feedID <= 0 {
		return cfg, fmt.Errorf("必须传入正整数 --feed-id")
	}
	for _, bound := range []struct {
		name   string
		target *int64
	}{
		{"start-before", &cfg.startBefore}, {"stop-after", &cfg.stopAfter},
	} {
		if cmd.IsSet(bound.name) {
			value, err := zhihu.ParseScanOffset(bound.name, cmd.String(bound.name))
			if err != nil {
				return cfg, err
			}
			*bound.target = value
		}
	}
	if cmd.IsSet("start-before") && cmd.IsSet("stop-after") && cfg.startBefore < cfg.stopAfter {
		return cfg, fmt.Errorf("--start-before 不能早于 --stop-after")
	}
	cfg.rangeConfirmed = (cmd.IsSet("stop-after") && cfg.stopAfter != 0) || (cmd.IsSet("max-items") && cfg.maxItems > 0)
	if cfg.action == backfill.ActionRefresh && !cfg.rangeConfirmed {
		return cfg, fmt.Errorf("重刷必须指定 --stop-after 或显式指定正整数 --max-items")
	}
	c, err := config.Load(cfg.configPath)
	if err != nil {
		return cfg, err
	}
	cfg.minifluxURL = strings.TrimSpace(c.Backfill.MinifluxURL)
	cfg.minifluxToken = strings.TrimSpace(c.Backfill.MinifluxAPIToken)
	for _, field := range []struct{ name, value string }{
		{"backfill.miniflux_url", cfg.minifluxURL},
		{"backfill.miniflux_api_token", cfg.minifluxToken},
	} {
		if field.value == "" {
			return cfg, fmt.Errorf("配置文件 %s: %s 不能为空", cfg.configPath, field.name)
		}
	}
	if cfg.action != backfill.ActionList {
		cfg.sourceCookie = strings.TrimSpace(c.Routes[cfg.source].Cookie)
		if cfg.sourceCookie == "" {
			return cfg, fmt.Errorf("配置文件 %s: routes.%s.cookie 不能为空", cfg.configPath, cfg.source)
		}
	}
	if apiURL := strings.TrimSpace(c.Backfill.ImmichURL); apiURL != "" {
		if c.Media == nil {
			return cfg, fmt.Errorf("配置文件 %s: backfill.immich_url 需要 media 段", cfg.configPath)
		}
		u, err := url.Parse(apiURL)
		if err != nil || u.Scheme == "" || u.Hostname() == "" {
			return cfg, fmt.Errorf("配置文件 %s: backfill.immich_url 必须是包含协议和主机的绝对 URL", cfg.configPath)
		}
		c.Media.Immich.APIURL = strings.TrimRight(apiURL, "/")
	}
	cfg.mediaConfig = c.Media
	cfg.archiveRoute = config.ResolveRoute(c.Scraper, c.Routes[cfg.source])
	return cfg, nil
}

func runBackfill(ctx context.Context, cfg backfillCLIConfig, output io.Writer) error {
	deps := backfill.Dependencies{Progress: slog.Info}
	if cfg.mediaConfig != nil && cfg.action != backfill.ActionList {
		archiver := media.New(*cfg.mediaConfig)
		deps.Archive = func(content, link string, published time.Time) (string, error) {
			items := []route.FeedItem{{Content: content, Link: link, PubDate: &published}}
			err := archiver.Archive(cfg.archiveRoute, items)
			return items[0].Content, err
		}
		defer func() {
			stats := archiver.Stats()
			slog.Info("图片归档统计", "stored", stats.Stored, "deduplicated", stats.Deduplicated,
				"found", stats.Found, "source_failures", stats.SourceFailures)
		}()
	}
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
	deps.Source = source
	deps.Destination = destination
	slog.Info("开始历史回填", "source", cfg.source, "action", cfg.action, "feed_id", cfg.feedID)
	result, runErr := backfill.Run(ctx, backfill.Request{
		Action: cfg.action, DryRun: cfg.dryRun, RangeConfirmed: cfg.rangeConfirmed,
		FeedID: cfg.feedID, EntryID: cfg.entryID, IncludeCategories: cfg.includeCategories,
	}, deps)
	if runErr == nil || result.Feed.ID != 0 || result.Scanned != 0 || result.Imported != 0 {
		printBackfillResult(output, cfg.action, cfg.dryRun, result)
	}
	return runErr
}

func printBackfillResult(output io.Writer, action backfill.Action, dryRun bool, result backfill.Result) {
	if action == backfill.ActionList {
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "ID\tTITLE\tFEED_URL")
		for _, feed := range result.Feeds {
			_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\n", feed.ID, feed.Title, feed.FeedURL)
		}
		_ = writer.Flush()
		return
	}
	if action == backfill.ActionRefresh {
		_, _ = fmt.Fprintf(output, "feed_id=%d source_id=%s scanned=%d updated=%d failed=%d comment_failures=%d\n",
			result.Feed.ID, result.SourceID, result.Scanned, result.Updated, len(result.UpdateFailures), result.CommentFailures)
		if dryRun {
			for _, target := range result.PendingUpdates {
				_, _ = fmt.Fprintf(output, "pending_update entry_id=%d url=%s\n", target.EntryID, target.URL)
			}
		}
		for _, failure := range result.UpdateFailures {
			_, _ = fmt.Fprintf(output, "update_failed entry_id=%d post_id=%s url=%s operation=%s error=%v\n",
				failure.EntryID, failure.CandidateID, failure.URL, failure.Operation, failure.Err)
		}
	} else {
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
			// 区间是导入前的对账结论，而这一行在导入之后才打印：execute 下叫 missing
			// 会被读成"仍然缺失"，实际是本轮打算补的范围。
			label := "missing_range"
			if !dryRun {
				label = "planned_range"
			}
			_, _ = fmt.Fprintf(output, "%s=%s..%s\n", label,
				result.MissingOldest.UTC().Format(time.RFC3339), result.MissingNewest.UTC().Format(time.RFC3339))
		}
	}
	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(output, "warning post_id=%s operation=%s error=%v\n", warning.CandidateID, warning.Operation, warning.Err)
	}
	for _, skipped := range result.Skipped {
		_, _ = fmt.Fprintf(output, "skipped id=%s reason=%s\n", skipped.ID, skipped.Reason)
	}
	// 续扫提示保持在最后一行，避免被逐条日志淹没。
	if result.ScanInterrupted || result.ScanTruncated {
		key, reason := "scan_truncated", "更早的历史未被扫描"
		if result.ScanInterrupted {
			key = "scan_interrupted"
			reason = "扫描被中断，已处理部分已写入"
			if action == backfill.ActionRefresh {
				reason = "扫描被中断，已处理部分的更新结果见上方统计"
			}
			// dry-run 一条都不写，"已写入"在这条路径上是假话。
			if dryRun {
				reason = "扫描被中断，以上仅为已处理部分的对账结果"
			}
		}
		// 首页失败或数据源没有游标时，提示直接重跑。
		resume := "，直接重跑即可（已导入的会被跳过）"
		if result.ScanNextCursor != "" {
			resume = fmt.Sprintf("，续扫请传 --start-before=%s", result.ScanNextCursor)
		}
		_, _ = fmt.Fprintf(output, "%s=true %s%s\n", key, reason, resume)
	}
}
