package zhihu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/config"
)

func newBackfillTestSource() *BackfillSource {
	return &BackfillSource{route: New(config.ResolvedRouteConfig{Cookie: `d_c0="abc";`, RateLimit: 0.001})}
}

func backfillTestActivity(id, verb, targetType string) zhihuActivity {
	return zhihuActivity{
		Verb: verb, ActionText: "动态", CreatedTime: 123,
		Actor: &zhihuPerson{ID: "reader"},
		Target: &zhihuTarget{
			ID: zhihuID(id), Type: targetType, Title: "标题", ExcerptTitle: "想法",
			Content:  json.RawMessage(`"<p>正文</p>"`),
			Author:   &zhihuPerson{ID: "author", Name: "作者"},
			Question: &zhihuQuestion{ID: "q", Title: "问题", Detail: "<p>问题描述</p>"},
		},
	}
}

func TestBackfillPaginationStops(t *testing.T) {
	for _, stop := range []string{"is_end", "empty next", "empty data", "repeated next"} {
		t.Run(stop, func(t *testing.T) {
			requests := 0
			var serverURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests > 1 {
					t.Error("unexpected request after terminal page")
				}
				data := []zhihuActivity{backfillTestActivity("1", "MEMBER_CREATE_ARTICLE", "article")}
				paging := zhihuPaging{Next: serverURL + "/api/v3/moments/alice/activities?limit=5&desktop=true"}
				switch stop {
				case "is_end":
					paging.IsEnd = true
				case "empty next":
					paging.Next = ""
				case "empty data":
					data = nil
				}
				if requests > 1 {
					paging.IsEnd = true
				}
				writeZhihuJSON(t, w, activitiesResponse{Data: data, Paging: paging})
			}))
			defer server.Close()
			serverURL = server.URL
			withZhihuTestHost(t, server.URL)
			got, err := newBackfillTestSource().Discover(context.Background(), "alice", nil)
			want := 1
			if stop == "empty data" {
				want = 0
			}
			if err != nil || requests != 1 || len(got) != want {
				t.Fatalf("Discover count=%d requests=%d err=%v", len(got), requests, err)
			}
		})
	}
}

func TestBackfillDiscoverAllPages(t *testing.T) {
	requests := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 3 {
			t.Error("unexpected fourth page")
		}
		data := make([]zhihuActivity, 4)
		for i := range data {
			data[i] = backfillTestActivity(fmt.Sprint((requests-1)*4+i), "MEMBER_CREATE_ARTICLE", "article")
		}
		if requests == 1 {
			data[1] = backfillTestActivity("unknown", "UNKNOWN_VERB", "unknown_type")
		}
		writeZhihuJSON(t, w, activitiesResponse{Data: data, Paging: zhihuPaging{
			IsEnd: requests >= 3, Next: fmt.Sprintf("%s/page/%d", serverURL, requests+1),
		}})
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)
	var pages []string
	got, err := newBackfillTestSource().Discover(context.Background(), "alice", func(message string, attrs ...any) {
		pages = append(pages, message+" "+fmt.Sprint(attrs))
	})
	if err != nil || requests != 3 || len(got) != 12 || got[0].ID != "0" || got[11].ID != "11" {
		t.Fatalf("Discover count=%d requests=%d err=%v", len(got), requests, err)
	}
	want := []string{
		"知乎历史扫描进度 [source_id alice page 1 page_items 4 scanned 4]",
		"知乎历史扫描进度 [source_id alice page 2 page_items 4 scanned 8]",
		"知乎历史扫描进度 [source_id alice page 3 page_items 4 scanned 12]",
	}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("progress=%v", pages)
	}

	// 用同一组完整分页验证 dry-run 的现有条目、未知类别放行和缺失统计。
	requests = 0
	destination := &backfillTestDestination{entries: []backfill.Entry{{URL: "https://zhuanlan.zhihu.com/p/0"}}}
	result, err := backfill.Run(context.Background(), backfill.Request{Action: backfill.ActionDryRun, FeedID: 1}, backfill.Dependencies{
		Source: newBackfillTestSource(), Destination: destination,
	})
	if err != nil || requests != 3 || result.Scanned != 12 || result.Existing != 1 || result.Missing != 11 || !reflect.DeepEqual(result.MissingByCategory, map[string]int{"article": 10, "unknown_type": 1}) || len(destination.imported) != 0 {
		t.Fatalf("dry-run result=%+v requests=%d err=%v imports=%v", result, requests, err, destination.imported)
	}
}

// 上游永不返回 is_end 时，maxItems 是唯一的终止条件。
func TestBackfillStopsAtMaxItems(t *testing.T) {
	requests := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 4 {
			t.Error("max items did not stop pagination")
		}
		data := make([]zhihuActivity, 4)
		for i := range data {
			data[i] = backfillTestActivity(fmt.Sprint((requests-1)*4+i), "MEMBER_CREATE_ARTICLE", "article")
		}
		writeZhihuJSON(t, w, activitiesResponse{Data: data, Paging: zhihuPaging{
			Next: fmt.Sprintf("%s/page/%d", serverURL, requests+1),
		}})
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)
	source := newBackfillTestSource()
	source.maxItems = 6
	var messages []string
	got, err := source.Discover(context.Background(), "alice", func(message string, attrs ...any) {
		messages = append(messages, message)
	})
	if err != nil || requests != 2 || len(got) != 6 {
		t.Fatalf("candidates=%d requests=%d err=%v", len(got), requests, err)
	}
	if len(messages) == 0 || messages[len(messages)-1] != "已达知乎历史扫描上限，更早的历史未被扫描" {
		t.Fatalf("messages=%v", messages)
	}
}

func TestBackfillMatchesRSSIdentityAndContent(t *testing.T) {
	types := []struct{ verb, targetType, category string }{
		{"MEMBER_ANSWER_QUESTION", "answer", TypeAnswer},
		{"MEMBER_CREATE_ARTICLE", "article", TypeArticle},
		{"MEMBER_CREATE_PIN", "pin", TypePin},
		{"MEMBER_COLLECT_ANSWER", "answer", TypeCollectedAnswer},
		{"MEMBER_COLLECT_ARTICLE", "article", TypeCollectedArticle},
		{"MEMBER_COLLECT_PIN", "pin", TypeCollectedPin},
		{"MEMBER_VOTEUP_ANSWER", "answer", TypeVoteupAnswer},
		{"MEMBER_VOTEUP_ARTICLE", "article", TypeVoteupArticle},
		{"MEMBER_FOLLOW_QUESTION", "question", TypeFollowedQuestion},
	}
	activities := make([]zhihuActivity, len(types))
	for i, tt := range types {
		activities[i] = backfillTestActivity(fmt.Sprint(i+1), tt.verb, tt.targetType)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeZhihuJSON(t, w, activitiesResponse{Data: activities, Paging: zhihuPaging{IsEnd: true}})
	}))
	defer server.Close()
	withZhihuTestHost(t, server.URL)
	source := newBackfillTestSource()
	got, err := source.Discover(context.Background(), "alice", nil)
	if err != nil || len(got) != len(activities) {
		t.Fatalf("Discover count=%d err=%v", len(got), err)
	}
	for i, candidate := range got {
		item := source.route.makeFeedItem(activities[i])
		wantID := fmt.Sprint(i + 1)
		if i >= 3 {
			wantID = types[i].category + "_" + wantID
		}
		if candidate.ID != item.GUID || candidate.ID != wantID || candidate.Content == "" || candidate.Content != item.Content || candidate.Title != item.Title || candidate.URL != item.Link || candidate.Author != item.Author || candidate.Category != types[i].category || item.PubDate == nil || !candidate.PublishedAt.Equal(*item.PubDate) {
			t.Fatalf("candidate=%+v item=%+v wantID=%s", candidate, item, wantID)
		}
	}
}

func TestBackfillSelfInteraction(t *testing.T) {
	activities := []zhihuActivity{
		backfillTestActivity("1", "MEMBER_VOTEUP_ANSWER", "answer"),
		backfillTestActivity("2", "MEMBER_COLLECT_ARTICLE", "article"),
		backfillTestActivity("3", "MEMBER_CREATE_ARTICLE", "article"),
		{Target: nil},
	}
	for i := 0; i < 3; i++ {
		activities[i].Actor.ID = activities[i].Target.Author.ID
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeZhihuJSON(t, w, activitiesResponse{Data: activities, Paging: zhihuPaging{IsEnd: true}})
	}))
	defer server.Close()
	withZhihuTestHost(t, server.URL)
	for _, include := range []bool{false, true} {
		source := newBackfillTestSource()
		source.includeSelfInteraction = include
		got, err := source.Discover(context.Background(), "alice", nil)
		want := 1
		if include {
			want = 3
		}
		if err != nil || len(got) != want || got[len(got)-1].ID != "3" {
			t.Fatalf("include=%v candidates=%+v err=%v", include, got, err)
		}
	}
}

func TestBackfillConstructionIdentityAndNoOpMethods(t *testing.T) {
	if _, err := NewBackfillSource(`d_c0="abc";`, time.Second-time.Millisecond, false, 0); err == nil {
		t.Fatal("interval below one second must fail")
	}
	if _, err := NewBackfillSource(`d_c0="abc";`, time.Second, false, -1); err == nil {
		t.Fatal("negative max items must fail")
	}
	if _, err := NewBackfillSource("other=secret", time.Second, false, 0); err == nil || !strings.Contains(err.Error(), "d_c0") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("missing d_c0 error=%v", err)
	}
	for _, cookie := range []string{"", `d_c0="abc";`} {
		source, err := NewBackfillSource(cookie, time.Second, false, 0)
		if err != nil {
			t.Fatalf("NewBackfillSource: %v", err)
		}
		identity, err := source.FeedIdentity("http://rssgen/feed/zhihu/alice?include=voteup_answer,answer")
		if err != nil || identity.SourceID != "alice" || !reflect.DeepEqual(identity.Categories, []string{"answer", "voteup_answer"}) {
			t.Fatalf("identity=%+v err=%v", identity, err)
		}
		want := []string{TypeAnswer, TypeArticle, TypePin, TypeCollectedAnswer, TypeCollectedArticle, TypeCollectedPin, TypeVoteupAnswer, TypeVoteupArticle, TypeFollowedQuestion}
		if !reflect.DeepEqual(source.KnownCategories(), want) {
			t.Fatalf("categories=%v", source.KnownCategories())
		}
		if body, err := source.Detail(context.Background(), backfill.Candidate{}); err != nil || body != "" {
			t.Fatalf("Detail=%q, %v", body, err)
		}
		if body, err := source.Comments(context.Background(), backfill.Candidate{}); err != nil || body != "" {
			t.Fatalf("Comments=%q, %v", body, err)
		}
	}
}

// backfillTestDestination 记录完整 Run 流程实际交给目的端的正文。
type backfillTestDestination struct {
	entries  []backfill.Entry
	imported []backfill.Article
}

func (d *backfillTestDestination) Feeds(context.Context) ([]backfill.Feed, error) {
	return []backfill.Feed{{ID: 1, FeedURL: "http://rssgen/feed/zhihu/alice"}}, nil
}
func (d *backfillTestDestination) Feed(context.Context, int64) (backfill.Feed, error) {
	return backfill.Feed{ID: 1, FeedURL: "http://rssgen/feed/zhihu/alice"}, nil
}
func (d *backfillTestDestination) Entries(context.Context, int64) ([]backfill.Entry, error) {
	return d.entries, nil
}
func (d *backfillTestDestination) Import(_ context.Context, _ int64, article backfill.Article) (backfill.ImportOutcome, error) {
	d.imported = append(d.imported, article)
	return backfill.ImportCreated, nil
}

func TestBackfillImportsEmptyAndInlineContentAndUnknownType(t *testing.T) {
	activities := []zhihuActivity{
		backfillTestActivity("empty", "MEMBER_CREATE_ARTICLE", "article"),
		backfillTestActivity("inline", "MEMBER_ANSWER_QUESTION", "answer"),
		backfillTestActivity("unknown", "UNKNOWN_VERB", "unknown_type"),
	}
	activities[0].Target.Content = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeZhihuJSON(t, w, activitiesResponse{Data: activities, Paging: zhihuPaging{IsEnd: true}})
	}))
	defer server.Close()
	withZhihuTestHost(t, server.URL)
	source := newBackfillTestSource()
	destination := &backfillTestDestination{}
	result, err := backfill.Run(context.Background(), backfill.Request{Action: backfill.ActionExecute, FeedID: 1}, backfill.Dependencies{Source: source, Destination: destination})
	if err != nil || result.Imported != 3 || result.Scanned != 3 || len(result.Warnings) != 0 || len(destination.imported) != 3 {
		t.Fatalf("result=%+v err=%v imports=%+v", result, err, destination.imported)
	}
	for i, article := range destination.imported {
		item := source.route.makeFeedItem(activities[i])
		if article.Content != item.Content || article.ExternalID != item.GUID {
			t.Fatalf("article=%+v item=%+v", article, item)
		}
	}
	if destination.imported[0].Content != "" || destination.imported[1].Content == "" {
		t.Fatalf("imports=%+v", destination.imported)
	}
}
