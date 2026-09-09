package zhihu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/config"
)

// BackfillSource 从知乎动态列表发现完整历史，复用 RSS 的身份和正文渲染。
type BackfillSource struct {
	route                  *Route
	includeSelfInteraction bool
	maxItems               int
	startOffset            int64
	stopAfter              int64
}

// NewBackfillSource 创建串行且限速的知乎历史适配器。
// maxItems 为 0 表示不限制；知乎动态流不会自然收敛，无上限扫描等于上千次上游请求。
func NewBackfillSource(cookie string, requestInterval time.Duration, includeSelfInteraction bool, maxItems int, startOffset, stopAfter int64) (*BackfillSource, error) {
	if requestInterval < time.Second {
		return nil, fmt.Errorf("知乎请求间隔不能小于 1s")
	}
	if maxItems < 0 {
		return nil, fmt.Errorf("知乎历史扫描上限不能为负数")
	}
	r := New(config.ResolvedRouteConfig{
		Cookie: cookie, RateLimit: requestInterval.Seconds(), Impersonate: "chrome_131",
	})
	// List 只解析 feed URL，不读取 cookie；执行和 dry-run 的非空凭证在此校验。
	if cookie != "" {
		if _, err := r.getDC0(); err != nil {
			return nil, err
		}
	}
	return &BackfillSource{route: r, includeSelfInteraction: includeSelfInteraction, maxItems: maxItems, startOffset: startOffset, stopAfter: stopAfter}, nil
}

// defaultBackfillCategories 是 feed URL 未带 include 时的回填默认类别：只要作者
// 本人创作的内容。backfill 命令不加载 config.yml，够不到 RSS 路径的 default_include，
// 默认值只能定在这里；要收藏、点赞、关注等类别时用 --include 显式列出。
var defaultBackfillCategories = []string{TypeAnswer, TypeArticle, TypePin}

// FeedIdentity 保留 feed URL 中的类别过滤；URL 未指定时回落到仅创作类。
func (s *BackfillSource) FeedIdentity(feedURL string) (backfill.Identity, error) {
	identity, err := backfill.FeedIdentityFromURL(feedURL, "zhihu")
	if err != nil {
		return identity, err
	}
	if len(identity.Categories) == 0 {
		identity.Categories = append([]string(nil), defaultBackfillCategories...)
	}
	return identity, nil
}

// knownBackfillCategories 是知乎动态的全部合法类别，按创作、收藏、点赞、关注分组排列。
// backfill.Run 的类别校验与 CLI 的 --include 帮助文本共用这一份，避免两处各写一遍而悄悄发散。
var knownBackfillCategories = []string{
	TypeAnswer, TypeArticle, TypePin, TypeCollectedAnswer, TypeCollectedArticle,
	TypeCollectedPin, TypeVoteupAnswer, TypeVoteupArticle, TypeFollowedQuestion,
	TypeFollowedCollection, TypeVoteupPin, TypeAskedQuestion,
}

// KnownBackfillCategories 返回合法类别的副本，供 CLI 组装 --include 的帮助文本。
func KnownBackfillCategories() []string {
	return append([]string(nil), knownBackfillCategories...)
}

// KnownCategories 返回知乎动态的合法类别。
func (s *BackfillSource) KnownCategories() []string {
	return KnownBackfillCategories()
}

// SupportsRefresh 表示知乎正文可由当前渲染器完整重建。
func (s *BackfillSource) SupportsRefresh() bool { return true }

// Discover 扫描至多 maxItems 条动态，类别过滤由 backfill.Run 执行。
func (s *BackfillSource) Discover(ctx context.Context, userID string, progress backfill.ProgressFunc) (backfill.Scan, error) {
	if err := ctx.Err(); err != nil {
		return backfill.Scan{}, err
	}
	activities, err := s.route.fetchActivitiesPaged(ctx, userID, s.maxItems, s.startOffset, s.stopAfter, func(page, pageItems, totalItems int, nextOffset string) {
		if progress != nil {
			progress("知乎历史扫描进度", "source_id", userID, "page", page, "page_items", pageItems, "scanned", totalItems, "next_offset", nextOffset)
		}
	})
	interrupted := len(activities) > 0 && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	if err != nil && !interrupted {
		return backfill.Scan{}, err
	}
	scan := backfill.Scan{Interrupted: interrupted}
	// 截断必须说出来：静默截断会让用户以为回填已覆盖全部历史。
	// 游标取最后一条被保留的动态，而非整页末尾——按 maxItems 截断会丢弃该页剩余条目，
	// 用整页末尾续扫会让这几条永远落不进任何窗口。
	if interrupted || (s.maxItems > 0 && len(activities) >= s.maxItems) {
		scan.Truncated = true
		scan.NextCursor = string(activities[len(activities)-1].ID)
	}
	candidates := make([]backfill.Candidate, 0, len(activities))
	for _, act := range activities {
		if act.Target == nil {
			continue
		}
		if !s.includeSelfInteraction && isSelfInteraction(act) {
			continue
		}
		item := s.route.makeFeedItem(act)
		candidate := backfill.Candidate{
			ID: item.GUID, Title: item.Title, Author: item.Author, URL: item.Link,
			Content: item.Content, Category: item.Categories[0],
		}
		if item.PubDate != nil {
			candidate.PublishedAt = *item.PubDate
		}
		candidates = append(candidates, candidate)
	}
	scan.Candidates = candidates
	return scan, nil
}

// Detail 无操作：动态列表就是完整来源，没有独立正文详情请求。
// 空正文保持为空，与 RSS 路径对同一条动态的渲染结果一致。
func (s *BackfillSource) Detail(ctx context.Context, candidate backfill.Candidate) (string, error) {
	return "", nil
}

// Comments 无操作：知乎 RSS 路径不抓取评论。
func (s *BackfillSource) Comments(ctx context.Context, candidate backfill.Candidate) (string, error) {
	return "", nil
}

// ParseStartBefore 将毫秒游标或日期解析为知乎动态的起始游标。
func ParseStartBefore(value string) (int64, error) {
	return ParseScanOffset("start-before", value)
}

// ParseScanOffset 共用上下界解析规则，日期先取整秒再换算为毫秒游标。
func ParseScanOffset(flagName, value string) (int64, error) {
	if value != "" && strings.Trim(value, "0123456789") == "" {
		offset, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("--%s 毫秒游标超出 int64 范围", flagName)
		}
		if offset < 1_000_000_000_000 {
			return 0, fmt.Errorf("--%s 数字形态必须是 13 位起的毫秒游标", flagName)
		}
		return offset, nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if date, err := time.Parse(layout, value); err == nil {
			return date.Unix() * 1000, nil
		}
	}
	return 0, fmt.Errorf("--%s 必须是 13 位起的毫秒游标或日期（YYYY-MM-DD / RFC3339）", flagName)
}
