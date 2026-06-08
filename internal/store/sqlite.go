// Package store 提供 SQLite 文章持久化存储。
package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS articles (
    route      TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (route, item_id)
) WITHOUT ROWID;
`

// ArticleStore 是 SQLite 单文件文章存储。
//
// 生命周期由调用方管理：启动时调 Init()，关闭时调 Close()。
// 所有读写通过 sync.Mutex 串行化（RSSGen 并发量极小）。
// 运行期错误降级为 warning + 当作 cache miss；Init 失败硬抛。
type ArticleStore struct {
	dbPath string
	db     *sql.DB
	mu     sync.Mutex
}

// New 创建一个 ArticleStore 实例，但不打开数据库。需调用 Init() 完成初始化。
func New(dbPath string) *ArticleStore {
	return &ArticleStore{dbPath: dbPath}
}

// Init 打开数据库连接，配置 PRAGMA 并创建表。
func (s *ArticleStore) Init() error {
	dir := filepath.Dir(s.dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建数据库目录 %s: %w", dir, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", s.dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("打开数据库 %s: %w", s.dbPath, err)
	}

	// 确保连接可用
	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("连接数据库 %s: %w", s.dbPath, err)
	}

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return fmt.Errorf("创建 articles 表: %w", err)
	}

	s.db = db
	log.Printf("ArticleStore 初始化完成: %s", s.dbPath)
	return nil
}

// Close 关闭数据库连接。
func (s *ArticleStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Get 查询指定路由和 itemID 的文章内容。未找到返回 ("", false, nil)。
func (s *ArticleStore) Get(route, itemID string) (content string, found bool, err error) {
	if s.db == nil {
		return "", false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.db.QueryRow(
		"SELECT content FROM articles WHERE route = ? AND item_id = ?",
		route, itemID,
	).Scan(&content)

	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		log.Printf("ArticleStore.Get 失败 (%s/%s): %v", route, itemID, err)
		return "", false, nil
	}
	return content, true, nil
}

// Save 插入或替换指定路由和 itemID 的文章内容。
func (s *ArticleStore) Save(route, itemID, content string) error {
	if s.db == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO articles (route, item_id, content, fetched_at) VALUES (?, ?, ?, ?)",
		route, itemID, content, now,
	)
	if err != nil {
		log.Printf("ArticleStore.Save 失败 (%s/%s): %v", route, itemID, err)
	}
	return err
}

// HasArticles 检查指定路由是否已有文章数据。
func (s *ArticleStore) HasArticles(route string) (bool, error) {
	if s.db == nil {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var dummy int
	err := s.db.QueryRow(
		"SELECT 1 FROM articles WHERE route = ? LIMIT 1",
		route,
	).Scan(&dummy)

	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		log.Printf("ArticleStore.HasArticles 失败 (%s): %v", route, err)
		return false, nil
	}
	return true, nil
}
