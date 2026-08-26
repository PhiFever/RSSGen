package miniflux

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/backfill"
)

func TestClientFeedsFeedAndPaginatedEntries(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != "secret-token" {
			t.Errorf("X-Auth-Token = %q", r.Header.Get("X-Auth-Token"))
		}
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/v1/feeds":
			writeJSON(t, w, []map[string]any{{"id": 42, "title": "Alice", "feed_url": "http://rssgen/feed/afdian/alice"}})
		case "/v1/feeds/42":
			writeJSON(t, w, map[string]any{"id": 42, "title": "Alice", "feed_url": "http://rssgen/feed/afdian/alice"})
		case "/v1/feeds/42/entries":
			for _, status := range r.URL.Query()["status"] {
				if status == "removed" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error_message":"invalid entry status, valid status values are: \"read\" and \"unread\""}`))
					return
				}
			}
			assertEntryStatuses(t, r.URL.Query())
			offset := r.URL.Query().Get("offset")
			if offset == "0" {
				writeJSON(t, w, map[string]any{"total": 3, "entries": []map[string]any{
					{"url": "https://afdian.com/p/1", "hash": "h1"},
					{"url": "https://afdian.com/p/2", "hash": "h2"},
				}})
				return
			}
			if offset == "2" {
				writeJSON(t, w, map[string]any{"total": 3, "entries": []map[string]any{
					{"url": "https://afdian.com/p/3", "hash": "h3"},
				}})
				return
			}
			t.Fatalf("unexpected offset %q", offset)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "secret-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.pageSize = 2

	feeds, err := client.Feeds(context.Background())
	if err != nil || len(feeds) != 1 || feeds[0].ID != 42 {
		t.Fatalf("Feeds = %+v, %v", feeds, err)
	}
	feed, err := client.Feed(context.Background(), 42)
	if err != nil || feed.Title != "Alice" {
		t.Fatalf("Feed = %+v, %v", feed, err)
	}
	entries, err := client.Entries(context.Background(), 42)
	if err != nil || len(entries) != 3 || entries[2].Hash != "h3" {
		t.Fatalf("Entries = %+v, %v", entries, err)
	}
	if len(paths) != 4 || paths[0] != "/v1/feeds" || paths[1] != "/v1/feeds/42" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestClientImportDistinguishesCreatedAndExisting(t *testing.T) {
	statuses := []int{http.StatusCreated, http.StatusOK}
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/feeds/42/entries/import" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["url"] != "https://afdian.com/p/post-1" || body["external_id"] != "post-1" || body["status"] != "unread" {
			t.Fatalf("body = %+v", body)
		}
		if body["published_at"] != float64(123) {
			t.Fatalf("published_at = %#v", body["published_at"])
		}
		w.WriteHeader(statuses[requestCount])
		requestCount++
		_, _ = w.Write([]byte(`{"id":99}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	article := backfill.Article{
		Title: "Post", URL: "https://afdian.com/p/post-1", Author: "Alice",
		Content: "<p>body</p>", PublishedAt: time.Unix(123, 0), Status: "unread", ExternalID: "post-1",
	}
	created, err := client.Import(context.Background(), 42, article)
	if err != nil || created != backfill.ImportCreated {
		t.Fatalf("created = %v, %v", created, err)
	}
	existing, err := client.Import(context.Background(), 42, article)
	if err != nil || existing != backfill.ImportExisting {
		t.Fatalf("existing = %v, %v", existing, err)
	}
}

func TestClientErrorIsBoundedAndDoesNotLeakToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBodyBytes+100)))
	}))
	defer server.Close()

	client, err := New(server.URL, "super-secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Feed(context.Background(), 42)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if strings.Contains(err.Error(), "super-secret") || len(err.Error()) > maxErrorBodyBytes+200 {
		t.Fatalf("unsafe error: len=%d error=%v", len(err.Error()), err)
	}
}

func TestNewValidatesBaseURLAndToken(t *testing.T) {
	for _, tt := range []struct {
		baseURL string
		token   string
	}{
		{"rssgen", "token"},
		{"https://user:pass@example.com", "token"},
		{"https://example.com", ""},
	} {
		if _, err := New(tt.baseURL, tt.token); err == nil {
			t.Fatalf("New(%q, token=%t) should fail", tt.baseURL, tt.token != "")
		}
	}
}

func assertEntryStatuses(t *testing.T, values url.Values) {
	t.Helper()
	want := []string{"read", "unread"}
	if got := values["status"]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("status = %v, want %v", got, want)
	}
	if values.Get("order") != "id" || values.Get("direction") != "asc" {
		t.Fatalf("entry ordering = %s/%s", values.Get("order"), values.Get("direction"))
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
