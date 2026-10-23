// Package media 在 feed 发布前把白名单图片归档到持久存储（当前后端：Immich），并改写正文中的图片地址。
package media

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
	fhttp "github.com/bogdanfinn/fhttp"
	"golang.org/x/net/html"
)

type sourceClient interface {
	Do(*fhttp.Request) (*fhttp.Response, error)
}
type clientKey struct{ impersonate, proxy string }
type stats struct {
	Stored           uint64            `json:"stored"`
	Deduplicated     uint64            `json:"deduplicated"`
	Found            uint64            `json:"found"`
	SourceFailures   map[string]uint64 `json:"source_failures"`
	LastStoreError   string            `json:"last_store_error,omitempty"`
	LastStoreErrorAt *time.Time        `json:"last_store_error_at,omitempty"`
}

// Archiver 持有媒体会话、已发布地址及归档计数。
type Archiver struct {
	cfg             config.MediaConfig
	store           *immichStore
	newSourceClient func(impersonate, proxy string) (sourceClient, error)
	mu              sync.Mutex
	clients         map[clientKey]sourceClient
	published       map[string]string // 规范化源 URL → 已提交的存储地址
	stats           stats
}

// New 创建归档器；不探测存储的可达性。
func New(cfg config.MediaConfig) *Archiver {
	return &Archiver{
		cfg:   cfg,
		store: newImmichStore(cfg.Immich),
		newSourceClient: func(impersonate, proxy string) (sourceClient, error) {
			return scraper.NewHTTPClient(impersonate, proxy)
		},
		clients: make(map[clientKey]sourceClient), published: make(map[string]string),
		stats: stats{SourceFailures: make(map[string]uint64)},
	}
}

// Stats 返回独立的、可 JSON 编码的计数快照。
func (a *Archiver) Stats() stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.stats
	out.SourceFailures = make(map[string]uint64, len(a.stats.SourceFailures))
	for k, v := range a.stats.SourceFailures {
		out.SourceFailures[k] = v
	}
	if out.LastStoreErrorAt != nil {
		timestamp := *out.LastStoreErrorAt
		out.LastStoreErrorAt = &timestamp
	}
	return out
}

type imageJob struct {
	source, referer string
	published       *time.Time
}

// Archive 原地改写条目正文；存储侧失败时返回错误并保留全部原始正文。
func (a *Archiver) Archive(rc config.ResolvedRouteConfig, items []route.FeedItem) error {
	pending := make(map[string]imageJob)
	known := make(map[string]string)
	for _, item := range items {
		a.walkImages(item.Content, func(source string) string {
			a.mu.Lock()
			published := a.published[source]
			a.mu.Unlock()
			if published != "" {
				known[source] = published
			} else {
				pending[source] = imageJob{source, item.Link, item.PubDate}
			}
			return ""
		})
	}
	// 两个工作协程共享队列，遇到存储侧错误后不再领取新任务。
	var workMu sync.Mutex
	var firstErr error
	fresh := make(map[string]string)
	var ids []string
	jobs := make([]imageJob, 0, len(pending))
	for _, job := range pending {
		jobs = append(jobs, job)
	}
	var wg sync.WaitGroup
	workers := min(2, len(jobs))
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				workMu.Lock()
				if firstErr != nil || len(jobs) == 0 {
					workMu.Unlock()
					return
				}
				job := jobs[0]
				jobs = jobs[1:]
				workMu.Unlock()
				id, err := a.store.find(imageBaseName(job.source))
				found := id != ""
				if err == nil && !found {
					data, ext, reason := a.download(rc, job)
					if reason != "" {
						a.sourceFailure(job.source, reason)
						continue
					}
					id, err = a.put(job, data, ext)
					if errors.Is(err, errRejected) {
						a.sourceFailure(job.source, "rejected")
						continue
					}
				}
				workMu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					fresh[job.source] = id
					if found {
						a.mu.Lock()
						a.stats.Found++
						a.mu.Unlock()
					} else {
						ids = append(ids, id)
					}
				}
				workMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr == nil && len(ids) > 0 {
		firstErr = a.store.commit(ids)
	}
	if firstErr != nil {
		now := time.Now()
		a.mu.Lock()
		a.stats.LastStoreError = firstErr.Error()
		a.stats.LastStoreErrorAt = &now
		a.mu.Unlock()
		return firstErr
	}
	a.mu.Lock()
	for source, id := range fresh {
		published := a.store.url(id)
		a.published[source] = published
		known[source] = published
	}
	a.mu.Unlock()
	for i := range items {
		items[i].Content = a.walkImages(items[i].Content, func(source string) string {
			return known[source]
		})
	}
	return nil
}

func imageBaseName(source string) string {
	digest := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", digest[:8])
}

// put 把图片交给存储并更新计数；文件名用源 URL 哈希加魔数扩展名。
func (a *Archiver) put(job imageJob, data []byte, ext string) (string, error) {
	published := time.Now()
	if job.published != nil && !job.published.IsZero() {
		published = *job.published
	}
	id, duplicate, err := a.store.put(data, imageBaseName(job.source)+"."+ext, published)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	if duplicate {
		a.stats.Deduplicated++
	} else {
		a.stats.Stored++
	}
	a.mu.Unlock()
	return id, nil
}

func (a *Archiver) sourceFailure(source, reason string) {
	a.mu.Lock()
	a.stats.SourceFailures[reason]++
	a.mu.Unlock()
	slog.Warn("图片归档跳过源图", "url", safeSource(source), "reason", reason)
}

func safeSource(source string) string {
	u, err := url.Parse(source)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func (a *Archiver) normalize(source string) (string, bool) {
	u, err := url.Parse(source)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, rule := range a.cfg.AllowedHosts {
		rule = strings.ToLower(rule)
		if strings.HasPrefix(rule, "*.") {
			if strings.HasSuffix(host, rule[1:]) && len(host) > len(rule[1:]) {
				allowed = true
			}
		} else if host == rule {
			allowed = true
		}
	}
	if !allowed {
		return "", false
	}
	u.Scheme = "https"
	u.Host = host
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), true
}

// walkImages 仅重建实际改写的 img 标签，其余字节直接保留。
func (a *Archiver) walkImages(content string, replace func(string) string) string {
	z := html.NewTokenizer(strings.NewReader(content))
	var out strings.Builder
	for {
		kind := z.Next()
		raw := string(z.Raw()) // Token 会改变 Raw 的底层字节，先复制。
		if kind == html.ErrorToken {
			out.WriteString(raw)
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			out.WriteString(raw)
			continue
		}
		token := z.Token()
		changed := false
		if token.Data == "img" {
			for i, attr := range token.Attr {
				if attr.Key != "src" {
					continue
				}
				if source, ok := a.normalize(attr.Val); ok {
					if replacement := replace(source); replacement != "" {
						token.Attr[i].Val = replacement
						changed = true
					}
				}
			}
		}
		if changed {
			out.WriteString(token.String())
		} else {
			out.WriteString(raw)
		}
	}
	return out.String()
}

func (a *Archiver) source(rc config.ResolvedRouteConfig) (sourceClient, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := clientKey{rc.Impersonate, rc.Proxy}
	if client := a.clients[key]; client != nil {
		return client, nil
	}
	client, err := a.newSourceClient(key.impersonate, key.proxy)
	if err != nil {
		return nil, fmt.Errorf("创建图片下载客户端失败")
	}
	a.clients[key] = client
	return client, nil
}
