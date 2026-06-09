// Package afdian 实现爱发电创作者动态路由。
package afdian

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
)

const hostURL = "https://afdian.com"

func init() {
	route.Register("afdian", func(cfg config.ResolvedRouteConfig) route.Route {
		return New(cfg)
	})
}

// Route 是爱发电路由实现。
type Route struct {
	cfg config.ResolvedRouteConfig
}

// New 创建爱发电路由实例。
func New(cfg config.ResolvedRouteConfig) *Route {
	return &Route{cfg: cfg}
}

func (r *Route) Name() string        { return "afdian" }
func (r *Route) Description() string { return "爱发电创作者动态订阅" }
func (r *Route) FeedIDField() string { return "user_id" }

func (r *Route) getScraper() *scraper.Scraper {
	return scraper.New(scraper.Config{
		Cookies:     parseCookieString(r.cfg.Cookie),
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

	sc := r.getScraper()

	// 获取作者 ID
	userID, err := r.getAuthorID(sc, authorSlug)
	if err != nil {
		return nil, fmt.Errorf("获取作者 ID 失败: %w", err)
	}

	// 获取帖子列表
	posts, err := r.getPostList(sc, userID, authorSlug, limit)
	if err != nil {
		return nil, fmt.Errorf("获取帖子列表失败: %w", err)
	}

	// 获取每篇文章内容并构建 FeedItem
	items := make([]route.FeedItem, 0, len(posts))
	for _, post := range posts {
		postID, _ := post["post_id"].(string)
		title, _ := post["title"].(string)

		// 获取文章内容
		content := ""
		if articleStore != nil {
			cached, found, err := articleStore.Get("afdian", postID)
			if err == nil && found && cached != "" {
				content = cached
			}
		}
		if content == "" {
			content, err = r.getPostDetail(sc, postID)
			if err != nil {
				// 获取详情失败，使用空内容
				content = ""
			} else if articleStore != nil {
				articleStore.Save("afdian", postID, content)
			}
		}

		// 解析发布时间
		var pubDate *time.Time
		if publishTime, ok := post["publish_time"].(float64); ok && publishTime > 0 {
			t := time.Unix(int64(publishTime), 0).UTC()
			pubDate = &t
		}

		// 提取图片附件
		var enclosures []route.Enclosure
		if pics, ok := post["pics"].([]interface{}); ok {
			for _, pic := range pics {
				if picURL, ok := pic.(string); ok && picURL != "" {
					enclosures = append(enclosures, route.Enclosure{
						URL:  picURL,
						Type: "image/jpeg",
					})
				}
			}
		}

		// 提取作者名
		author := ""
		if user, ok := post["user"].(map[string]interface{}); ok {
			author, _ = user["name"].(string)
		}

		item := route.FeedItem{
			Title:      title,
			Link:       fmt.Sprintf("%s/p/%s", hostURL, postID),
			Content:    content,
			PubDate:    pubDate,
			Author:     author,
			GUID:       postID,
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

	var data map[string]interface{}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", fmt.Errorf("解析响应 JSON 失败: %w", err)
	}

	if ec, ok := data["ec"].(float64); !ok || ec != 200 {
		em, _ := data["em"].(string)
		return "", fmt.Errorf("爱发电 API 错误: ec=%v, em=%s", data["ec"], em)
	}

	dataObj, ok := data["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("响应缺少 data 字段")
	}
	user, ok := dataObj["user"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("响应缺少 user 字段")
	}
	userID, ok := user["user_id"].(string)
	if !ok {
		return "", fmt.Errorf("响应缺少 user_id 字段")
	}

	return userID, nil
}

// getPostList 获取作者动态列表（简化版，只取第一页）。
func (r *Route) getPostList(sc *scraper.Scraper, userID, authorSlug string, limit int) ([]map[string]interface{}, error) {
	referer := fmt.Sprintf("%s/a/%s", hostURL, authorSlug)
	var allPosts []map[string]interface{}
	publishSN := ""

	for len(allPosts) < limit {
		apiURL := fmt.Sprintf(
			"%s/api/post/get-list?user_id=%s&type=old&publish_sn=%s&per_page=10&group_id=&all=1&is_public=&plan_id=&title=&name=",
			hostURL, userID, publishSN,
		)

		resp, err := sc.Get(apiURL, referer, nil)
		if err != nil {
			return allPosts, err
		}
		if resp.StatusCode != 200 {
			return allPosts, &route.HTTPError{StatusCode: resp.StatusCode, URL: apiURL}
		}

		var data map[string]interface{}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return allPosts, fmt.Errorf("解析响应 JSON 失败: %w", err)
		}

		if ec, ok := data["ec"].(float64); !ok || ec != 200 {
			em, _ := data["em"].(string)
			return allPosts, fmt.Errorf("爱发电 API 错误: ec=%v, em=%s", data["ec"], em)
		}

		dataObj, _ := data["data"].(map[string]interface{})
		if dataObj == nil {
			break
		}

		listRaw, ok := dataObj["list"].([]interface{})
		if !ok || len(listRaw) == 0 {
			break
		}

		for _, item := range listRaw {
			if post, ok := item.(map[string]interface{}); ok {
				allPosts = append(allPosts, post)
			}
		}

		// 获取最后一条的 publish_sn 用于翻页
		lastPost, ok := listRaw[len(listRaw)-1].(map[string]interface{})
		if !ok {
			break
		}
		sn, ok := lastPost["publish_sn"].(string)
		if !ok || sn == "" {
			break
		}
		publishSN = sn
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

	var data map[string]interface{}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", fmt.Errorf("解析响应 JSON 失败: %w", err)
	}

	if ec, ok := data["ec"].(float64); !ok || ec != 200 {
		em, _ := data["em"].(string)
		return "", fmt.Errorf("爱发电 API 错误: ec=%v, em=%s", data["ec"], em)
	}

	dataObj, _ := data["data"].(map[string]interface{})
	if dataObj == nil {
		return "", nil
	}
	post, _ := dataObj["post"].(map[string]interface{})
	if post == nil {
		return "", nil
	}
	content, _ := post["content"].(string)
	return content, nil
}

// parseCookieString 解析 "key1=val1; key2=val2" 格式的 cookie 字符串。
func parseCookieString(s string) map[string]string {
	cookies := make(map[string]string)
	s = strings.TrimSpace(s)
	if s == "" {
		return cookies
	}
	pairs := strings.Split(s, ";")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			cookies[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return cookies
}
