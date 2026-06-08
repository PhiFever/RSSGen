package store

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.db")
}

func TestInitAndClose(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInitCreatesDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "dir", "test.db")

	s := New(dbPath)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected db file to exist: %v", err)
	}
}

func TestSaveAndGet(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	if err := s.Save("zhihu", "item1", "<p>hello</p>"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	content, found, err := s.Get("zhihu", "item1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected to find item1")
	}
	if content != "<p>hello</p>" {
		t.Fatalf("expected <p>hello</p>, got %s", content)
	}
}

func TestGetMissing(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	content, found, err := s.Get("zhihu", "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
	if content != "" {
		t.Fatalf("expected empty content, got %s", content)
	}
}

func TestSaveOverwrite(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	s.Save("zhihu", "item1", "old")
	s.Save("zhihu", "item1", "new")

	content, found, _ := s.Get("zhihu", "item1")
	if !found {
		t.Fatal("expected to find item1")
	}
	if content != "new" {
		t.Fatalf("expected new, got %s", content)
	}
}

func TestHasArticles(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	has, err := s.HasArticles("zhihu")
	if err != nil {
		t.Fatalf("HasArticles: %v", err)
	}
	if has {
		t.Fatal("expected no articles initially")
	}

	s.Save("zhihu", "item1", "content")

	has, err = s.HasArticles("zhihu")
	if err != nil {
		t.Fatalf("HasArticles: %v", err)
	}
	if !has {
		t.Fatal("expected articles after save")
	}

	// 不同路由应独立
	has, err = s.HasArticles("weixin")
	if err != nil {
		t.Fatalf("HasArticles: %v", err)
	}
	if has {
		t.Fatal("expected no articles for weixin route")
	}
}

func TestNilDBGraceful(t *testing.T) {
	// 未调用 Init() 时，所有操作应优雅降级
	s := New(tempDB(t))

	content, found, err := s.Get("r", "i")
	if err != nil {
		t.Fatalf("Get on nil db: %v", err)
	}
	if found || content != "" {
		t.Fatal("expected not found on nil db")
	}

	if err := s.Save("r", "i", "c"); err != nil {
		t.Fatalf("Save on nil db: %v", err)
	}

	has, err := s.HasArticles("r")
	if err != nil {
		t.Fatalf("HasArticles on nil db: %v", err)
	}
	if has {
		t.Fatal("expected false on nil db")
	}
}
