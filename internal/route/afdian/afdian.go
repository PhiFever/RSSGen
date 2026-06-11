// Package afdian 实现爱发电创作者动态路由。
package afdian

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
)

var hostURL = "https://afdian.com"

// afdianResponse 爱发电 API 通用响应结构。
type afdianResponse struct {
	EC   int             `json:"ec"`
	EM   string          `json:"em"`
	Data json.RawMessage `json:"data"`
}

// afdianUser 爱发电用户信息。
type afdianUser struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// afdianPost 爱发电帖子信息。
type afdianPost struct {
	PostID      string     `json:"post_id"`
	Title       string     `json:"title"`
	PublishTime float64    `json:"publish_time"`
	PublishSN   int64      `json:"publish_sn"`
	Content     string     `json:"content"`
	Pics        []string   `json:"pics"`
	User        afdianUser `json:"user"`
}

// afdianPostList 爱发电帖子列表响应。
type afdianPostList struct {
	List []afdianPost `json:"list"`
}

// afdianUserProfile 爱发电用户资料响应。
type afdianUserProfile struct {
	User afdianUser `json:"user"`
}

// afdianPostDetail 爱发电帖子详情响应。
type afdianPostDetail struct {
	Post struct {
		Content string `json:"content"`
	} `json:"post"`
}

// parseAfdianResponse 解析爱发电 API 响应，返回原始 data 字节。
// 统一处理 JSON 解析和 ec 码校验。
func parseAfdianResponse(body []byte) (json.RawMessage, error) {
	var resp afdianResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析响应 JSON 失败: %w", err)
	}

	if resp.EC != 200 {
		return nil, fmt.Errorf("爱发电 API 错误: ec=%d, em=%s", resp.EC, resp.EM)
	}

	return resp.Data, nil
}

func init() {
	route.Register("afdian", func(cfg config.ResolvedRouteConfig) route.Route {
		return New(cfg)
	})
}

// Route 是爱发电路由实现。
type Route struct {
	cfg             config.ResolvedRouteConfig
	getAuthorIDFn   func(sc *scraper.Scraper, authorSlug string) (string, error)
	getPostListFn   func(sc *scraper.Scraper, userID, authorSlug string, limit int) ([]afdianPost, error)
	getPostDetailFn func(sc *scraper.Scraper, postID string) (string, error)
}

// New 创建爱发电路由实例。
func New(cfg config.ResolvedRouteConfig) *Route {
	return &Route{cfg: cfg}
}

func (r *Route) Name() string        { return "afdian" }
func (r *Route) Description() string { return "爱发电创作者动态订阅" }
func (r *Route) FeedIDField() string { return "user_id" }

func (r *Route) getScraper() (*scraper.Scraper, error) {
	return scraper.New(scraper.Config{
		Cookies:     scraper.ParseCookieString(r.cfg.Cookie),
		RateLimit:   r.cfg.RateLimit,
		Proxy:       r.cfg.Proxy,
		Impersonate: r.cfg.Impersonate,
	})
}

func (r *Route) FeedInfo(pathParams []string) (*route.FeedInfo, error) {
	if len(pathParams) == 0 {
		return nil, fmt.Errorf("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
	}
	authorSlug := pathParams[0]
	displayName := authorSlug

	// 查找 alias
	for _, f := range r.cfg.Feeds {
		if f.UserID == authorSlug && f.Alias != "" {
			displayName = f.Alias
		}
	}

	return &route.FeedInfo{
		Title:       fmt.Sprintf("爱发电 - %s", displayName),
		Link:        fmt.Sprintf("%s/a/%s", hostURL, authorSlug),
		Description: fmt.Sprintf("爱发电创作者 %s 的最新动态", displayName),
	}, nil
}

func (r *Route) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
	if len(pathParams) == 0 {
		return nil, fmt.Errorf("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
	}
	authorSlug := pathParams[0]
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	sc, err := r.getScraper()
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}

	// 获取作者 ID
	authorIDFn := r.getAuthorID
	if r.getAuthorIDFn != nil {
		authorIDFn = r.getAuthorIDFn
	}
	userID, err := authorIDFn(sc, authorSlug)
	if err != nil {
		return nil, fmt.Errorf("获取作者 ID 失败: %w", err)
	}

	// 获取帖子列表
	postListFn := r.getPostList
	if r.getPostListFn != nil {
		postListFn = r.getPostListFn
	}
	posts, err := postListFn(sc, userID, authorSlug, limit)
	if err != nil {
		return nil, fmt.Errorf("获取帖子列表失败: %w", err)
	}

	contents := make([]string, len(posts))
	contentOK := make([]bool, len(posts))
	var missing []int

	for i, post := range posts {
		if articleStore != nil {
			cached, found, err := articleStore.Get("afdian", post.PostID)
			if err == nil && found && cached != "" {
				contents[i] = cached
				contentOK[i] = true
				continue
			}
		}
		missing = append(missing, i)
	}

	detailFn := r.getPostDetail
	if r.getPostDetailFn != nil {
		detailFn = r.getPostDetailFn
	}

	var wg sync.WaitGroup
	for _, idx := range missing {
		idx := idx
		postID := posts[idx].PostID
		wg.Add(1)
		go func() {
			defer wg.Done()
			content, err := detailFn(sc, postID)
			if err != nil {
				// 获取详情失败，跳过此条目（与 Python 行为一致）
				slog.Warn("文章详情获取失败，跳过", "post_id", postID, "error", err)
				return
			}
			contents[idx] = content
			contentOK[idx] = true
		}()
	}
	wg.Wait()

	for _, idx := range missing {
		if articleStore != nil && contentOK[idx] {
			if err := articleStore.Save("afdian", posts[idx].PostID, contents[idx]); err != nil {
				slog.Warn("文章详情落库失败", "post_id", posts[idx].PostID, "error", err)
			}
		}
	}

	// 构建 FeedItem，保持帖子列表原始顺序。
	items := make([]route.FeedItem, 0, len(posts))
	for i, post := range posts {
		if !contentOK[i] {
			continue
		}
		// 解析发布时间
		var pubDate *time.Time
		if post.PublishTime > 0 {
			t := time.Unix(int64(post.PublishTime), 0).UTC()
			pubDate = &t
		}

		// 提取图片附件
		var enclosures []route.Enclosure
		for _, picURL := range post.Pics {
			if picURL != "" {
				enclosures = append(enclosures, route.Enclosure{
					URL:  picURL,
					Type: "image/jpeg",
				})
			}
		}

		item := route.FeedItem{
			Title:      post.Title,
			Link:       fmt.Sprintf("%s/p/%s", hostURL, post.PostID),
			Content:    contents[i],
			PubDate:    pubDate,
			Author:     post.User.Name,
			GUID:       post.PostID,
			Enclosures: enclosures,
		}
		items = append(items, item)
	}

	return items, nil
}

// getAuthorID 通过 url_slug 获取作者 user_id。
func (r *Route) getAuthorID(sc *scraper.Scraper, authorSlug string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/user/get-profile-by-slug?url_slug=%s", hostURL, authorSlug)
	referer := fmt.Sprintf("%s/a/%s", hostURL, authorSlug)

	resp, err := sc.Get(apiURL, referer, nil)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", &route.HTTPError{StatusCode: resp.StatusCode, URL: apiURL}
	}

	data, err := parseAfdianResponse(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("响应缺少 data 字段")
	}

	var profile afdianUserProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return "", fmt.Errorf("解析用户资料失败: %w", err)
	}
	if profile.User.UserID == "" {
		return "", fmt.Errorf("响应缺少 user_id 字段")
	}

	return profile.User.UserID, nil
}

// getPostList 获取作者动态列表（简化版，只取第一页）。
func (r *Route) getPostList(sc *scraper.Scraper, userID, authorSlug string, limit int) ([]afdianPost, error) {
	referer := fmt.Sprintf("%s/a/%s", hostURL, authorSlug)
	var allPosts []afdianPost
	publishSN := int64(0)

	for len(allPosts) < limit {
		apiURL := fmt.Sprintf(
			"%s/api/post/get-list?user_id=%s&type=old&publish_sn=%d&per_page=10&group_id=&all=1&is_public=&plan_id=&title=&name=",
			hostURL, userID, publishSN,
		)

		resp, err := sc.Get(apiURL, referer, nil)
		if err != nil {
			return allPosts, err
		}
		if resp.StatusCode != 200 {
			return allPosts, &route.HTTPError{StatusCode: resp.StatusCode, URL: apiURL}
		}

		data, err := parseAfdianResponse(resp.Body)
		if err != nil {
			return allPosts, err
		}
		if len(data) == 0 {
			break
		}

		var postList afdianPostList
		if err := json.Unmarshal(data, &postList); err != nil {
			slog.Warn("解析帖子列表失败，原始响应", "error", err, "data", string(data))
			return allPosts, fmt.Errorf("解析帖子列表失败: %w", err)
		}
		if len(postList.List) == 0 {
			break
		}

		allPosts = append(allPosts, postList.List...)

		// 获取最后一条的 publish_sn 用于翻页
		lastPost := postList.List[len(postList.List)-1]
		if lastPost.PublishSN == 0 {
			break
		}
		publishSN = lastPost.PublishSN
	}

	// 截断到 limit
	if len(allPosts) > limit {
		allPosts = allPosts[:limit]
	}

	return allPosts, nil
}

// getPostDetail 获取文章正文 HTML。
func (r *Route) getPostDetail(sc *scraper.Scraper, postID string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/post/get-detail?post_id=%s&album_id=", hostURL, postID)
	referer := fmt.Sprintf("%s/p/%s", hostURL, postID)

	resp, err := sc.Get(apiURL, referer, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", &route.HTTPError{StatusCode: resp.StatusCode, URL: apiURL}
	}

	data, err := parseAfdianResponse(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}

	var detail afdianPostDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		return "", fmt.Errorf("解析帖子详情失败: %w", err)
	}
	return detail.Post.Content, nil
}
