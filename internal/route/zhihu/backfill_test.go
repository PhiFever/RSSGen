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
			scan, err := newBackfillTestSource().Discover(context.Background(), "alice", nil)
			want := 1
			if stop == "empty data" {
				want = 0
			}
			if err != nil || requests != 1 || len(scan.Candidates) != want {
				t.Fatalf("Discover count=%d requests=%d err=%v", len(scan.Candidates), requests, err)
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
	scan, err := newBackfillTestSource().Discover(context.Background(), "alice", func(message string, attrs ...any) {
		pages = append(pages, message+" "+fmt.Sprint(attrs))
	})
	if err != nil || requests != 3 || len(scan.Candidates) != 12 || scan.Candidates[0].ID != "0" || scan.Candidates[11].ID != "11" {
		t.Fatalf("Discover count=%d requests=%d err=%v", len(scan.Candidates), requests, err)
	}
	want := []string{
		"知乎历史扫描进度 [source_id alice page 1 page_items 4 scanned 4 next_offset ]",
		"知乎历史扫描进度 [source_id alice page 2 page_items 4 scanned 8 next_offset ]",
		"知乎历史扫描进度 [source_id alice page 3 page_items 4 scanned 12 next_offset ]",
	}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("progress=%v", pages)
	}

	// 用同一组完整分页验证 dry-run 的现有条目和缺失统计。feed URL 未带 include，
	// 默认只回填创作类，12 条候选里那条 unknown_type 因此不计入。
	requests = 0
	destination := &backfillTestDestination{entries: []backfill.Entry{{URL: "https://zhuanlan.zhihu.com/p/0"}}}
	result, err := backfill.Run(context.Background(), backfill.Request{Action: backfill.ActionExecute, DryRun: true, FeedID: 1}, backfill.Dependencies{
		Source: newBackfillTestSource(), Destination: destination,
	})
	if err != nil || requests != 3 || result.Scanned != 11 || result.Existing != 1 || result.Missing != 10 || !reflect.DeepEqual(result.MissingByCategory, map[string]int{"article": 10}) || len(destination.imported) != 0 {
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
	scan, err := source.Discover(context.Background(), "alice", nil)
	if err != nil || requests != 2 || len(scan.Candidates) != 6 {
		t.Fatalf("candidates=%d requests=%d err=%v", len(scan.Candidates), requests, err)
	}
	if !scan.Truncated {
		t.Fatalf("达到扫描上限却没有报告截断，scan=%+v", scan)
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
	scan, err := source.Discover(context.Background(), "alice", nil)
	if err != nil || len(scan.Candidates) != len(activities) {
		t.Fatalf("Discover count=%d err=%v", len(scan.Candidates), err)
	}
	for i, candidate := range scan.Candidates {
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
		scan, err := source.Discover(context.Background(), "alice", nil)
		want := 1
		if include {
			want = 3
		}
		if err != nil || len(scan.Candidates) != want || scan.Candidates[len(scan.Candidates)-1].ID != "3" {
			t.Fatalf("include=%v candidates=%+v err=%v", include, scan.Candidates, err)
		}
	}
}

func TestBackfillConstructionIdentityAndNoOpMethods(t *testing.T) {
	if _, err := NewBackfillSource(`d_c0="abc";`, time.Second-time.Millisecond, false, 0, 0, 0); err == nil {
		t.Fatal("interval below one second must fail")
	}
	if _, err := NewBackfillSource(`d_c0="abc";`, time.Second, false, -1, 0, 0); err == nil {
		t.Fatal("negative max items must fail")
	}
	if _, err := NewBackfillSource("other=secret", time.Second, false, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "d_c0") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("missing d_c0 error=%v", err)
	}
	for _, cookie := range []string{"", `d_c0="abc";`} {
		source, err := NewBackfillSource(cookie, time.Second, false, 0, 0, 0)
		if err != nil {
			t.Fatalf("NewBackfillSource: %v", err)
		}
		identity, err := source.FeedIdentity("http://rssgen/feed/zhihu/alice?include=voteup_answer,answer")
		if err != nil || identity.SourceID != "alice" || !reflect.DeepEqual(identity.Categories, []string{"answer", "voteup_answer"}) {
			t.Fatalf("identity=%+v err=%v", identity, err)
		}
		// feed URL 未带 include 时回落到创作类，而不是不过滤。
		bare, err := source.FeedIdentity("http://rssgen/feed/zhihu/alice")
		if err != nil || !reflect.DeepEqual(bare.Categories, []string{TypeAnswer, TypeArticle, TypePin}) {
			t.Fatalf("默认类别 identity=%+v err=%v", bare, err)
		}
		want := []string{TypeAnswer, TypeArticle, TypePin, TypeCollectedAnswer, TypeCollectedArticle, TypeCollectedPin, TypeVoteupAnswer, TypeVoteupArticle, TypeFollowedQuestion, TypeFollowedCollection, TypeVoteupPin, TypeAskedQuestion}
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
	updated []struct {
		EntryID        int64
		Title, Content string
	}
	entries  []backfill.Entry
	imported []backfill.Article
}

func (d *backfillTestDestination) Update(_ context.Context, entryID int64, title, content string) error {
	d.updated = append(d.updated, struct {
		EntryID        int64
		Title, Content string
	}{entryID, title, content})
	return nil
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

func TestBackfillImportsEmptyAndInlineContentAndSkipsUnknownType(t *testing.T) {
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
	// 默认类别不含 unknown_type，第三条被过滤；前两条的索引仍与 activities 对齐。
	result, err := backfill.Run(context.Background(), backfill.Request{Action: backfill.ActionExecute, FeedID: 1}, backfill.Dependencies{Source: source, Destination: destination})
	if err != nil || result.Imported != 2 || result.Scanned != 2 || len(result.Warnings) != 0 || len(destination.imported) != 2 {
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

// 截断丢弃的条目不能被游标跳过：按窗口续扫时最后一页会被 maxItems 截断，
// 若续扫游标指向整页末尾，被丢弃的那几条将永远不会进入任何窗口。
func TestBackfillDiscoverTruncationCursorCoversDroppedItems(t *testing.T) {
	requests := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		data := make([]zhihuActivity, 4)
		for i := range data {
			seq := (requests-1)*4 + i
			act := backfillTestActivity(fmt.Sprint(seq), "MEMBER_CREATE_ARTICLE", "article")
			act.ID = zhihuID(fmt.Sprint(2000 - seq))
			data[i] = act
		}
		lastID := 2000 - ((requests-1)*4 + 3)
		writeZhihuJSON(t, w, activitiesResponse{Data: data, Paging: zhihuPaging{
			Next: fmt.Sprintf("%s/api/v3/moments/alice/activities?offset=%d&page_num=%d", serverURL, lastID, requests+1),
		}})
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)

	source := newBackfillTestSource()
	source.maxItems = 6
	scan, err := source.Discover(context.Background(), "alice", nil)
	if err != nil || len(scan.Candidates) != 6 {
		t.Fatalf("Discover count=%d err=%v", len(scan.Candidates), err)
	}
	if !scan.Truncated {
		t.Fatalf("达到扫描上限却没有报告截断，scan=%+v", scan)
	}
	// 第 6 条（最后一条被保留的）activity id = 2000-5 = 1995；整页末尾是 2000-7 = 1993。
	if scan.NextCursor != "1995" {
		t.Errorf("续扫游标应为最后一条保留条目的 id 1995（而非整页末尾 1993），实际 %q", scan.NextCursor)
	}
}

func TestBackfillDiscoverInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		id := fmt.Sprint(100 - requests)
		activity := backfillTestActivity(id, "MEMBER_CREATE_ARTICLE", "article")
		activity.ID = zhihuID(id)
		if requests == 2 {
			cancel()
		}
		writeZhihuJSON(t, w, activitiesResponse{
			Data:   []zhihuActivity{activity},
			Paging: zhihuPaging{IsEnd: requests >= 3, Next: fmt.Sprintf("%s/page/%d", serverURL, requests+1)},
		})
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)

	scan, err := newBackfillTestSource().Discover(ctx, "alice", nil)
	if err != nil || !scan.Interrupted || !scan.Truncated || requests != 2 || len(scan.Candidates) != 2 {
		t.Fatalf("Discover scan=%+v requests=%d err=%v", scan, requests, err)
	}
	if scan.Candidates[0].ID != "99" || scan.Candidates[1].ID != "98" || scan.NextCursor != "98" {
		t.Fatalf("Discover scan=%+v", scan)
	}
}

func TestBackfillDiscoverStopAfter(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		stopAfter               int64
		wantItems, wantRequests int
	}{
		{"页内裁剪并停止", 1781935272669, 2, 1},
		{"零下界保持完整扫描", 0, 5, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			var serverURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var data []zhihuActivity
				if requests == 1 {
					for i, cursor := range []string{"1781935272670", "1781935272669", "1781935272668", "1781935272667"} {
						act := backfillTestActivity(fmt.Sprint(i), "MEMBER_VOTEUP_ANSWER", "answer")
						act.ID = zhihuID(cursor)
						act.Target.CreatedTime = 1
						data = append(data, act)
					}
				} else {
					// 零下界时继续接纳没有动态游标的既有格式。
					data = []zhihuActivity{backfillTestActivity("4", "MEMBER_CREATE_ARTICLE", "article")}
				}
				writeZhihuJSON(t, w, activitiesResponse{Data: data, Paging: zhihuPaging{
					IsEnd: requests >= 2, Next: serverURL + "/page/2",
				}})
			}))
			defer server.Close()
			serverURL = server.URL
			withZhihuTestHost(t, server.URL)
			source, err := NewBackfillSource(`d_c0="abc";`, time.Second, false, 0, 0, tt.stopAfter)
			if err != nil {
				t.Fatal(err)
			}
			var pageItems []int
			scan, err := source.Discover(context.Background(), "alice", func(_ string, attrs ...any) {
				pageItems = append(pageItems, attrs[5].(int))
			})
			if err != nil || requests != tt.wantRequests || len(scan.Candidates) != tt.wantItems {
				t.Fatalf("候选数=%d 请求数=%d err=%v", len(scan.Candidates), requests, err)
			}
			if scan.Truncated || scan.NextCursor != "" {
				t.Fatalf("正常完成扫描不应报告截断：%+v", scan)
			}
			if scan.Candidates[0].URL != serverURL+"/question/q/answer/0" || scan.Candidates[1].URL != serverURL+"/question/q/answer/1" {
				t.Fatalf("应保留新动态和等于下界的动态：%+v", scan.Candidates)
			}
			if tt.stopAfter != 0 && !reflect.DeepEqual(pageItems, []int{2}) {
				t.Fatalf("页内裁剪后的进度=%v", pageItems)
			}
		})
	}
}

func TestBackfillDiscoverStopAfterMissingCursor(t *testing.T) {
	requests := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeZhihuBody(t, w, fmt.Sprintf(`{"data":[{"id":null,"target":{"id":"1781935272670","type":"article"}}],"paging":{"is_end":false,"next":%q}}`, serverURL+"/page/2"))
	}))
	defer server.Close()
	serverURL = server.URL
	withZhihuTestHost(t, server.URL)
	source := newBackfillTestSource()
	source.stopAfter = 1781935272669
	scan, err := source.Discover(context.Background(), "alice", nil)
	if err == nil || !strings.Contains(err.Error(), "动态游标无效") || requests != 1 || len(scan.Candidates) != 0 {
		t.Fatalf("缺失动态游标必须报错且停止：scan=%+v requests=%d err=%v", scan, requests, err)
	}
}

func TestBackfillSupportsRefresh(t *testing.T) {
	if !newBackfillTestSource().SupportsRefresh() {
		t.Fatal("知乎应支持重刷")
	}
}
