package zhihu

import (
	"context"
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
}

// NewBackfillSource 创建串行且限速的知乎历史适配器。
// maxItems 为 0 表示不限制；知乎动态流不会自然收敛，无上限扫描等于上千次上游请求。
func NewBackfillSource(cookie string, requestInterval time.Duration, includeSelfInteraction bool, maxItems int, startOffset int64) (*BackfillSource, error) {
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
	return &BackfillSource{route: r, includeSelfInteraction: includeSelfInteraction, maxItems: maxItems, startOffset: startOffset}, nil
}

// FeedIdentity 保留 feed URL 中的类别过滤。
func (s *BackfillSource) FeedIdentity(feedURL string) (backfill.Identity, error) {
	return backfill.FeedIdentityFromURL(feedURL, "zhihu")
}

// KnownCategories 返回知乎动态的合法类别。
func (s *BackfillSource) KnownCategories() []string {
	return []string{
		TypeAnswer, TypeArticle, TypePin, TypeCollectedAnswer, TypeCollectedArticle,
		TypeCollectedPin, TypeVoteupAnswer, TypeVoteupArticle, TypeFollowedQuestion,
		TypeFollowedCollection, TypeVoteupPin, TypeAskedQuestion,
	}
}

// Discover 扫描至多 maxItems 条动态，类别过滤由 backfill.Run 执行。
func (s *BackfillSource) Discover(ctx context.Context, userID string, progress backfill.ProgressFunc) ([]backfill.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	activities, err := s.route.fetchActivitiesPaged(userID, s.maxItems, s.startOffset, func(page, pageItems, totalItems int, nextOffset string) {
		if progress != nil {
			progress("知乎历史扫描进度", "source_id", userID, "page", page, "page_items", pageItems, "scanned", totalItems, "next_offset", nextOffset)
		}
	})
	if err != nil {
		return nil, err
	}
	// 截断必须说出来：静默截断会让用户以为回填已覆盖全部历史。
	// 游标取最后一条被保留的动态，而非整页末尾——按 maxItems 截断会丢弃该页剩余条目，
	// 用整页末尾续扫会让这几条永远落不进任何窗口。
	if s.maxItems > 0 && len(activities) >= s.maxItems && progress != nil {
		progress("已达知乎历史扫描上限，更早的历史未被扫描",
			"source_id", userID, "max_items", s.maxItems,
			"next_offset", string(activities[len(activities)-1].ID))
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
	return candidates, nil
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
	if value != "" && strings.Trim(value, "0123456789") == "" {
		offset, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("--start-before 毫秒游标超出 int64 范围")
		}
		if offset < 1_000_000_000_000 {
			return 0, fmt.Errorf("--start-before 数字形态必须是 13 位起的毫秒游标")
		}
		return offset, nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if date, err := time.Parse(layout, value); err == nil {
			return date.Unix() * 1000, nil
		}
	}
	return 0, fmt.Errorf("--start-before 必须是 13 位起的毫秒游标或日期（YYYY-MM-DD / RFC3339）")
}
