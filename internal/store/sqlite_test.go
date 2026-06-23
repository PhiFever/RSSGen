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

func closeStore(t *testing.T, s *ArticleStore) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
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
	defer closeStore(t, s)

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected db file to exist: %v", err)
	}
}

func TestInitDirectoryError(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("写入父级文件失败: %v", err)
	}
	s := New(filepath.Join(parentFile, "test.db"))
	if err := s.Init(); err == nil {
		t.Fatal("父级路径是文件时 Init 应返回错误")
	}
}

func TestCloseNilDB(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil db: %v", err)
	}
}

func TestSaveAndGet(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeStore(t, s)

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
	defer closeStore(t, s)

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
	defer closeStore(t, s)

	if err := s.Save("zhihu", "item1", "old"); err != nil {
		t.Fatalf("Save old: %v", err)
	}
	if err := s.Save("zhihu", "item1", "new"); err != nil {
		t.Fatalf("Save new: %v", err)
	}

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
	defer closeStore(t, s)

	has, err := s.HasArticles("zhihu")
	if err != nil {
		t.Fatalf("HasArticles: %v", err)
	}
	if has {
		t.Fatal("expected no articles initially")
	}

	if err := s.Save("zhihu", "item1", "content"); err != nil {
		t.Fatalf("Save: %v", err)
	}

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

func TestClosedDBGracefulReadAndReportsSaveError(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	content, found, err := s.Get("r", "i")
	if err != nil {
		t.Fatalf("Get on closed db should degrade without error: %v", err)
	}
	if found || content != "" {
		t.Fatal("closed db Get 应降级为未命中")
	}

	if err := s.Save("r", "i", "c"); err == nil {
		t.Fatal("closed db Save 应返回底层错误")
	}

	has, err := s.HasArticles("r")
	if err != nil {
		t.Fatalf("HasArticles on closed db should degrade without error: %v", err)
	}
	if has {
		t.Fatal("closed db HasArticles 应降级为 false")
	}
}

// --- 迁移补充：Unicode+HTML 存取（迁移自 Python TestRoundTrip.unicode_and_html） ---

func TestSaveAndGetUnicodeHTML(t *testing.T) {
	s := New(tempDB(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeStore(t, s)

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
	defer closeStore(t, s)

	if err := s.Save("zhihu", "item1", "zhihu内容"); err != nil {
		t.Fatalf("Save zhihu: %v", err)
	}
	if err := s.Save("afdian", "item1", "afdian内容"); err != nil {
		t.Fatalf("Save afdian: %v", err)
	}

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
	if err := s1.Init(); err != nil {
		t.Fatalf("Init s1: %v", err)
	}
	if err := s1.Save("zhihu", "persist1", "持久化内容"); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}

	// 重新打开
	s2 := New(path)
	if err := s2.Init(); err != nil {
		t.Fatalf("Init s2: %v", err)
	}
	defer closeStore(t, s2)

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
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Save("r", "i", "c"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

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
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Go 实现：关闭后 Save 返回 error（Python 版静默吞错，行为不同）
	err := s.Save("r", "i", "c")
	if err == nil {
		t.Error("关闭后 Save 应返回 error")
	}
}
