// Package afdian 实现爱发电创作者动态路由。
package afdian

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PhiFever/RSSGen/internal/backfill"
	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
	"github.com/tidwall/gjson"
)

var hostURL = "https://afdian.com"

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

type afdianUnixTime int64

func (t *afdianUnixTime) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	raw = strings.Trim(raw, `"`)
	if raw == "" || raw == "null" {
		*t = 0
		return nil
	}

	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("解析评论发布时间失败: %w", err)
	}
	*t = afdianUnixTime(int64(seconds))
	return nil
}

// afdianComment 是爱发电评论接口返回的单条评论。
type afdianComment struct {
	User        afdianUser     `json:"user"`
	PublishTime afdianUnixTime `json:"publish_time"`
	Content     string         `json:"content"`
	ReplyUser   *afdianUser    `json:"reply_user"`
}

// afdianCommentList 是爱发电评论接口响应 data。
type afdianCommentList struct {
	HotList []afdianComment `json:"hot_list"`
	List    []afdianComment `json:"list"`
}

// parseAfdianResponse 解析爱发电 API 响应，返回原始 data 字节。
// 统一处理 JSON 解析和 ec 码校验。
func parseAfdianResponse(body []byte) (json.RawMessage, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("解析响应 JSON 失败")
	}

	ec := gjson.GetBytes(body, "ec").Int()
	if ec != 200 {
		em := gjson.GetBytes(body, "em").String()
		return nil, fmt.Errorf("爱发电 API 错误: ec=%d, em=%s", ec, em)
	}

	data := gjson.GetBytes(body, "data")
	if !data.Exists() {
		return nil, nil
	}
	return json.RawMessage([]byte(data.Raw)), nil
}

func init() {
	route.Register("afdian", "爱发电创作者动态订阅", func(cfg config.ResolvedRouteConfig) route.Route {
		return New(cfg)
	})
}

// Route 是爱发电路由实现。
type Route struct {
	route.BaseRoute
	cfg               config.ResolvedRouteConfig
	getAuthorIDFn     func(sc *scraper.Scraper, authorSlug string) (string, error)
	getPostListFn     func(sc *scraper.Scraper, userID, authorSlug string, limit int) ([]afdianPost, error)
	getPostDetailFn   func(sc *scraper.Scraper, postID string) (string, error)
	getPostCommentsFn func(sc *scraper.Scraper, postID string) (afdianCommentList, error)
}

// New 创建爱发电路由实例。
func New(cfg config.ResolvedRouteConfig) *Route {
	return &Route{
		BaseRoute: route.NewBaseRoute("afdian"),
		cfg:       cfg,
	}
}

func (r *Route) feedInfo(pathParams []string) (route.FeedInfo, error) {
	if len(pathParams) == 0 {
		return route.FeedInfo{}, fmt.Errorf("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
	}
	authorSlug := pathParams[0]
	displayName := authorSlug

	// 查找 alias
	for _, f := range r.cfg.Feeds {
		if f.UserID == authorSlug && f.Alias != "" {
			displayName = f.Alias
		}
	}

	return route.FeedInfo{
		Title:       fmt.Sprintf("爱发电 - %s", displayName),
		Link:        fmt.Sprintf("%s/a/%s", hostURL, authorSlug),
		Description: fmt.Sprintf("爱发电创作者 %s 的最新动态", displayName),
	}, nil
}

func (r *Route) Fetch(pathParams []string, opts route.FetchOptions) (route.FeedResult, error) {
	if len(pathParams) == 0 {
		return route.FeedResult{}, fmt.Errorf("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
	}
	info, err := r.feedInfo(pathParams)
	if err != nil {
		return route.FeedResult{}, err
	}

	authorSlug := pathParams[0]
	limit := opts.Limit

	sc, err := r.Scraper(r.cfg)
	if err != nil {
		return route.FeedResult{}, fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}

	// 获取作者 ID
	authorIDFn := r.getAuthorID
	if r.getAuthorIDFn != nil {
		authorIDFn = r.getAuthorIDFn
	}
	userID, err := authorIDFn(sc, authorSlug)
	if err != nil {
		return route.FeedResult{}, fmt.Errorf("获取作者 ID 失败: %w", err)
	}

	// 获取帖子列表
	postListFn := r.getPostList
	if r.getPostListFn != nil {
		postListFn = r.getPostListFn
	}
	posts, err := postListFn(sc, userID, authorSlug, limit)
	if err != nil {
		return route.FeedResult{}, fmt.Errorf("获取帖子列表失败: %w", err)
	}

	contents := make([]string, len(posts))
	contentOK := make([]bool, len(posts))
	detailFn := r.getPostDetail
	if r.getPostDetailFn != nil {
		detailFn = r.getPostDetailFn
	}
	commentsFn := r.getPostComments
	if r.getPostCommentsFn != nil {
		commentsFn = r.getPostCommentsFn
	}

	var wg sync.WaitGroup
	for idx := range posts {
		idx := idx
		postID := posts[idx].PostID
		pics := posts[idx].Pics
		wg.Add(1)
		go func() {
			defer wg.Done()
			content, err := detailFn(sc, postID)
			if err != nil {
				// 获取详情失败，跳过此条目（与 Python 行为一致）
				slog.Warn("文章详情获取失败，跳过", "post_id", postID, "error", err)
				return
			}
			// 图片动态的正文是纯文本，换行要转成 <br> 才能在阅读器里保留分段；
			// 文章类正文是单行 HTML（换行由 <p>/<br> 表达，不含字面换行符），此处为空操作。
			// 必须在追加评论之前做，renderComments 生成的换行是有意的，不能一起转掉。
			content = strings.ReplaceAll(content, "\n", "<br>")
			content = appendPics(content, pics)
			comments, err := commentsFn(sc, postID)
			if err != nil {
				slog.Warn("文章评论获取失败，省略评论", "post_id", postID, "error", err)
			} else {
				content = appendRenderedComments(content, comments)
			}
			contents[idx] = content
			contentOK[idx] = true
		}()
	}
	wg.Wait()

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

		item := route.FeedItem{
			Title:   post.Title,
			Link:    fmt.Sprintf("%s/p/%s", hostURL, post.PostID),
			Content: contents[i],
			PubDate: pubDate,
			Author:  post.User.Name,
			GUID:    post.PostID,
		}
		items = append(items, item)
	}

	return route.FeedResult{Info: info, Items: items}, nil
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
		return "", route.NewHTTPError(resp.StatusCode, apiURL)
	}

	data, err := parseAfdianResponse(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("响应缺少 data 字段")
	}

	profile := gjson.ParseBytes(data)
	if !profile.IsObject() {
		return "", fmt.Errorf("解析用户资料失败: data 不是对象")
	}

	userID := profile.Get("user.user_id")
	if !userID.Exists() || userID.Type == gjson.Null || userID.String() == "" {
		return "", fmt.Errorf("响应缺少 user_id 字段")
	}
	if userID.Type != gjson.String {
		return "", fmt.Errorf("解析用户资料失败: data.user.user_id 不是字符串")
	}

	return userID.String(), nil
}

// getPostList 按 publish_sn 分页获取作者动态；limit <= 0 时遍历到自然结束。
func (r *Route) getPostList(sc *scraper.Scraper, userID, authorSlug string, limit int) ([]afdianPost, error) {
	return r.getPostListWithProgress(sc, userID, authorSlug, limit, nil)
}

type postListProgressFunc func(page, pageItems, totalItems int)

func (r *Route) getPostListWithProgress(
	sc *scraper.Scraper,
	userID, authorSlug string,
	limit int,
	progress postListProgressFunc,
) ([]afdianPost, error) {
	referer := fmt.Sprintf("%s/a/%s", hostURL, authorSlug)
	var allPosts []afdianPost
	publishSN := int64(0)
	seenCursors := map[int64]struct{}{0: {}}
	page := 0

	for limit <= 0 || len(allPosts) < limit {
		page++
		apiURL := fmt.Sprintf(
			"%s/api/post/get-list?user_id=%s&type=old&publish_sn=%d&per_page=10&group_id=&all=1&is_public=&plan_id=&title=&name=",
			hostURL, userID, publishSN,
		)

		resp, err := sc.Get(apiURL, referer, nil)
		if err != nil {
			return allPosts, err
		}
		if resp.StatusCode != 200 {
			return allPosts, route.NewHTTPError(resp.StatusCode, apiURL)
		}

		data, err := parseAfdianResponse(resp.Body)
		if err != nil {
			return allPosts, err
		}
		if len(data) == 0 {
			if progress != nil {
				progress(page, 0, len(allPosts))
			}
			break
		}

		var postList afdianPostList
		if err := json.Unmarshal(data, &postList); err != nil {
			slog.Warn("解析帖子列表失败，原始响应", "error", err, "data", string(data))
			return allPosts, fmt.Errorf("解析帖子列表失败: %w", err)
		}
		pageItems := len(postList.List)
		if pageItems == 0 {
			if progress != nil {
				progress(page, 0, len(allPosts))
			}
			break
		}

		allPosts = append(allPosts, postList.List...)
		if progress != nil {
			progress(page, pageItems, len(allPosts))
		}

		// 获取最后一条的 publish_sn 用于翻页
		lastPost := postList.List[len(postList.List)-1]
		if lastPost.PublishSN == 0 {
			break
		}
		if _, seen := seenCursors[lastPost.PublishSN]; seen {
			break
		}
		publishSN = lastPost.PublishSN
		seenCursors[publishSN] = struct{}{}
	}

	// 截断到 limit
	if limit > 0 && len(allPosts) > limit {
		allPosts = allPosts[:limit]
	}

	return allPosts, nil
}

// BackfillSource 把共享的 Afdian 解析实现适配为 backfill.Source。
type BackfillSource struct {
	route   *Route
	scraper *scraper.Scraper
}

// FeedIdentity 校验 Afdian feed 路径并返回作者 slug。
func (s *BackfillSource) FeedIdentity(feedURL string) (string, error) {
	return backfill.FeedIdentityFromURL(feedURL, "afdian")
}

// NewBackfillSource 创建串行且限速的 Afdian 历史适配器。
func NewBackfillSource(cookie string, requestInterval time.Duration) (*BackfillSource, error) {
	if requestInterval < time.Second {
		return nil, fmt.Errorf("Afdian 请求间隔不能小于 1s")
	}
	sc, err := scraper.New(scraper.Config{
		Cookies:           scraper.ParseCookieString(cookie),
		RateLimit:         requestInterval.Seconds(),
		Impersonate:       "chrome_131",
		TransportAttempts: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Afdian HTTP 客户端: %w", err)
	}
	return &BackfillSource{route: New(config.ResolvedRouteConfig{}), scraper: sc}, nil
}

// Discover 按新到旧遍历 Afdian 列表直到自然结束，不设置数量上限。
func (s *BackfillSource) Discover(
	ctx context.Context,
	authorSlug string,
	progress backfill.ProgressFunc,
) ([]backfill.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userID, err := s.route.getAuthorID(s.scraper, authorSlug)
	if err != nil {
		return nil, err
	}
	posts, err := s.route.getPostListWithProgress(s.scraper, userID, authorSlug, 0, func(page, pageItems, totalItems int) {
		if progress != nil {
			progress("Afdian 历史扫描进度",
				"source_id", authorSlug, "page", page, "page_items", pageItems, "scanned", totalItems)
		}
	})
	if err != nil {
		return nil, err
	}
	candidates := make([]backfill.Candidate, 0, len(posts))
	for _, post := range posts {
		var publishedAt time.Time
		if post.PublishTime > 0 {
			publishedAt = time.Unix(int64(post.PublishTime), 0).UTC()
		}
		candidates = append(candidates, backfill.Candidate{
			ID:          post.PostID,
			Title:       post.Title,
			Author:      post.User.Name,
			URL:         fmt.Sprintf("%s/p/%s", hostURL, post.PostID),
			PublishedAt: publishedAt,
		})
	}
	return candidates, nil
}

// Detail 获取一篇付费文章正文。
func (s *BackfillSource) Detail(ctx context.Context, candidate backfill.Candidate) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.route.getPostDetail(s.scraper, candidate.ID)
}

// Comments 获取评论，并复用 feed 路由的渲染行为。
func (s *BackfillSource) Comments(ctx context.Context, candidate backfill.Candidate) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	comments, err := s.route.getPostComments(s.scraper, candidate.ID)
	if err != nil {
		return "", err
	}
	return renderComments(comments), nil
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
		return "", route.NewHTTPError(resp.StatusCode, apiURL)
	}

	data, err := parseAfdianResponse(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}

	post := gjson.GetBytes(data, "post")
	if !post.Exists() {
		return "", nil
	}
	if !post.IsObject() {
		return "", fmt.Errorf("解析帖子详情失败: data.post 不是对象")
	}
	content := post.Get("content")
	if !content.Exists() || content.Type == gjson.Null {
		return "", nil
	}
	if content.Type != gjson.String {
		return "", fmt.Errorf("解析帖子详情失败: data.post.content 不是字符串")
	}
	return content.String(), nil
}

func (r *Route) getPostComments(sc *scraper.Scraper, postID string) (afdianCommentList, error) {
	apiURL := fmt.Sprintf("%s/api/comment/get-list?post_id=%s&publish_sn=&type=old&hot=1", hostURL, postID)
	referer := fmt.Sprintf("%s/p/%s", hostURL, postID)

	resp, err := sc.Get(apiURL, referer, nil)
	if err != nil {
		return afdianCommentList{}, err
	}
	if resp.StatusCode != 200 {
		return afdianCommentList{}, route.NewHTTPError(resp.StatusCode, apiURL)
	}

	data, err := parseAfdianResponse(resp.Body)
	if err != nil {
		return afdianCommentList{}, err
	}
	if len(data) == 0 {
		return afdianCommentList{}, nil
	}

	var comments afdianCommentList
	if err := json.Unmarshal(data, &comments); err != nil {
		return afdianCommentList{}, fmt.Errorf("解析帖子评论失败: %w", err)
	}
	return comments, nil
}

func renderComments(comments afdianCommentList) string {
	var sections []string
	if len(comments.HotList) > 0 {
		sections = append(sections, renderCommentSection("热评", comments.HotList))
	}
	if len(comments.List) > 0 {
		sections = append(sections, renderCommentSection("评论", comments.List))
	}
	return strings.Join(sections, "\n\n")
}

// appendPics 把图片动态的 pics 以 <img> 形式追加到正文末尾。
// pics 里的地址是完整的 CDN URL，原样引用；<p> 是块级元素，正文与图片之间无需额外分隔符。
func appendPics(content string, pics []string) string {
	var b strings.Builder
	for _, picURL := range pics {
		if picURL == "" {
			continue
		}
		fmt.Fprintf(&b, `<p><img src="%s"></p>`, html.EscapeString(picURL))
	}
	return content + b.String()
}

func appendRenderedComments(content string, comments afdianCommentList) string {
	rendered := renderComments(comments)
	if rendered == "" {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return rendered
	}
	return strings.TrimRight(content, "\n") + "\n\n" + rendered
}

func renderCommentSection(title string, comments []afdianComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h2>%s</h2>\n", html.EscapeString(title))
	for i, comment := range comments {
		b.WriteString("<hr>\n")
		fmt.Fprintf(
			&b,
			"<h5><span>[%d] %s by %s</span></h5>\n",
			i,
			formatCommentTime(comment.PublishTime),
			html.EscapeString(comment.User.Name),
		)
		if comment.ReplyUser != nil && comment.ReplyUser.Name != "" {
			fmt.Fprintf(&b, "<blockquote>回复 %s: </blockquote>\n", html.EscapeString(comment.ReplyUser.Name))
		}
		fmt.Fprintf(&b, "<p>%s</p>\n", html.EscapeString(comment.Content))
	}
	return strings.TrimSpace(b.String())
}

func formatCommentTime(publishTime afdianUnixTime) string {
	if publishTime <= 0 {
		return ""
	}
	return time.Unix(int64(publishTime), 0).UTC().Format("2006-01-02 15:04:05")
}
