package feedcatalog

import (
	"fmt"
	"testing"

	"github.com/PhiFever/RSSGen/internal/pipeline"
)

func TestCatalogObserveListAndDeduplicate(t *testing.T) {
	cat := New(10)

	ref := pipeline.FeedRef{
		RouteName: "zhihu",
		FeedID:    "u1",
		CacheKey:  "zhihu/u1?format=atom&limit=20",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20, Include: []string{"answer"}},
	}

	cat.Observe(ref)
	ref.Variant.Include[0] = "mutated"
	cat.Observe(pipeline.FeedRef{
		RouteName: "zhihu",
		FeedID:    "u1",
		CacheKey:  "zhihu/u1?format=atom&limit=20",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20, Include: []string{"article"}},
	})

	got := cat.List("zhihu")
	if len(got) != 1 {
		t.Fatalf("List len = %d, want 1", len(got))
	}
	if fmt.Sprint(got[0].Variant.Include) != "[article]" {
		t.Fatalf("Include = %v, want [article]", got[0].Variant.Include)
	}
}

func TestCatalogEvictsLeastRecentlyUsedPerRoute(t *testing.T) {
	cat := New(2)
	cat.Observe(ref("zhihu", "u1"))
	cat.Observe(ref("zhihu", "u2"))
	cat.Observe(ref("afdian", "a1"))
	cat.Observe(ref("zhihu", "u1"))
	cat.Observe(ref("zhihu", "u3"))

	got := cat.List("zhihu")
	if len(got) != 2 {
		t.Fatalf("zhihu len = %d, want 2", len(got))
	}
	if got[0].FeedID != "u1" || got[1].FeedID != "u3" {
		t.Fatalf("zhihu refs = %+v, want u1 then u3", got)
	}

	afdian := cat.List("afdian")
	if len(afdian) != 1 || afdian[0].FeedID != "a1" {
		t.Fatalf("afdian refs = %+v, want a1", afdian)
	}
}

func TestCatalogLimitZeroDisablesObservation(t *testing.T) {
	cat := New(0)
	cat.Observe(ref("zhihu", "u1"))
	if got := cat.List("zhihu"); len(got) != 0 {
		t.Fatalf("List len = %d, want 0", len(got))
	}
}

func ref(routeName, feedID string) pipeline.FeedRef {
	return pipeline.FeedRef{
		RouteName: routeName,
		FeedID:    feedID,
		PathParts: []string{feedID},
		CacheKey:  routeName + "/" + feedID + "?format=atom&limit=20",
		Variant:   pipeline.FeedVariant{Format: "atom", Limit: 20},
	}
}
