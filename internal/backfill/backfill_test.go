package backfill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeSource struct {
	candidates    []Candidate
	discoverErrs  []error
	detailErrs    map[string][]error
	commentErrs   map[string][]error
	detailCalls   []string
	commentCalls  []string
	discoverCalls int
	reportPage    bool
}

func (s *fakeSource) FeedIdentity(feedURL string) (string, error) {
	return FeedIdentityFromURL(feedURL, "afdian")
}

func (s *fakeSource) Discover(_ context.Context, _ string, progress ProgressFunc) ([]Candidate, error) {
	s.discoverCalls++
	if err := popError(&s.discoverErrs); err != nil {
		return nil, err
	}
	if s.reportPage {
		reportProgress(progress, "数据源分页扫描进度", "page", 1, "page_items", len(s.candidates), "scanned", len(s.candidates))
	}
	return append([]Candidate(nil), s.candidates...), nil
}

func (s *fakeSource) Detail(_ context.Context, candidate Candidate) (string, error) {
	s.detailCalls = append(s.detailCalls, candidate.ID)
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
	errs := s.commentErrs[candidate.ID]
	if err := popError(&errs); err != nil {
		s.commentErrs[candidate.ID] = errs
		return "", err
	}
	s.commentErrs[candidate.ID] = errs
	return "comments-" + candidate.ID, nil
}

type fakeDestination struct {
	feeds          []Feed
	feed           Feed
	entries        []Entry
	imported       []Article
	importOutcomes map[string]ImportOutcome
	importErrs     map[string][]error
}

func (d *fakeDestination) Feeds(context.Context) ([]Feed, error) {
	return append([]Feed(nil), d.feeds...), nil
}

func (d *fakeDestination) Feed(context.Context, int64) (Feed, error) {
	return d.feed, nil
}

func (d *fakeDestination) Entries(context.Context, int64) ([]Entry, error) {
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

	result, err := Run(context.Background(), Request{Action: ActionDryRun, FeedID: 42}, Dependencies{
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

	_, err := Run(context.Background(), Request{Action: ActionDryRun, FeedID: 1}, Dependencies{
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
		want    string
		ok      bool
	}{
		{"plain", "http://rssgen:8000/feed/afdian/alice", "alice", true},
		{"escaped", "https://rss.example/feed/afdian/%E7%88%B1", "爱", true},
		{"wrong route", "https://rss.example/feed/zhihu/alice", "", false},
		{"missing slug", "https://rss.example/feed/afdian", "", false},
		{"extra segment", "https://rss.example/feed/afdian/alice/more", "", false},
		{"encoded slash", "https://rss.example/feed/afdian/a%2Fb", "", false},
		{"relative", "/feed/afdian/alice", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FeedIdentityFromURL(tt.feedURL, "afdian")
			if (err == nil) != tt.ok || got != tt.want {
				t.Fatalf("FeedIdentityFromURL(%q) = %q, %v", tt.feedURL, got, err)
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
