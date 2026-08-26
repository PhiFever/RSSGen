// Package miniflux 实现历史对账所需的 Miniflux HTTP 适配器。
package miniflux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/backfill"
)

const (
	defaultPageSize   = 100
	maxErrorBodyBytes = 4096
)

// Client 是 backfill.Destination 的 Miniflux HTTP 适配器。
type Client struct {
	baseURL  string
	token    string
	http     *http.Client
	pageSize int
}

// New 校验 Miniflux 实例根地址并创建 API token 客户端。
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("解析 Miniflux URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("Miniflux URL 必须是 http(s) 绝对 URL")
	}
	if u.User != nil {
		return nil, fmt.Errorf("Miniflux URL 不得包含用户名或密码")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Miniflux API token 不能为空")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/") + "/v1"
	u.RawPath = ""
	return &Client{
		baseURL:  strings.TrimRight(u.String(), "/"),
		token:    token,
		http:     &http.Client{Timeout: 30 * time.Second},
		pageSize: defaultPageSize,
	}, nil
}

type feedResponse struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	FeedURL string `json:"feed_url"`
}

// Feeds 返回当前认证用户可见的全部 feed。
func (c *Client) Feeds(ctx context.Context) ([]backfill.Feed, error) {
	var response []feedResponse
	if err := c.getJSON(ctx, "/feeds", &response); err != nil {
		return nil, err
	}
	feeds := make([]backfill.Feed, 0, len(response))
	for _, feed := range response {
		feeds = append(feeds, backfill.Feed{ID: feed.ID, Title: feed.Title, FeedURL: feed.FeedURL})
	}
	return feeds, nil
}

// Feed 按数字 ID 返回一个确定的 feed。
func (c *Client) Feed(ctx context.Context, feedID int64) (backfill.Feed, error) {
	var response feedResponse
	if err := c.getJSON(ctx, "/feeds/"+strconv.FormatInt(feedID, 10), &response); err != nil {
		return backfill.Feed{}, err
	}
	return backfill.Feed{ID: response.ID, Title: response.Title, FeedURL: response.FeedURL}, nil
}

type entriesResponse struct {
	Total   int `json:"total"`
	Entries []struct {
		URL  string `json:"url"`
		Hash string `json:"hash"`
	} `json:"entries"`
}

// Entries 读取一个 feed 的全部状态和全部分页条目。
func (c *Client) Entries(ctx context.Context, feedID int64) ([]backfill.Entry, error) {
	entries := make([]backfill.Entry, 0)
	for offset := 0; ; offset += c.pageSize {
		query := url.Values{}
		query.Add("status", "read")
		query.Add("status", "unread")
		query.Set("offset", strconv.Itoa(offset))
		query.Set("limit", strconv.Itoa(c.pageSize))
		query.Set("order", "id")
		query.Set("direction", "asc")

		path := fmt.Sprintf("/feeds/%d/entries?%s", feedID, query.Encode())
		var response entriesResponse
		if err := c.getJSON(ctx, path, &response); err != nil {
			return nil, err
		}
		for _, entry := range response.Entries {
			entries = append(entries, backfill.Entry{URL: entry.URL, Hash: entry.Hash})
		}
		if len(response.Entries) < c.pageSize || response.Total > 0 && len(entries) >= response.Total {
			break
		}
	}
	return entries, nil
}

type importRequest struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Author      string `json:"author,omitempty"`
	Content     string `json:"content,omitempty"`
	PublishedAt *int64 `json:"published_at,omitempty"`
	Status      string `json:"status,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
}

// Import 通过 Miniflux Entry Import 发送一篇规范化文章。
func (c *Client) Import(ctx context.Context, feedID int64, article backfill.Article) (backfill.ImportOutcome, error) {
	request := importRequest{
		Title:      article.Title,
		URL:        article.URL,
		Author:     article.Author,
		Content:    article.Content,
		Status:     article.Status,
		ExternalID: article.ExternalID,
	}
	if !article.PublishedAt.IsZero() {
		publishedAt := article.PublishedAt.Unix()
		request.PublishedAt = &publishedAt
	}
	body, err := json.Marshal(request)
	if err != nil {
		return 0, fmt.Errorf("编码导入条目: %w", err)
	}
	path := fmt.Sprintf("/feeds/%d/entries/import", feedID)
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated:
		_, _ = io.Copy(io.Discard, resp.Body)
		return backfill.ImportCreated, nil
	case http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		return backfill.ImportExisting, nil
	default:
		return 0, responseError(resp, http.MethodPost, path)
	}
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp, http.MethodGet, path)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("解析 Miniflux 响应 %s: %w", pathWithoutQuery(path), err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("创建 Miniflux 请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Auth-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Miniflux %s %s 请求失败: %w", method, pathWithoutQuery(path), err)
	}
	return resp, nil
}

func responseError(resp *http.Response, method, path string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	diagnostic := strings.TrimSpace(string(body))
	if diagnostic == "" {
		return fmt.Errorf("Miniflux %s %s 返回 HTTP %d", method, pathWithoutQuery(path), resp.StatusCode)
	}
	return fmt.Errorf("Miniflux %s %s 返回 HTTP %d: %s", method, pathWithoutQuery(path), resp.StatusCode, diagnostic)
}

func pathWithoutQuery(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		return path[:index]
	}
	return path
}
