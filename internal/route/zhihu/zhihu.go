// Package zhihu 实现知乎用户动态路由。
package zhihu

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	signzhihu "github.com/PhiFever/RSSGen/internal/sign/zhihu"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// 动态 category 常量
const (
	TypeAnswer           = "answer"
	TypeArticle          = "article"
	TypePin              = "pin"
	TypeCollectedAnswer  = "collected_answer"
	TypeCollectedArticle = "collected_article"
	TypeCollectedPin     = "collected_pin"
	TypeVoteupAnswer     = "voteup_answer"
	TypeVoteupArticle    = "voteup_article"
	TypeFollowedQuestion = "followed_question"
)

// PIN 内容 block 类型常量。
const (
	PinBlockImage    = "image"
	PinBlockLinkCard = "link_card"
	PinBlockText     = "text"
	PinBlockVideo    = "video"
)

// verbCategoryMap 将 verb 映射到 category。
var verbCategoryMap = map[string]string{
	"MEMBER_ANSWER_QUESTION": TypeAnswer,
	"MEMBER_CREATE_ARTICLE":  TypeArticle,
	"MEMBER_CREATE_PIN":      TypePin,
	"MEMBER_COLLECT_ANSWER":  TypeCollectedAnswer,
	"MEMBER_COLLECT_ARTICLE": TypeCollectedArticle,
	"MEMBER_COLLECT_PIN":     TypeCollectedPin,
	"MEMBER_VOTEUP_ANSWER":   TypeVoteupAnswer,
	"MEMBER_VOTEUP_ARTICLE":  TypeVoteupArticle,
	"MEMBER_FOLLOW_QUESTION": TypeFollowedQuestion,
}

// targetTypeFallback 当 verb 缺失时按 target.type 兜底。
var targetTypeFallback = map[string]string{
	"answer":   TypeAnswer,
	"article":  TypeArticle,
	"pin":      TypePin,
	"question": TypeFollowedQuestion,
}

// 预编译正则，避免在热路径重复编译。
var (
	zhihuHostURL = "https://www.zhihu.com"
	dc0Re        = regexp.MustCompile(`d_c0=([^;]+)`)
	htmlTagRe    = regexp.MustCompile(`<[^>]+>`)
)

// selfInteractableVerbs 仅这些 verb 在 actor == target.author 时算作"自互动"。
var selfInteractableVerbs = map[string]bool{
	"MEMBER_COLLECT_ANSWER":  true,
	"MEMBER_COLLECT_ARTICLE": true,
	"MEMBER_COLLECT_PIN":     true,
	"MEMBER_VOTEUP_ANSWER":   true,
	"MEMBER_VOTEUP_ARTICLE":  true,
}

func init() {
	route.Register("zhihu", "知乎用户动态订阅", func(cfg config.ResolvedRouteConfig) route.Route {
		return New(cfg)
	})
}

// fetchActivitiesFunc 是 fetchActivities 的函数签名，用于测试注入。
type fetchActivitiesFunc func(userID string, limit int) ([]zhihuActivity, error)

type activitiesResponse struct {
	Data   []zhihuActivity `json:"data"`
	Paging zhihuPaging     `json:"paging"`
}

type zhihuPaging struct {
	IsEnd bool   `json:"is_end"`
	Next  string `json:"next"`
}

type zhihuActivity struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Target      *zhihuTarget    `json:"target"`
	Verb        string          `json:"verb"`
	ActionText  string          `json:"action_text"`
	CreatedTime float64         `json:"created_time"`
	Actor       *zhihuPerson    `json:"actor"`
	Raw         json.RawMessage `json:"-"`
}

type zhihuTarget struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Title        string          `json:"title"`
	Content      json.RawMessage `json:"content"`
	Detail       string          `json:"detail"`
	Excerpt      string          `json:"excerpt"`
	ExcerptTitle string          `json:"excerpt_title"`
	CreatedTime  float64         `json:"created_time"`
	Created      float64         `json:"created"`
	Author       *zhihuPerson    `json:"author"`
	Question     *zhihuQuestion  `json:"question"`
	Raw          json.RawMessage `json:"-"`
}

type zhihuQuestion struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type zhihuPerson struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Headline string `json:"headline"`
}

type zhihuPinBlock struct {
	Type           string                 `json:"type"`
	Content        string                 `json:"content"`
	Text           string                 `json:"text"`
	OriginalURL    string                 `json:"original_url"`
	URL            string                 `json:"url"`
	DataDraftTitle string                 `json:"data_draft_title"`
	Thumbnail      string                 `json:"thumbnail"`
	Width          float64                `json:"width"`
	Playlist       []zhihuPinPlaylistItem `json:"playlist"`
	VideoInfo      *zhihuVideoInfo        `json:"video_info"`
	Raw            json.RawMessage        `json:"-"`
}

type zhihuVideoInfo struct {
	Thumbnail string                          `json:"thumbnail"`
	Playlist  map[string]zhihuPinPlaylistItem `json:"playlist"`
}

type zhihuPinPlaylistItem struct {
	Quality string `json:"quality"`
	URL     string `json:"url"`
	PlayURL string `json:"play_url"`
}

// Route 是知乎路由实现。
type Route struct {
	route.BaseRoute
	cfg               config.ResolvedRouteConfig
	fetchActivitiesFn fetchActivitiesFunc // 可替换的 fetchActivities（测试用）
}

// New 创建知乎路由实例。
func New(cfg config.ResolvedRouteConfig) *Route {
	return &Route{
		BaseRoute: route.NewBaseRoute("zhihu"),
		cfg:       cfg,
	}
}

func (r *Route) getDC0() (string, error) {
	matches := dc0Re.FindStringSubmatch(r.cfg.Cookie)
	if len(matches) < 2 {
		return "", fmt.Errorf("cookie 中缺少 d_c0 字段")
	}
	return matches[1], nil
}

func (r *Route) feedInfo(pathParams []string, actor *zhihuPerson) (route.FeedInfo, error) {
	if len(pathParams) == 0 {
		return route.FeedInfo{}, fmt.Errorf("需要指定用户 ID，如 /feed/zhihu/{user_id}")
	}
	userID := pathParams[0]

	displayName := userID

	if actor != nil && actor.Name != "" {
		displayName = actor.Name
	}

	description := fmt.Sprintf("知乎用户 %s 的最新动态", displayName)
	if actor != nil && actor.Headline != "" {
		description = actor.Headline
	}

	return route.FeedInfo{
		Title:       fmt.Sprintf("知乎动态 - %s", displayName),
		Link:        fmt.Sprintf("%s/people/%s", zhihuHostURL, userID),
		Description: description,
	}, nil
}

func (r *Route) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) (route.FeedResult, error) {
	if len(pathParams) == 0 {
		return route.FeedResult{}, fmt.Errorf("需要指定用户 ID")
	}
	userID := pathParams[0]
	limit := opts.Limit

	include := opts.Include
	includeSelfInteraction := r.cfg.IncludeSelfInteraction

	fetchFn := r.fetchActivities
	if r.fetchActivitiesFn != nil {
		fetchFn = r.fetchActivitiesFn
	}
	activities, err := fetchFn(userID, limit)
	if err != nil {
		return route.FeedResult{}, fmt.Errorf("获取知乎动态失败: %w", err)
	}

	info, err := r.feedInfo(pathParams, actorFromActivities(activities))
	if err != nil {
		return route.FeedResult{}, err
	}

	var items []route.FeedItem
	for _, act := range activities {
		if act.Target == nil {
			continue
		}

		if !includeSelfInteraction && isSelfInteraction(act) {
			continue
		}

		category := deriveCategory(act)
		if len(include) > 0 && !contains(include, category) {
			continue
		}

		item := r.makeFeedItem(act)
		items = append(items, item)
	}

	return route.FeedResult{Info: info, Items: items}, nil
}

// fetchActivities 请求知乎用户动态 API。
func (r *Route) fetchActivities(userID string, limit int) ([]zhihuActivity, error) {
	dC0, err := r.getDC0()
	if err != nil {
		return nil, err
	}

	sc, err := r.Scraper(r.cfg)
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}
	referer := fmt.Sprintf("%s/people/%s", zhihuHostURL, userID)

	nextURL := fmt.Sprintf(
		"%s/api/v3/moments/%s/activities?limit=5&desktop=true",
		zhihuHostURL, userID,
	)

	var activities []zhihuActivity

	for nextURL != "" && len(activities) < limit {
		// 获取签名
		signResult, err := signzhihu.GetSignature(nextURL, dC0, "")
		if err != nil {
			return activities, fmt.Errorf("获取签名失败: %w", err)
		}

		headers := map[string]string{
			"accept":           "*/*",
			"x-requested-with": "fetch",
			"x-zse-93":         signResult.XZSE93,
			"x-zse-96":         signResult.XZSE96,
		}

		resp, err := sc.Get(nextURL, referer, headers)
		if err != nil {
			return activities, err
		}
		if resp.StatusCode != 200 {
			return activities, route.NewHTTPError(resp.StatusCode, nextURL)
		}

		var data activitiesResponse
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return activities, fmt.Errorf("解析响应 JSON 失败: %w", err)
		}

		activities = append(activities, data.Data...)

		// 翻页
		if data.Paging.IsEnd {
			break
		}
		if data.Paging.Next == "" {
			break
		}
		nextURL = data.Paging.Next
	}

	// 截断到 limit
	if len(activities) > limit {
		activities = activities[:limit]
	}

	return activities, nil
}

func actorFromActivities(activities []zhihuActivity) *zhihuPerson {
	if len(activities) == 0 {
		return nil
	}
	return activities[0].Actor
}

// makeFeedItem 根据 activity 构造 FeedItem。
func (r *Route) makeFeedItem(act zhihuActivity) route.FeedItem {
	target := act.Target
	if target == nil {
		target = &zhihuTarget{}
	}

	targetID := target.ID
	targetType := target.Type
	if targetType == "" {
		targetType = "unknown"
	}
	category := deriveCategory(act)

	var title, link string

	switch targetType {
	case "answer":
		if target.Question != nil {
			title = target.Question.Title
			questionID := target.Question.ID
			link = fmt.Sprintf("%s/question/%s/answer/%s", zhihuHostURL, questionID, targetID)
		}
	case "article":
		title = target.Title
		link = fmt.Sprintf("https://zhuanlan.zhihu.com/p/%s", targetID)
	case "pin":
		excerpt := target.ExcerptTitle
		if excerpt == "" {
			excerpt = target.Excerpt
		}
		cleaned := truncateRunes(htmlTagRe.ReplaceAllString(excerpt, ""), 50)
		if cleaned != "" {
			title = cleaned
		} else {
			title = "想法"
		}
		link = fmt.Sprintf("%s/pin/%s", zhihuHostURL, targetID)
	case "question":
		title = target.Title
		link = fmt.Sprintf("%s/question/%s", zhihuHostURL, targetID)
	default:
		title = target.Title
		if title == "" {
			title = truncateRunes(target.Excerpt, 50)
		}
		if title == "" {
			title = "未知内容"
		}
		link = fmt.Sprintf("%s/%s/%s", zhihuHostURL, targetType, targetID)
	}

	// 空 title 通常意味着抓取字段的位置变了（如 question.title 改名）
	if title == "" {
		slog.Warn("知乎动态条目 title 为空", "target_id", targetID, "target_type", targetType, "verb", act.Verb)
	}

	// 添加 action_text 前缀
	if act.ActionText != "" {
		title = fmt.Sprintf("[%s] %s", act.ActionText, title)
	}

	// 生成 guid
	var guid string
	if category == TypeAnswer || category == TypeArticle || category == TypePin {
		guid = targetID
	} else {
		guid = fmt.Sprintf("%s_%s", category, targetID)
	}

	// 解析时间
	var pubDate *time.Time
	createdTime := act.CreatedTime
	if createdTime == 0 {
		createdTime = target.CreatedTime
	}
	if createdTime == 0 {
		createdTime = target.Created
	}
	if createdTime > 0 {
		t := time.Unix(int64(createdTime), 0).UTC()
		pubDate = &t
	}

	// 获取内容
	var content string
	if rawContent, ok := target.renderContent(link); ok {
		content = rawContent
	} else if target.Detail != "" {
		content = target.Detail
	} else if target.Excerpt != "" {
		content = target.Excerpt
	}
	content = fixLazyImages(content)

	// answer 类型：在正文前添加问题描述
	if targetType == "answer" {
		if target.Question != nil {
			if target.Question.Detail != "" {
				questionDesc := formatQuestionDescription(target.Question.Detail)
				content = questionDesc + "\n<br/>\n" + content
			}
		}
	}

	// 提取作者
	author := ""
	if target.Author != nil {
		author = target.Author.Name
	}

	return route.FeedItem{
		Title:      title,
		Link:       link,
		Content:    content,
		PubDate:    pubDate,
		Author:     author,
		GUID:       guid,
		Categories: []string{category},
	}
}

func (t *zhihuTarget) renderContent(zhihuURL string) (string, bool) {
	if t == nil || len(t.Content) == 0 {
		return "", false
	}
	raw := strings.TrimSpace(string(t.Content))
	if raw == "" || raw == "null" {
		return "", false
	}

	var content string
	if err := json.Unmarshal(t.Content, &content); err == nil {
		return content, true
	}

	var blocks []zhihuPinBlock
	if err := json.Unmarshal(t.Content, &blocks); err == nil {
		return renderPinContent(blocks, zhihuURL), true
	}

	return "", false
}

// deriveCategory 从 activity 派生 category。
func deriveCategory(act zhihuActivity) string {
	if cat, ok := verbCategoryMap[act.Verb]; ok {
		return cat
	}
	if act.Target == nil {
		return "unknown"
	}
	if cat, ok := targetTypeFallback[act.Target.Type]; ok {
		return cat
	}
	slog.Warn("未识别的知乎动态", "verb", act.Verb, "target_type", act.Target.Type, "action_text", act.ActionText)
	return act.Target.Type
}

// isSelfInteraction 判定 activity 是否为作者对自己内容的互动。
func isSelfInteraction(act zhihuActivity) bool {
	if !selfInteractableVerbs[act.Verb] {
		return false
	}
	if act.Actor == nil || act.Target == nil {
		return false
	}
	if act.Target.Author == nil {
		return false
	}
	return act.Actor.ID != "" && act.Actor.ID == act.Target.Author.ID
}

// renderPinContent 将 PIN 类型的 content blocks 渲染为 HTML。
func renderPinContent(blocks []zhihuPinBlock, zhihuURL string) string {
	var parts []string
	for _, block := range blocks {
		if block.Content != "" {
			parts = append(parts, block.Content)
			continue
		}
		switch block.Type {
		case PinBlockText:
			if block.Text != "" {
				parts = append(parts, html.EscapeString(block.Text))
			}
		case PinBlockImage:
			url := block.OriginalURL
			if url == "" {
				url = block.URL
			}
			if url != "" {
				parts = append(parts, fmt.Sprintf(`<img src="%s" />`, html.EscapeString(url)))
			}
		case PinBlockLinkCard:
			url := block.URL
			linkTitle := block.DataDraftTitle
			if linkTitle == "" {
				linkTitle = url
			}
			if url != "" {
				parts = append(parts, fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(url), html.EscapeString(linkTitle)))
			}
		case PinBlockVideo:
			if rendered := renderPinVideoBlock(block); rendered != "" {
				parts = append(parts, rendered)
			} else {
				slog.Warn("知乎 pin video block 缺少可渲染 URL", "block_type", block.Type, "zhihu_url", zhihuURL)
			}
		default:
			if block.Type != "" {
				slog.Warn("未识别的知乎 pin block 类型", "block_type", block.Type, "zhihu_url", zhihuURL)
			}
		}
	}
	return strings.Join(parts, "<br/>")
}

func renderPinVideoBlock(block zhihuPinBlock) string {
	videoURL := selectPinVideoURL(block)
	if videoURL == "" {
		return ""
	}
	if !strings.Contains(videoURL, ".mp4") {
		return fmt.Sprintf(`<a href="%s">查看视频</a>`, html.EscapeString(videoURL))
	}

	poster := block.Thumbnail
	if poster == "" && block.VideoInfo != nil {
		poster = block.VideoInfo.Thumbnail
	}

	var attrs []string
	attrs = append(attrs, `controls`, `preload="metadata"`)
	if poster != "" {
		attrs = append(attrs, fmt.Sprintf(`poster="%s"`, html.EscapeString(poster)))
	}
	if block.Width > 0 {
		attrs = append(attrs, fmt.Sprintf(`width="%.0f"`, block.Width))
	}
	return fmt.Sprintf(`<video %s><source src="%s" type="video/mp4" /></video>`, strings.Join(attrs, " "), html.EscapeString(videoURL))
}

func selectPinVideoURL(block zhihuPinBlock) string {
	if len(block.Playlist) > 0 {
		if url := selectPinVideoURLFromList(block.Playlist); url != "" {
			return url
		}
	}
	if block.VideoInfo != nil && block.VideoInfo.Playlist != nil {
		for _, quality := range []string{"fhd", "hd", "sd", "ld"} {
			item, ok := block.VideoInfo.Playlist[quality]
			if !ok {
				continue
			}
			if item.PlayURL != "" {
				return item.PlayURL
			}
			if item.URL != "" {
				return item.URL
			}
		}
	}
	return block.URL
}

func selectPinVideoURLFromList(playlist []zhihuPinPlaylistItem) string {
	bestRank := len(pinVideoQualityRank)
	bestURL := ""
	for _, item := range playlist {
		if item.URL == "" {
			continue
		}
		rank, ok := pinVideoQualityRank[item.Quality]
		if !ok {
			rank = len(pinVideoQualityRank)
		}
		if bestURL == "" || rank < bestRank {
			bestRank = rank
			bestURL = item.URL
		}
	}
	return bestURL
}

var pinVideoQualityRank = map[string]int{
	"fhd": 0,
	"hd":  1,
	"sd":  2,
	"ld":  3,
}

// fixLazyImages 修复知乎懒加载图片：将 SVG 占位符的 src 替换为 data-actualsrc /
// data-original 指向的真实链接，并移除 <noscript>（其内容会被 RSS 阅读器忽略）。
// 占位符 src 值本身含 '<'/'>'，无法用正则安全提取属性，故用 HTML 解析器处理。
func fixLazyImages(content string) string {
	if content == "" {
		return content
	}
	nodes, err := xhtml.ParseFragment(strings.NewReader(content), &xhtml.Node{
		Type:     xhtml.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return content
	}
	var buf strings.Builder
	for _, n := range nodes {
		if n.Type == xhtml.ElementNode && n.Data == "noscript" {
			continue
		}
		fixLazyNode(n)
		if err := xhtml.Render(&buf, n); err != nil {
			return content
		}
	}
	return buf.String()
}

// fixLazyNode 递归处理节点：修复 img 占位符，并移除 noscript 子节点。
func fixLazyNode(n *xhtml.Node) {
	var noscripts []*xhtml.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == "noscript" {
			noscripts = append(noscripts, c)
			continue
		}
		fixLazyNode(c)
	}
	for _, c := range noscripts {
		n.RemoveChild(c)
	}
	if n.Type == xhtml.ElementNode && n.Data == "img" {
		fixLazyImg(n)
	}
}

// fixLazyImg 将单个 img 的 SVG 占位符 src 替换为真实链接，并清理懒加载属性。
func fixLazyImg(n *xhtml.Node) {
	var src, actualSrc, original string
	for _, a := range n.Attr {
		switch a.Key {
		case "src":
			src = a.Val
		case "data-actualsrc":
			actualSrc = a.Val
		case "data-original":
			original = a.Val
		}
	}
	if !strings.HasPrefix(src, "data:image/svg+xml") {
		return
	}
	// 优先 data-actualsrc，其次 data-original
	realSrc := actualSrc
	if realSrc == "" {
		realSrc = original
	}
	if realSrc == "" {
		return
	}
	attrs := n.Attr[:0]
	for _, a := range n.Attr {
		switch a.Key {
		case "src":
			a.Val = realSrc
			attrs = append(attrs, a)
		case "data-actualsrc", "data-original":
			// 移除懒加载属性
		case "class":
			if cls := removeLazyClass(a.Val); cls != "" {
				a.Val = cls
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, a)
		}
	}
	n.Attr = attrs
}

// removeLazyClass 从 class 列表中移除 "lazy"，保留其余 class。
func removeLazyClass(class string) string {
	fields := strings.Fields(class)
	kept := fields[:0]
	for _, f := range fields {
		if f != "lazy" {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, " ")
}

// formatQuestionDescription 将问题描述 HTML 转换为引用块格式。
func formatQuestionDescription(detail string) string {
	if detail == "" {
		return ""
	}
	detail = fixLazyImages(detail)
	return fmt.Sprintf("<h3>【问题描述】</h3>\n<blockquote>\n%s\n</blockquote>", detail)
}

// truncateRunes 按 rune（而非字节）截断字符串，避免切断多字节中文产生非法 UTF-8。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// contains 检查字符串切片是否包含指定值。
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
