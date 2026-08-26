// Package backfill 将数据源的完整历史与现有 Miniflux feed 对账。
package backfill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Action 选择前台回填操作。
type Action string

const (
	ActionList    Action = "list"
	ActionDryRun  Action = "dry-run"
	ActionExecute Action = "execute"
)

// Request 是一次回填运行的完整调用参数。
type Request struct {
	Action Action
	FeedID int64
}

// Feed 是对账所需的 Miniflux feed 身份信息。
type Feed struct {
	ID      int64
	Title   string
	FeedURL string
}

// Entry 是对账所需的现有 Miniflux 条目标识。
type Entry struct {
	URL  string
	Hash string
}

// Candidate 是无需请求完整正文即可发现的数据源条目。
type Candidate struct {
	ID          string
	Title       string
	Author      string
	URL         string
	PublishedAt time.Time
}

// Article 是发送到 Miniflux Entry Import 的规范化条目。
type Article struct {
	Title       string
	URL         string
	Author      string
	Content     string
	PublishedAt time.Time
	Status      string
	ExternalID  string
}

// ImportOutcome 区分 Miniflux 新建条目和重复条目响应。
type ImportOutcome int

const (
	ImportCreated ImportOutcome = iota + 1
	ImportExisting
)

// Source 是历史内容源的内部接缝。
type Source interface {
	FeedIdentity(feedURL string) (string, error)
	Discover(ctx context.Context, sourceID string) ([]Candidate, error)
	Detail(ctx context.Context, candidate Candidate) (string, error)
	Comments(ctx context.Context, candidate Candidate) (string, error)
}

// Destination 是 Miniflux 对账与导入的内部接缝。
type Destination interface {
	Feeds(ctx context.Context) ([]Feed, error)
	Feed(ctx context.Context, feedID int64) (Feed, error)
	Entries(ctx context.Context, feedID int64) ([]Entry, error)
	Import(ctx context.Context, feedID int64, article Article) (ImportOutcome, error)
}

// Warning 记录不阻断回填的数据源错误。
type Warning struct {
	CandidateID string
	Operation   string
	Err         error
}

// Result 报告 Run 执行的可观察结果。
type Result struct {
	Feeds               []Feed
	Feed                Feed
	SourceID            string
	Scanned             int
	Existing            int
	Missing             int
	DuplicateCandidates int
	Imported            int
	DuplicateImports    int
	CommentFailures     int
	MissingOldest       *time.Time
	MissingNewest       *time.Time
	Warnings            []Warning
}

// WaitFunc 让测试可以确定性地替换重试等待。
type WaitFunc func(context.Context, time.Duration) error

// Dependencies 提供两个外部适配器与重试策略。
type Dependencies struct {
	Source         Source
	Destination    Destination
	MaxAttempts    int
	RetryBaseDelay time.Duration
	Wait           WaitFunc
}

// Run 列出可用 feed，或将指定 feed 与数据源完整历史对账。
func Run(ctx context.Context, req Request, deps Dependencies) (Result, error) {
	var result Result
	if deps.Destination == nil {
		return result, fmt.Errorf("缺少 Miniflux 适配器")
	}
	if deps.Source == nil {
		return result, fmt.Errorf("缺少数据源适配器")
	}
	policy := retryPolicyFrom(deps)

	switch req.Action {
	case ActionList:
		feeds, err := retryValue(ctx, policy, deps.Destination.Feeds)
		if err != nil {
			return result, fmt.Errorf("列出 Miniflux feeds: %w", err)
		}
		for _, feed := range feeds {
			if _, err := deps.Source.FeedIdentity(feed.FeedURL); err == nil {
				result.Feeds = append(result.Feeds, feed)
			}
		}
		return result, nil
	case ActionDryRun, ActionExecute:
		if req.FeedID <= 0 {
			return result, fmt.Errorf("feed ID 必须是正整数")
		}
	default:
		return result, fmt.Errorf("不支持的回填操作 %q", req.Action)
	}

	feed, err := retryValue(ctx, policy, func(ctx context.Context) (Feed, error) {
		return deps.Destination.Feed(ctx, req.FeedID)
	})
	if err != nil {
		return result, fmt.Errorf("获取 Miniflux feed %d: %w", req.FeedID, err)
	}
	if feed.ID != req.FeedID {
		return result, fmt.Errorf("Miniflux feed 响应 ID=%d，与请求 ID=%d 不一致", feed.ID, req.FeedID)
	}
	result.Feed = feed
	sourceID, err := deps.Source.FeedIdentity(feed.FeedURL)
	if err != nil {
		return result, fmt.Errorf("feed %d 与当前数据源不匹配: %w", req.FeedID, err)
	}
	result.SourceID = sourceID

	entries, err := retryValue(ctx, policy, func(ctx context.Context) ([]Entry, error) {
		return deps.Destination.Entries(ctx, req.FeedID)
	})
	if err != nil {
		return result, fmt.Errorf("读取 feed %d 的现有条目: %w", req.FeedID, err)
	}
	candidates, err := retryValue(ctx, policy, func(ctx context.Context) ([]Candidate, error) {
		return deps.Source.Discover(ctx, sourceID)
	})
	if err != nil {
		return result, fmt.Errorf("发现数据源完整历史: %w", err)
	}

	missing, err := reconcile(candidates, entries, &result)
	if err != nil {
		return result, err
	}
	if req.Action == ActionDryRun {
		return result, nil
	}

	for _, candidate := range missing {
		content, err := retryValue(ctx, policy, func(ctx context.Context) (string, error) {
			return deps.Source.Detail(ctx, candidate)
		})
		if err != nil {
			return result, fmt.Errorf("获取文章 %s 详情: %w", candidate.ID, err)
		}

		comments, commentErr := retryValue(ctx, policy, func(ctx context.Context) (string, error) {
			return deps.Source.Comments(ctx, candidate)
		})
		if commentErr != nil {
			result.CommentFailures++
			result.Warnings = append(result.Warnings, Warning{
				CandidateID: candidate.ID,
				Operation:   "comments",
				Err:         commentErr,
			})
		} else {
			content = appendContent(content, comments)
		}

		article := Article{
			Title:       candidate.Title,
			URL:         candidate.URL,
			Author:      candidate.Author,
			Content:     content,
			PublishedAt: candidate.PublishedAt,
			Status:      "unread",
			ExternalID:  candidate.ID,
		}
		outcome, err := retryValue(ctx, policy, func(ctx context.Context) (ImportOutcome, error) {
			return deps.Destination.Import(ctx, req.FeedID, article)
		})
		if err != nil {
			return result, fmt.Errorf("导入文章 %s: %w", candidate.ID, err)
		}
		switch outcome {
		case ImportCreated:
			result.Imported++
		case ImportExisting:
			result.DuplicateImports++
		default:
			return result, fmt.Errorf("导入文章 %s: 未知导入结果 %d", candidate.ID, outcome)
		}
	}

	return result, nil
}

func reconcile(candidates []Candidate, entries []Entry, result *Result) ([]Candidate, error) {
	existingURLs := make(map[string]struct{}, len(entries))
	existingHashes := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.URL != "" {
			existingURLs[entry.URL] = struct{}{}
		}
		if entry.Hash != "" {
			existingHashes[strings.ToLower(entry.Hash)] = struct{}{}
		}
	}

	unique := make([]Candidate, 0, len(candidates))
	seenIDs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			return nil, fmt.Errorf("数据源历史包含缺少稳定 ID 的条目")
		}
		if _, ok := seenIDs[candidate.ID]; ok {
			result.DuplicateCandidates++
			continue
		}
		seenIDs[candidate.ID] = struct{}{}
		unique = append(unique, candidate)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return unique[i].PublishedAt.After(unique[j].PublishedAt)
	})

	result.Scanned = len(unique)
	missing := make([]Candidate, 0, len(unique))
	for _, candidate := range unique {
		_, urlExists := existingURLs[candidate.URL]
		_, hashExists := existingHashes[externalIDHash(candidate.ID)]
		if urlExists || hashExists {
			result.Existing++
			continue
		}
		missing = append(missing, candidate)
		result.Missing++
		if candidate.PublishedAt.IsZero() {
			continue
		}
		if result.MissingOldest == nil || candidate.PublishedAt.Before(*result.MissingOldest) {
			t := candidate.PublishedAt
			result.MissingOldest = &t
		}
		if result.MissingNewest == nil || candidate.PublishedAt.After(*result.MissingNewest) {
			t := candidate.PublishedAt
			result.MissingNewest = &t
		}
	}
	return missing, nil
}

// FeedIdentityFromURL 校验 RSSGen feed 路径并解码单段数据源 ID。
func FeedIdentityFromURL(feedURL, routeName string) (string, error) {
	u, err := url.Parse(feedURL)
	if err != nil {
		return "", fmt.Errorf("解析 feed_url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("feed_url 必须是绝对 URL")
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] != "feed" || parts[1] != routeName || parts[2] == "" {
		return "", fmt.Errorf("feed_url 路径必须是 /feed/%s/{source_id}", routeName)
	}
	slug, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", fmt.Errorf("解码数据源 ID: %w", err)
	}
	if slug == "" || strings.Contains(slug, "/") {
		return "", fmt.Errorf("数据源 ID 必须是单个非空路径段")
	}
	return slug, nil
}

func externalIDHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func appendContent(content, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return extra
	}
	return strings.TrimRight(content, "\n") + "\n\n" + extra
}

type retryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	wait        WaitFunc
}

func retryPolicyFrom(deps Dependencies) retryPolicy {
	maxAttempts := deps.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > 3 {
		maxAttempts = 3
	}
	baseDelay := deps.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	wait := deps.Wait
	if wait == nil {
		wait = waitContext
	}
	return retryPolicy{maxAttempts: maxAttempts, baseDelay: baseDelay, wait: wait}
}

func retryValue[T any](ctx context.Context, policy retryPolicy, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := operation(ctx)
		if err == nil {
			return value, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return zero, err
		}
		if attempt+1 < policy.maxAttempts {
			delay := policy.baseDelay * time.Duration(1<<attempt)
			if err := policy.wait(ctx, delay); err != nil {
				return zero, err
			}
		}
	}
	return zero, lastErr
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
