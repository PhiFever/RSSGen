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

// --- 迁移补充：Unicode+HTML 存取（迁移自 Python TestRoundTrip.unicode_and_html） ---

func TestSaveAndGetUnicodeHTML(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	content := "<p>你好世界 🌍</p><script>alert('xss')</script>"
	if err := s.Save("zhihu", "unicode1", content); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := s.Get("zhihu", "unicode1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected to find unicode1")
	}
	if got != content {
		t.Errorf("内容不匹配: got %q, want %q", got, content)
	}
}

// --- 跨路由隔离（迁移自 Python TestKeySemantics.different_routes_isolated） ---

func TestDifferentRoutesIsolated(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	s.Save("zhihu", "item1", "zhihu内容")
	s.Save("afdian", "item1", "afdian内容")

	zhihu, found, _ := s.Get("zhihu", "item1")
	if !found || zhihu != "zhihu内容" {
		t.Errorf("zhihu/item1 = %q, want 'zhihu内容'", zhihu)
	}
	afdian, found, _ := s.Get("afdian", "item1")
	if !found || afdian != "afdian内容" {
		t.Errorf("afdian/item1 = %q, want 'afdian内容'", afdian)
	}
}

// --- 持久化（迁移自 Python TestPersistence.data_survives_close_and_reopen） ---

func TestPersistenceSurvivesCloseAndReopen(t *testing.T) {
	path := tempDB(t)

	// 写入并关闭
	s1 := New(path)
	s1.Init()
	s1.Save("zhihu", "persist1", "持久化内容")
	s1.Close()

	// 重新打开
	s2 := New(path)
	s2.Init()
	defer s2.Close()

	got, found, err := s2.Get("zhihu", "persist1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !found {
		t.Fatal("数据应在重开后保留")
	}
	if got != "持久化内容" {
		t.Errorf("got %q, want '持久化内容'", got)
	}
}

// --- 降级场景补充（迁移自 Python TestDegradation） ---

func TestGetReturnsNoneWhenUninitialized(t *testing.T) {
	s := New(tempDB(t))
	// 不调用 Init()
	content, found, err := s.Get("r", "i")
	if err != nil {
		t.Fatalf("Get on uninitialized: %v", err)
	}
	if found || content != "" {
		t.Error("未初始化时 Get 应返回 not found")
	}
}

func TestSaveSilentWhenUninitialized(t *testing.T) {
	s := New(tempDB(t))
	// 不调用 Init()，Save 不应 panic
	if err := s.Save("r", "i", "c"); err != nil {
		t.Fatalf("Save on uninitialized: %v", err)
	}
}

func TestGetAfterCloseReturnsNone(t *testing.T) {
	s := New(tempDB(t))
	s.Init()
	s.Save("r", "i", "c")
	s.Close()

	content, found, err := s.Get("r", "i")
	if err != nil {
		t.Fatalf("Get after close: %v", err)
	}
	if found || content != "" {
		t.Error("关闭后 Get 应返回 not found")
	}
}

func TestSaveAfterCloseReturnsError(t *testing.T) {
	s := New(tempDB(t))
	s.Init()
	s.Close()

	// Go 实现：关闭后 Save 返回 error（Python 版静默吞错，行为不同）
	err := s.Save("r", "i", "c")
	if err == nil {
		t.Error("关闭后 Save 应返回 error")
	}
}
