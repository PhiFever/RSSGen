package backfill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	refreshSupported bool
	emptyDetail      bool
	commentCancel    context.CancelFunc
	knownCategories  []string
	emptyComments    bool
	candidates       []Candidate
	discoverErrs     []error
	detailErrs       map[string][]error
	commentErrs      map[string][]error
	detailCalls      []string
	commentCalls     []string
	discoverCalls    int
	reportPage       bool
	scanNextCursor   string
	discoverCancel   context.CancelFunc
	scanTruncated    bool
	scanInterrupted  bool
}

func (s *fakeSource) FeedIdentity(feedURL string) (Identity, error) {
	return FeedIdentityFromURL(feedURL, "afdian")
}

func (s *fakeSource) KnownCategories() []string { return s.knownCategories }

func (s *fakeSource) SupportsRefresh() bool { return s.refreshSupported }

func (s *fakeSource) Discover(_ context.Context, _ string, progress ProgressFunc) (Scan, error) {
	s.discoverCalls++
	if s.discoverCancel != nil {
		s.discoverCancel()
	}
	if err := popError(&s.discoverErrs); err != nil {
		return Scan{}, err
	}
	if s.reportPage {
		reportProgress(progress, "数据源分页扫描进度", "page", 1, "page_items", len(s.candidates), "scanned", len(s.candidates))
	}
	return Scan{
		Candidates:  append([]Candidate(nil), s.candidates...),
		NextCursor:  s.scanNextCursor,
		Truncated:   s.scanTruncated,
		Interrupted: s.scanInterrupted,
	}, nil
}

func (s *fakeSource) Detail(_ context.Context, candidate Candidate) (string, error) {
	s.detailCalls = append(s.detailCalls, candidate.ID)
	if s.emptyDetail {
		return "", nil
	}
	errs := s.detailErrs[candidate.ID]
	if err := popError(&errs); err != nil {
		s.detailErrs[candidate.ID] = errs
		return "", err
	}
	s.detailErrs[candidate.ID] = errs
	return "body-" + candidate.ID, nil
}

func (s *fakeSource) Comments(_ context.Context, candidate Candidate) (string, error) {
	s.commentCalls = append(s.commentCalls, candidate.ID)
	if s.commentCancel != nil {
		s.commentCancel()
		return "", context.Canceled
	}
	if s.emptyComments {
		return "", nil
	}
	errs := s.commentErrs[candidate.ID]
	if err := popError(&errs); err != nil {
		s.commentErrs[candidate.ID] = errs
		return "", err
	}
	s.commentErrs[candidate.ID] = errs
	return "comments-" + candidate.ID, nil
}

type fakeDestination struct {
	updates        []recordedUpdate
	updateErrs     []error
	updateCancel   context.CancelFunc
	entriesCalls   int
	feeds          []Feed
	feed           Feed
	entries        []Entry
	imported       []Article
	importOutcomes map[string]ImportOutcome
	importErrs     map[string][]error
}

type recordedUpdate struct {
	EntryID        int64
	Title, Content string
}

func (d *fakeDestination) Update(_ context.Context, entryID int64, title, content string) error {
	d.updates = append(d.updates, recordedUpdate{entryID, title, content})
	if d.updateCancel != nil {
		d.updateCancel()
		return context.Canceled
	}
	return popError(&d.updateErrs)
}

func (d *fakeDestination) Feeds(context.Context) ([]Feed, error) {
	return append([]Feed(nil), d.feeds...), nil
}

func (d *fakeDestination) Feed(context.Context, int64) (Feed, error) {
	return d.feed, nil
}

func (d *fakeDestination) Entries(context.Context, int64) ([]Entry, error) {
	d.entriesCalls++
	return append([]Entry(nil), d.entries...), nil
}

func (d *fakeDestination) Import(_ context.Context, _ int64, article Article) (ImportOutcome, error) {
	d.imported = append(d.imported, article)
	errs := d.importErrs[article.ExternalID]
	if err := popError(&errs); err != nil {
		d.importErrs[article.ExternalID] = errs
		return 0, err
	}
	d.importErrs[article.ExternalID] = errs
	if outcome := d.importOutcomes[article.ExternalID]; outcome != 0 {
		return outcome, nil
	}
	return ImportCreated, nil
}

func popError(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}

func noWait(context.Context, time.Duration) error { return nil }

func TestRunFlushesInterruptedScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeSource{
		candidates: []Candidate{
			{ID: "a", URL: "https://afdian.com/p/a"},
			{ID: "b", URL: "https://afdian.com/p/b"},
		},
		detailErrs:      map[string][]error{},
		commentErrs:     map[string][]error{},
		discoverCancel:  cancel,
		scanTruncated:   true,
		scanInterrupted: true,
	}
	destination := &fakeDestination{
		feed:       Feed{ID: 42, FeedURL: "http://rssgen:8000/feed/afdian/alice"},
		importErrs: map[string][]error{},
	}
	result, err := Run(ctx, Request{Action: ActionExecute, FeedID: 42}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if err != nil || result.Imported != len(source.candidates) || len(destination.imported) != len(source.candidates) || !result.ScanInterrupted {
		t.Fatalf("Run result=%+v imports=%d err=%v", result, len(destination.imported), err)
	}
	if ctx.Err() != context.Canceled || source.discoverCalls != 1 {
		t.Fatalf("ctx.Err()=%v discoverCalls=%d", ctx.Err(), source.discoverCalls)
	}
}

func TestRunCarriesScanTruncationIntoResult(t *testing.T) {
	source := &fakeSource{
		candidates:     []Candidate{{ID: "a", URL: "https://afdian.com/p/a"}},
		scanTruncated:  true,
		scanNextCursor: "1995",
	}
	destination := &fakeDestination{feed: Feed{ID: 42, FeedURL: "http://rssgen:8000/feed/afdian/alice"}}

	result, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 42}, Dependencies{
		Source:      source,
		Destination: destination,
		Wait:        noWait,
	})
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if !result.ScanTruncated || result.ScanNextCursor != "1995" {
		t.Fatalf("Result = %+v", result)
	}
}

func TestRunListFiltersAfdianFeeds(t *testing.T) {
	destination := &fakeDestination{feeds: []Feed{
		{ID: 1, Title: "A", FeedURL: "http://rssgen:8000/feed/afdian/alice"},
		{ID: 2, Title: "Z", FeedURL: "http://rssgen:8000/feed/zhihu/bob"},
		{ID: 3, Title: "Encoded", FeedURL: "https://rss.example/feed/afdian/%E7%88%B1"},
	}}

	result, err := Run(context.Background(), Request{Action: ActionList}, Dependencies{
		Source:      &fakeSource{},
		Destination: destination,
		Wait:        noWait,
	})
	if err != nil {
		t.Fatalf("Run list: %v", err)
	}
	if len(result.Feeds) != 2 || result.Feeds[0].ID != 1 || result.Feeds[1].ID != 3 {
		t.Fatalf("Feeds = %+v", result.Feeds)
	}
}

func TestRunDryRunReconcilesAllEntriesAndArbitraryGap(t *testing.T) {
	t3 := time.Unix(300, 0).UTC()
	t2 := time.Unix(200, 0).UTC()
	t1 := time.Unix(100, 0).UTC()
	source := &fakeSource{candidates: []Candidate{
		{ID: "new", URL: "https://afdian.com/p/new", PublishedAt: t3},
		{ID: "gap", URL: "https://afdian.com/p/gap", PublishedAt: t2},
		{ID: "old", URL: "https://afdian.com/p/old", PublishedAt: t1},
		{ID: "gap", URL: "https://afdian.com/p/gap", PublishedAt: t2},
	}}
	destination := &fakeDestination{
		feed: Feed{ID: 42, FeedURL: "http://rssgen:8000/feed/afdian/alice"},
		entries: []Entry{
			{URL: "https://afdian.com/p/new"},
			{Hash: hashID("old")},
			{URL: "https://afdian.com/p/deleted"},
		},
	}

	result, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 42}, Dependencies{
		Source:      source,
		Destination: destination,
		Wait:        noWait,
	})
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if result.SourceID != "alice" || result.Scanned != 3 || result.Existing != 2 || result.Missing != 1 || result.DuplicateCandidates != 1 {
		t.Fatalf("Result = %+v", result)
	}
	if result.MissingOldest == nil || !result.MissingOldest.Equal(t2) || result.MissingNewest == nil || !result.MissingNewest.Equal(t2) {
		t.Fatalf("missing range = %v..%v", result.MissingOldest, result.MissingNewest)
	}
	if len(source.detailCalls) != 0 || len(source.commentCalls) != 0 || len(destination.imported) != 0 {
		t.Fatal("dry-run must not fetch details/comments or import")
	}
}

func TestRunExecuteImportsNewestFirstAndContinuesAfterCommentFailure(t *testing.T) {
	source := &fakeSource{
		candidates: []Candidate{
			{ID: "old", Title: "Old", Author: "Alice", URL: "https://afdian.com/p/old", PublishedAt: time.Unix(100, 0).UTC()},
			{ID: "new", Title: "New", Author: "Alice", URL: "https://afdian.com/p/new", PublishedAt: time.Unix(300, 0).UTC()},
		},
		detailErrs:  map[string][]error{},
		commentErrs: map[string][]error{"new": {errors.New("comment down"), errors.New("comment down"), errors.New("comment down")}},
	}
	destination := &fakeDestination{
		feed:           Feed{ID: 42, FeedURL: "http://rssgen/feed/afdian/alice"},
		importErrs:     map[string][]error{},
		importOutcomes: map[string]ImportOutcome{"old": ImportExisting},
	}

	result, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 42}, Dependencies{
		Source:      source,
		Destination: destination,
		Wait:        noWait,
	})
	if err != nil {
		t.Fatalf("Run execute: %v", err)
	}
	if !reflect.DeepEqual(source.detailCalls, []string{"new", "old"}) {
		t.Fatalf("detail order = %v", source.detailCalls)
	}
	if len(destination.imported) != 2 || destination.imported[0].ExternalID != "new" || destination.imported[1].ExternalID != "old" {
		t.Fatalf("import order = %+v", destination.imported)
	}
	if destination.imported[0].Content != "body-new" {
		t.Fatalf("failed comments should preserve body, got %q", destination.imported[0].Content)
	}
	if destination.imported[1].Content != "body-old\n\ncomments-old" {
		t.Fatalf("comments not appended: %q", destination.imported[1].Content)
	}
	if destination.imported[0].Status != "unread" || destination.imported[0].ExternalID != "new" {
		t.Fatalf("import identity/status = %+v", destination.imported[0])
	}
	if result.Imported != 1 || result.DuplicateImports != 1 || result.CommentFailures != 1 {
		t.Fatalf("Result = %+v", result)
	}
}

func TestRunReportsPhaseDiscoveryAndImportProgress(t *testing.T) {
	source := &fakeSource{
		candidates: []Candidate{{
			ID: "post-1", Title: "Paid", URL: "https://afdian.com/p/post-1", PublishedAt: time.Unix(100, 0),
		}},
		detailErrs:  map[string][]error{},
		commentErrs: map[string][]error{},
		reportPage:  true,
	}
	destination := &fakeDestination{
		feed:       Feed{ID: 42, Title: "Alice", FeedURL: "http://rssgen/feed/afdian/alice"},
		importErrs: map[string][]error{},
	}
	type progressRecord struct {
		message string
		attrs   []any
	}
	var records []progressRecord

	_, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 42}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
		Progress: func(message string, attrs ...any) {
			records = append(records, progressRecord{message: message, attrs: append([]any(nil), attrs...)})
		},
	})
	if err != nil {
		t.Fatalf("Run execute: %v", err)
	}
	wantMessages := []string{
		"目标 feed 已解析",
		"开始读取 Miniflux 现有条目",
		"Miniflux 现有条目读取完成",
		"开始扫描数据源完整历史",
		"数据源分页扫描进度",
		"数据源完整历史扫描完成",
		"历史对账完成",
		"开始处理缺失条目",
		"缺失条目处理完成",
	}
	var gotMessages []string
	for _, record := range records {
		gotMessages = append(gotMessages, record.message)
	}
	if !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("progress messages = %v, want %v", gotMessages, wantMessages)
	}
	completed := attrsMap(records[len(records)-1].attrs)
	if completed["current"] != 1 || completed["total"] != 1 || completed["post_id"] != "post-1" || completed["outcome"] != "created" {
		t.Fatalf("completed attrs = %+v", completed)
	}
}

func TestRunRetriesDiscoveryBeforeStartingDetails(t *testing.T) {
	source := &fakeSource{discoverErrs: []error{errors.New("bad"), errors.New("bad"), errors.New("bad")}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}

	_, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if err == nil {
		t.Fatal("expected discovery failure")
	}
	if source.discoverCalls != 3 || len(source.detailCalls) != 0 || len(destination.imported) != 0 {
		t.Fatalf("calls: discover=%d detail=%v imports=%v", source.discoverCalls, source.detailCalls, destination.imported)
	}
}

func TestRunUsesConservativeExponentialRetryDelays(t *testing.T) {
	source := &fakeSource{discoverErrs: []error{errors.New("once"), errors.New("twice")}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}
	var waits []time.Duration

	_, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 1}, Dependencies{
		Source: source, Destination: destination,
		MaxAttempts: 10,
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{time.Second, 2 * time.Second}) || source.discoverCalls != 3 {
		t.Fatalf("waits=%v attempts=%d", waits, source.discoverCalls)
	}
}

func TestRunStopsAtDetailFailureAndDoesNotProcessOlderItems(t *testing.T) {
	source := &fakeSource{
		candidates: []Candidate{
			{ID: "new", URL: "https://afdian.com/p/new", PublishedAt: time.Unix(300, 0)},
			{ID: "old", URL: "https://afdian.com/p/old", PublishedAt: time.Unix(100, 0)},
		},
		detailErrs:  map[string][]error{"new": {errors.New("bad"), errors.New("bad"), errors.New("bad")}},
		commentErrs: map[string][]error{},
	}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}

	_, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if err == nil {
		t.Fatal("expected detail failure")
	}
	if !reflect.DeepEqual(source.detailCalls, []string{"new", "new", "new"}) || len(destination.imported) != 0 {
		t.Fatalf("detail calls=%v imports=%v", source.detailCalls, destination.imported)
	}
}

func TestRunRetriesClientTimeoutMistakenForContextDeadline(t *testing.T) {
	source := &fakeSource{
		candidates:  []Candidate{{ID: "post-1", URL: "https://afdian.com/p/post-1"}},
		detailErrs:  map[string][]error{},
		commentErrs: map[string][]error{},
	}
	destination := &fakeDestination{
		feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"},
		importErrs: map[string][]error{"post-1": {
			fmt.Errorf("Miniflux 请求失败: %w", context.DeadlineExceeded),
			fmt.Errorf("Miniflux 请求失败: %w", context.DeadlineExceeded),
		}},
	}

	result, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var importIDs []string
	for _, article := range destination.imported {
		importIDs = append(importIDs, article.ExternalID)
	}
	if !reflect.DeepEqual(importIDs, []string{"post-1", "post-1", "post-1"}) || result.Imported != 1 {
		t.Fatalf("import attempts=%v result=%+v", importIDs, result)
	}
}

func TestRunStopsImmediatelyWhenCallerContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &fakeSource{}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}

	_, err := Run(ctx, Request{Action: ActionExecute, FeedID: 1}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if !errors.Is(err, ctx.Err()) {
		t.Fatalf("Run error=%v, want %v", err, ctx.Err())
	}
	if len(destination.imported) != 0 || source.discoverCalls != 0 {
		t.Fatalf("imports=%v discoverCalls=%d", destination.imported, source.discoverCalls)
	}
}

func TestRunStopsAtImportFailureAndReturnsPriorProgress(t *testing.T) {
	source := &fakeSource{
		candidates: []Candidate{
			{ID: "new", URL: "https://afdian.com/p/new", PublishedAt: time.Unix(300, 0)},
			{ID: "middle", URL: "https://afdian.com/p/middle", PublishedAt: time.Unix(200, 0)},
			{ID: "old", URL: "https://afdian.com/p/old", PublishedAt: time.Unix(100, 0)},
		},
		detailErrs:  map[string][]error{},
		commentErrs: map[string][]error{},
	}
	destination := &fakeDestination{
		feed:       Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"},
		importErrs: map[string][]error{"middle": {errors.New("bad"), errors.New("bad"), errors.New("bad")}},
	}

	result, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{
		Source: source, Destination: destination, Wait: noWait,
	})
	if err == nil {
		t.Fatal("expected import failure")
	}
	if result.Imported != 1 {
		t.Fatalf("partial result = %+v", result)
	}
	var importIDs []string
	for _, article := range destination.imported {
		importIDs = append(importIDs, article.ExternalID)
	}
	if !reflect.DeepEqual(importIDs, []string{"new", "middle", "middle", "middle"}) {
		t.Fatalf("import attempts = %v", importIDs)
	}
}

func TestFeedIdentityFromURL(t *testing.T) {
	tests := []struct {
		name    string
		feedURL string
		want    Identity
		ok      bool
	}{
		{"plain", "http://rssgen:8000/feed/afdian/alice", Identity{SourceID: "alice"}, true},
		{"escaped", "https://rss.example/feed/afdian/%E7%88%B1", Identity{SourceID: "爱"}, true},
		{"include", "http://rssgen/feed/afdian/alice?include=b,a", Identity{SourceID: "alice", Categories: []string{"a", "b"}}, true},
		{"wrong route", "https://rss.example/feed/zhihu/alice", Identity{}, false},
		{"missing slug", "https://rss.example/feed/afdian", Identity{}, false},
		{"extra segment", "https://rss.example/feed/afdian/alice/more", Identity{}, false},
		{"encoded slash", "https://rss.example/feed/afdian/a%2Fb", Identity{}, false},
		{"relative", "/feed/afdian/alice", Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FeedIdentityFromURL(tt.feedURL, "afdian")
			if (err == nil) != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FeedIdentityFromURL(%q) = %+v, %v", tt.feedURL, got, err)
			}
		})
	}
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func attrsMap(attrs []any) map[string]any {
	result := make(map[string]any, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if ok {
			result[key] = attrs[i+1]
		}
	}
	return result
}

func TestParseCategories(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want []string
	}{
		{" b, a,,b,  c , ", []string{"a", "b", "c"}},
		{"", nil},
	} {
		if got := ParseCategories(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("ParseCategories(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestRunCategoryFiltersAndMissingCounts(t *testing.T) {
	for _, tt := range []struct {
		name    string
		query   string
		include []string
		want    map[string]int
		count   int
	}{
		{"request overrides URL", "?include=article", []string{"answer"}, map[string]int{"answer": 2}, 2},
		{"URL fallback", "?include=article", nil, map[string]int{"article": 1}, 1},
		{"no filter", "", nil, map[string]int{"answer": 2, "article": 1}, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{knownCategories: []string{"article", "answer"}, candidates: []Candidate{
				{ID: "a1", Category: "answer"}, {ID: "a2", Category: "answer"}, {ID: "p1", Category: "article"},
			}}
			destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice" + tt.query}}
			result, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 1, IncludeCategories: tt.include}, Dependencies{Source: source, Destination: destination})
			if err != nil || result.Scanned != tt.count || result.Missing != tt.count || !reflect.DeepEqual(result.MissingByCategory, tt.want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(source.detailCalls) != 0 || len(source.commentCalls) != 0 || len(destination.imported) != 0 {
				t.Fatal("dry-run must not fetch details/comments or import")
			}
		})
	}
}

func TestRunRejectsInvalidCategoriesBeforeReadingEntriesAndDiscovery(t *testing.T) {
	for _, fromURL := range []bool{false, true} {
		source := &fakeSource{knownCategories: []string{"article", "answer"}}
		destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}
		req := Request{Action: ActionExecute, DryRun: true, FeedID: 1, IncludeCategories: []string{"invalid"}}
		if fromURL {
			req.IncludeCategories = nil
			destination.feed.FeedURL += "?include=invalid"
		}
		_, err := Run(context.Background(), req, Dependencies{Source: source, Destination: destination})
		if err == nil || !strings.Contains(err.Error(), "invalid") || !strings.Contains(err.Error(), "answer, article") {
			t.Fatalf("invalid category error = %v", err)
		}
		if source.discoverCalls != 0 || destination.entriesCalls != 0 {
			t.Fatalf("discover=%d entries=%d", source.discoverCalls, destination.entriesCalls)
		}
	}
}

func TestRunListKeepsFeedWithInvalidInclude(t *testing.T) {
	source := &fakeSource{knownCategories: []string{"answer"}}
	destination := &fakeDestination{feeds: []Feed{{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice?include=invalid"}}}
	result, err := Run(context.Background(), Request{Action: ActionList}, Dependencies{Source: source, Destination: destination})
	if err != nil || len(result.Feeds) != 1 || result.Feeds[0].ID != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// 未知类别照常导入：RSS 路径会发布这类条目，回填不得与之分叉。
func TestRunImportsUnknownCategoryAndUsesInlineContent(t *testing.T) {
	source := &fakeSource{knownCategories: []string{"answer"}, emptyComments: true, candidates: []Candidate{
		{ID: "known", Category: "answer", Content: "<p>inline</p>"},
		{ID: "unknown", Category: "unknown", Content: "<p>unknown</p>"},
	}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}, importErrs: map[string][]error{}}
	result, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{Source: source, Destination: destination})
	if err != nil || result.Scanned != 2 || result.Imported != 2 || len(result.Warnings) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(result.MissingByCategory, map[string]int{"answer": 1, "unknown": 1}) {
		t.Fatalf("missing_by_category=%v", result.MissingByCategory)
	}
	if len(source.detailCalls) != 0 || len(destination.imported) != 2 || destination.imported[1].ExternalID != "unknown" || destination.imported[0].Content != source.candidates[0].Content {
		t.Fatalf("detail=%v imports=%+v", source.detailCalls, destination.imported)
	}
}

// 显式 --include 仍然排除未知类别：用户点名了要哪些类别。
func TestRunExplicitIncludeStillExcludesUnknownCategory(t *testing.T) {
	source := &fakeSource{knownCategories: []string{"answer"}, candidates: []Candidate{
		{ID: "known", Category: "answer", Content: "<p>inline</p>"},
		{ID: "unknown", Category: "unknown", Content: "<p>unknown</p>"},
	}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}}
	result, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 1, IncludeCategories: []string{"answer"}},
		Dependencies{Source: source, Destination: destination})
	if err != nil || result.Scanned != 1 || result.Missing != 1 || !reflect.DeepEqual(result.MissingByCategory, map[string]int{"answer": 1}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunWithoutCategoriesIgnoresURLInclude(t *testing.T) {
	source := &fakeSource{candidates: []Candidate{{ID: "a1"}}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice?include=xxx"}}
	result, err := Run(context.Background(), Request{Action: ActionExecute, DryRun: true, FeedID: 1}, Dependencies{Source: source, Destination: destination})
	if err != nil || result.Scanned != 1 || result.Missing != 1 || len(result.MissingByCategory) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunExistingURLSkipsDifferentCandidateID(t *testing.T) {
	url := "https://www.zhihu.com/question/1/answer/2"
	source := &fakeSource{knownCategories: []string{"voteup_answer"}, candidates: []Candidate{{ID: "voteup_answer_2", Category: "voteup_answer", URL: url}}}
	destination := &fakeDestination{feed: Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"}, entries: []Entry{{URL: url, Hash: hashID("2")}}}
	result, err := Run(context.Background(), Request{Action: ActionExecute, FeedID: 1}, Dependencies{Source: source, Destination: destination})
	if err != nil || result.Existing != 1 || result.Missing != 0 || result.Imported != 0 || len(destination.imported) != 0 || len(source.detailCalls) != 0 || len(source.commentCalls) != 0 {
		t.Fatalf("result=%+v err=%v imports=%+v", result, err, destination.imported)
	}
}

func refreshTestDependencies() Dependencies {
	return Dependencies{
		Source: &fakeSource{
			refreshSupported: true,
			candidates:       []Candidate{{ID: "a", URL: "https://example.com/a", Title: "新标题", Content: "内联正文"}},
			detailErrs:       map[string][]error{}, commentErrs: map[string][]error{},
		},
		Destination: &fakeDestination{
			feed:    Feed{ID: 1, FeedURL: "http://rssgen/feed/afdian/alice"},
			entries: []Entry{{ID: 10, URL: "https://example.com/a"}},
		},
		Wait: noWait,
	}
}

func TestRunRefreshMatchesAndDryRun(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			deps := refreshTestDependencies()
			source := deps.Source.(*fakeSource)
			destination := deps.Destination.(*fakeDestination)
			source.candidates = append(source.candidates, Candidate{ID: "missing", URL: "https://example.com/missing"})
			result, err := Run(context.Background(), Request{Action: ActionRefresh, DryRun: dryRun, RangeConfirmed: true, FeedID: 1}, deps)
			if err != nil || len(destination.imported) != 0 || len(source.detailCalls) != 0 || result.Missing != 1 {
				t.Fatalf("result=%+v err=%v imports=%v details=%v", result, err, destination.imported, source.detailCalls)
			}
			if dryRun {
				if result.Updated != 0 || len(destination.updates) != 0 || len(source.commentCalls) != 0 || !reflect.DeepEqual(result.PendingUpdates, []UpdateTarget{{EntryID: 10, URL: "https://example.com/a"}}) {
					t.Fatalf("试跑发生写入或缺少清单：result=%+v updates=%v", result, destination.updates)
				}
			} else if result.Updated != 1 || !reflect.DeepEqual(destination.updates, []recordedUpdate{{10, "新标题", "内联正文\n\ncomments-a"}}) || !reflect.DeepEqual(source.commentCalls, []string{"a"}) {
				t.Fatalf("重刷正文或计数错误：result=%+v updates=%v comments=%v", result, destination.updates, source.commentCalls)
			}
		})
	}
}

func TestRunRefreshRejectsUnsupportedOrUnbounded(t *testing.T) {
	for _, supported := range []bool{false, true} {
		deps := refreshTestDependencies()
		source := deps.Source.(*fakeSource)
		source.refreshSupported = supported
		result, err := Run(context.Background(), Request{Action: ActionRefresh, FeedID: 1, RangeConfirmed: !supported}, deps)
		want := "不支持重刷"
		if supported {
			want = "扫描下界或正整数条数上限"
		}
		if err == nil || !strings.Contains(err.Error(), want) || source.discoverCalls != 0 || deps.Destination.(*fakeDestination).entriesCalls != 0 || result.Updated != 0 {
			t.Fatalf("应在读取与扫描前拒绝：result=%+v err=%v", result, err)
		}
	}
}

func TestRunRefreshHashPriorityAndUniqueEntry(t *testing.T) {
	deps := refreshTestDependencies()
	source := deps.Source.(*fakeSource)
	destination := deps.Destination.(*fakeDestination)
	source.emptyComments = true
	source.candidates = []Candidate{
		{ID: "older", URL: "shared", Title: "旧", Content: "旧正文", PublishedAt: time.Unix(1, 0)},
		{ID: "a", URL: "conflicting", Title: "新", Content: "新正文", PublishedAt: time.Unix(2, 0)},
	}
	destination.entries = []Entry{{ID: 20, URL: "conflicting"}, {ID: 10, URL: "shared", Hash: externalIDHash("a")}}
	result, err := Run(context.Background(), Request{Action: ActionRefresh, RangeConfirmed: true, FeedID: 1}, deps)
	if err != nil || result.Updated != 1 || !reflect.DeepEqual(destination.updates, []recordedUpdate{{10, "新", "新正文"}}) {
		t.Fatalf("必须哈希优先并按排序只更新一次：result=%+v updates=%v err=%v", result, destination.updates, err)
	}
}

func TestRunRefreshCommentFailureSkipsEntry(t *testing.T) {
	deps := refreshTestDependencies()
	source := deps.Source.(*fakeSource)
	destination := deps.Destination.(*fakeDestination)
	source.commentErrs["a"] = []error{errors.New("评论失败"), errors.New("评论失败"), errors.New("评论失败")}
	source.candidates = append(source.candidates, Candidate{ID: "b", URL: "b", Title: "详情标题"})
	destination.entries = append(destination.entries, Entry{ID: 11, URL: "b"})
	result, err := Run(context.Background(), Request{Action: ActionRefresh, RangeConfirmed: true, FeedID: 1}, deps)
	if err != nil || result.Updated != 1 || result.CommentFailures != 1 || len(result.UpdateFailures) != 1 || result.UpdateFailures[0].EntryID != 10 || result.UpdateFailures[0].Operation != "comments" || len(result.Warnings) != 0 || !reflect.DeepEqual(destination.updates, []recordedUpdate{{11, "详情标题", "body-b\n\ncomments-b"}}) {
		t.Fatalf("评论失败应跳过该条且继续：result=%+v updates=%v err=%v", result, destination.updates, err)
	}
}

func TestRunRefreshEmptyContentAndUpdateRetry(t *testing.T) {
	for _, empty := range []bool{true, false} {
		deps := refreshTestDependencies()
		source := deps.Source.(*fakeSource)
		destination := deps.Destination.(*fakeDestination)
		source.emptyComments = true
		if empty {
			source.candidates[0].Content = ""
			source.emptyDetail = true
		} else {
			destination.updateErrs = []error{context.DeadlineExceeded, nil}
		}
		result, err := Run(context.Background(), Request{Action: ActionRefresh, RangeConfirmed: true, FeedID: 1}, deps)
		if empty {
			if err == nil || !strings.Contains(err.Error(), "正文为空") || result.Updated != 0 || len(destination.updates) != 0 || len(result.UpdateFailures) != 1 {
				t.Fatalf("空正文不得计成功：result=%+v err=%v", result, err)
			}
		} else if err != nil || result.Updated != 1 || len(destination.updates) != 2 || len(result.UpdateFailures) != 0 {
			t.Fatalf("单次超时应重试且只计一次成功：result=%+v err=%v", result, err)
		}
	}
}

func TestRunRefreshCancellation(t *testing.T) {
	for _, phase := range []string{"scan", "comments", "update"} {
		t.Run(phase, func(t *testing.T) {
			deps := refreshTestDependencies()
			source := deps.Source.(*fakeSource)
			destination := deps.Destination.(*fakeDestination)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch phase {
			case "scan":
				source.discoverCancel = cancel
				source.scanInterrupted = true
				source.scanTruncated = true
			case "comments":
				source.commentCancel = cancel
			case "update":
				destination.updateCancel = cancel
			}
			result, err := Run(ctx, Request{Action: ActionRefresh, RangeConfirmed: true, FeedID: 1}, deps)
			if phase == "scan" {
				if err != nil || result.Updated != 1 || !result.ScanInterrupted {
					t.Fatalf("扫描中断应写完已扫部分：%+v %v", result, err)
				}
			} else if !errors.Is(err, context.Canceled) || result.Updated != 0 || result.CommentFailures != 0 || len(result.Warnings) != 0 {
				t.Fatalf("取消不能计作成功或评论告警：%+v %v", result, err)
			}
		})
	}
}

func TestRunArchivesBeforeWrite(t *testing.T) {
	for _, action := range []Action{ActionExecute, ActionRefresh} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/失败=%v", action, fail), func(t *testing.T) {
				deps := refreshTestDependencies()
				source := deps.Source.(*fakeSource)
				destination := deps.Destination.(*fakeDestination)
				destination.importErrs = map[string][]error{}
				published := time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC)
				source.candidates[0].PublishedAt = published
				source.candidates = append(source.candidates, Candidate{ID: "b", URL: "https://example.com/b", Content: "较早正文", PublishedAt: published.Add(-time.Hour)})
				if action == ActionExecute {
					destination.entries = nil
				} else {
					destination.entries = append(destination.entries, Entry{ID: 11, URL: "https://example.com/b"})
				}
				archiveErr := errors.New("归档失败")
				var calls []string
				deps.Archive = func(content, link string, date time.Time) (string, error) {
					calls = append(calls, link)
					candidate := source.candidates[len(calls)-1]
					if content != candidate.Content+"\n\ncomments-"+candidate.ID || link != candidate.URL || !date.Equal(candidate.PublishedAt) {
						t.Errorf("归档入参不符：%q %q %v", content, link, date)
					}
					if fail {
						return "", archiveErr
					}
					return "归档后-" + candidate.ID, nil
				}
				result, err := Run(context.Background(), Request{Action: action, RangeConfirmed: true, FeedID: 1}, deps)
				if fail {
					if !errors.Is(err, archiveErr) || len(calls) != 1 || len(destination.imported) != 0 || len(destination.updates) != 0 || len(source.commentCalls) != 1 {
						t.Fatalf("失败未立即中止：%+v err=%v calls=%v", result, err, calls)
					}
					if action == ActionRefresh {
						if len(result.UpdateFailures) != 1 || result.UpdateFailures[0].Operation != "media" || result.UpdateFailures[0].CandidateID != "a" || !errors.Is(result.UpdateFailures[0].Err, archiveErr) {
							t.Fatalf("失败记录错误：%+v", result.UpdateFailures)
						}
					} else if !strings.Contains(err.Error(), "归档文章 a 的图片") {
						t.Fatalf("错误上下文缺失：%v", err)
					}
					return
				}
				if err != nil || len(calls) != 2 {
					t.Fatalf("归档失败：%v calls=%v", err, calls)
				}
				if action == ActionExecute {
					if len(destination.imported) != 2 || destination.imported[0].Content != "归档后-a" || destination.imported[1].Content != "归档后-b" {
						t.Fatalf("导入正文错误：%+v", destination.imported)
					}
				} else if len(destination.updates) != 2 || destination.updates[0].Content != "归档后-a" || destination.updates[1].Content != "归档后-b" {
					t.Fatalf("更新正文错误：%+v", destination.updates)
				}
			})
		}
	}
}
