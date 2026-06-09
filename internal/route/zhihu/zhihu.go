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
	"github.com/PhiFever/RSSGen/internal/scraper"
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
	dc0Re     = regexp.MustCompile(`d_c0=([^;]+)`)
	htmlTagRe = regexp.MustCompile(`<[^>]+>`)
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
	route.Register("zhihu", func(cfg config.ResolvedRouteConfig) route.Route {
		return New(cfg)
	})
}

// fetchActivitiesFunc 是 fetchActivities 的函数签名，用于测试注入。
type fetchActivitiesFunc func(userID string, limit int) ([]map[string]interface{}, error)

// Route 是知乎路由实现。
type Route struct {
	cfg              config.ResolvedRouteConfig
	actor            map[string]interface{} // 最近一次抓取的 actor 信息
	fetchActivitiesFn fetchActivitiesFunc  // 可替换的 fetchActivities（测试用）
}

// New 创建知乎路由实例。
func New(cfg config.ResolvedRouteConfig) *Route {
	return &Route{cfg: cfg}
}

func (r *Route) Name() string        { return "zhihu" }
func (r *Route) Description() string { return "知乎用户动态订阅" }
func (r *Route) FeedIDField() string { return "user_id" }

func (r *Route) getScraper() (*scraper.Scraper, error) {
	return scraper.New(scraper.Config{
		Cookies:     scraper.ParseCookieString(r.cfg.Cookie),
		RateLimit:   r.cfg.RateLimit,
		Proxy:       r.cfg.Proxy,
		Impersonate: r.cfg.Impersonate,
	})
}

func (r *Route) getDC0() (string, error) {
	matches := dc0Re.FindStringSubmatch(r.cfg.Cookie)
	if len(matches) < 2 {
		return "", fmt.Errorf("Cookie 中缺少 d_c0 字段")
	}
	return matches[1], nil
}

func (r *Route) FeedInfo(pathParams []string) (*route.FeedInfo, error) {
	if len(pathParams) == 0 {
		return nil, fmt.Errorf("需要指定用户 ID，如 /feed/zhihu/{user_id}")
	}
	userID := pathParams[0]

	displayName := userID

	if r.actor != nil {
		if name, ok := r.actor["name"].(string); ok && name != "" {
			displayName = name
		}
	}

	description := fmt.Sprintf("知乎用户 %s 的最新动态", displayName)
	if r.actor != nil {
		if headline, ok := r.actor["headline"].(string); ok && headline != "" {
			description = headline
		}
	}

	return &route.FeedInfo{
		Title:       fmt.Sprintf("知乎动态 - %s", displayName),
		Link:        fmt.Sprintf("https://www.zhihu.com/people/%s", userID),
		Description: description,
	}, nil
}

func (r *Route) Fetch(articleStore route.ArticleStore, pathParams []string, opts route.FetchOptions) ([]route.FeedItem, error) {
	if len(pathParams) == 0 {
		return nil, fmt.Errorf("需要指定用户 ID")
	}
	userID := pathParams[0]
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	// 获取 include 过滤
	include := opts.Include
	if len(include) == 0 {
		include = r.getFeedInclude(userID)
	}
	if len(include) == 0 {
		include = r.cfg.DefaultInclude
	}

	includeSelfInteraction := r.cfg.IncludeSelfInteraction

	fetchFn := r.fetchActivities
	if r.fetchActivitiesFn != nil {
		fetchFn = r.fetchActivitiesFn
	}
	activities, err := fetchFn(userID, limit)
	if err != nil {
		return nil, fmt.Errorf("获取知乎动态失败: %w", err)
	}

	var items []route.FeedItem
	for _, act := range activities {
		target, ok := act["target"].(map[string]interface{})
		if !ok || target == nil {
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

	return items, nil
}

// fetchActivities 请求知乎用户动态 API。
func (r *Route) fetchActivities(userID string, limit int) ([]map[string]interface{}, error) {
	dC0, err := r.getDC0()
	if err != nil {
		return nil, err
	}

	sc, err := r.getScraper()
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}
	referer := fmt.Sprintf("https://www.zhihu.com/people/%s", userID)

	nextURL := fmt.Sprintf(
		"https://www.zhihu.com/api/v3/moments/%s/activities?limit=5&desktop=true",
		userID,
	)

	var activities []map[string]interface{}

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
			return activities, &route.HTTPError{StatusCode: resp.StatusCode, URL: nextURL}
		}

		var data map[string]interface{}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return activities, fmt.Errorf("解析响应 JSON 失败: %w", err)
		}

		if dataList, ok := data["data"].([]interface{}); ok {
			for _, item := range dataList {
				if act, ok := item.(map[string]interface{}); ok {
					activities = append(activities, act)
				}
			}
		}

		// 翻页
		paging, ok := data["paging"].(map[string]interface{})
		if !ok {
			break
		}
		if isEnd, ok := paging["is_end"].(bool); ok && isEnd {
			break
		}
		next, ok := paging["next"].(string)
		if !ok || next == "" {
			break
		}
		nextURL = next
	}

	// 截断到 limit
	if len(activities) > limit {
		activities = activities[:limit]
	}

	// 提取 actor 信息
	if len(activities) > 0 {
		if actor, ok := activities[0]["actor"].(map[string]interface{}); ok {
			r.actor = actor
		}
	}

	return activities, nil
}

// getFeedInclude 从配置中获取指定用户的 include 过滤。
func (r *Route) getFeedInclude(userID string) []string {
	for _, f := range r.cfg.Feeds {
		if f.UserID == userID {
			return f.Include
		}
	}
	return nil
}

// makeFeedItem 根据 activity dict 构造 FeedItem。
func (r *Route) makeFeedItem(act map[string]interface{}) route.FeedItem {
	target, _ := act["target"].(map[string]interface{})
	if target == nil {
		target = make(map[string]interface{})
	}

	targetID, _ := target["id"].(string)
	targetType, _ := target["type"].(string)
	if targetType == "" {
		targetType = "unknown"
	}
	category := deriveCategory(act)

	var title, link string

	switch targetType {
	case "answer":
		question, _ := target["question"].(map[string]interface{})
		if question != nil {
			title, _ = question["title"].(string)
			questionID, _ := question["id"].(string)
			link = fmt.Sprintf("https://www.zhihu.com/question/%s/answer/%s", questionID, targetID)
		}
	case "article":
		title, _ = target["title"].(string)
		link = fmt.Sprintf("https://zhuanlan.zhihu.com/p/%s", targetID)
	case "pin":
		excerpt, _ := target["excerpt_title"].(string)
		if excerpt == "" {
			excerpt, _ = target["excerpt"].(string)
		}
		cleaned := truncateRunes(htmlTagRe.ReplaceAllString(excerpt, ""), 50)
		if cleaned != "" {
			title = cleaned
		} else {
			title = "想法"
		}
		link = fmt.Sprintf("https://www.zhihu.com/pin/%s", targetID)
	case "question":
		title, _ = target["title"].(string)
		link = fmt.Sprintf("https://www.zhihu.com/question/%s", targetID)
	default:
		title, _ = target["title"].(string)
		if title == "" {
			excerpt, _ := target["excerpt"].(string)
			title = truncateRunes(excerpt, 50)
		}
		if title == "" {
			title = "未知内容"
		}
		link = fmt.Sprintf("https://www.zhihu.com/%s/%s", targetType, targetID)
	}

	// 空 title 通常意味着抓取字段的位置变了（如 question.title 改名）
	if title == "" {
		slog.Warn("知乎动态条目 title 为空", "target_id", targetID, "target_type", targetType, "verb", act["verb"])
	}

	// 添加 action_text 前缀
	actionText, _ := act["action_text"].(string)
	if actionText != "" {
		title = fmt.Sprintf("[%s] %s", actionText, title)
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
	createdTime, _ := act["created_time"].(float64)
	if createdTime == 0 {
		createdTime, _ = target["created_time"].(float64)
	}
	if createdTime == 0 {
		createdTime, _ = target["created"].(float64)
	}
	if createdTime > 0 {
		t := time.Unix(int64(createdTime), 0).UTC()
		pubDate = &t
	}

	// 获取内容
	var content string
	if rawContent, ok := target["content"].([]interface{}); ok {
		content = renderPinContent(rawContent)
	} else if rawContent, ok := target["content"].(string); ok {
		content = rawContent
	} else if detail, ok := target["detail"].(string); ok {
		content = detail
	} else if excerpt, ok := target["excerpt"].(string); ok {
		content = excerpt
	}
	content = fixLazyImages(content)

	// answer 类型：在正文前添加问题描述
	if targetType == "answer" {
		question, _ := target["question"].(map[string]interface{})
		if question != nil {
			questionDetail, _ := question["detail"].(string)
			if questionDetail != "" {
				questionDesc := formatQuestionDescription(questionDetail)
				content = questionDesc + "\n<br/>\n" + content
			}
		}
	}

	// 提取作者
	author := ""
	if authorMap, ok := target["author"].(map[string]interface{}); ok {
		author, _ = authorMap["name"].(string)
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

// deriveCategory 从 activity 派生 category。
func deriveCategory(act map[string]interface{}) string {
	verb, _ := act["verb"].(string)
	if cat, ok := verbCategoryMap[verb]; ok {
		return cat
	}
	target, _ := act["target"].(map[string]interface{})
	if target == nil {
		return "unknown"
	}
	targetType, _ := target["type"].(string)
	if cat, ok := targetTypeFallback[targetType]; ok {
		return cat
	}
	slog.Warn("未识别的知乎动态", "verb", verb, "target_type", targetType, "action_text", act["action_text"])
	return targetType
}

// isSelfInteraction 判定 activity 是否为作者对自己内容的互动。
func isSelfInteraction(act map[string]interface{}) bool {
	verb, _ := act["verb"].(string)
	if !selfInteractableVerbs[verb] {
		return false
	}
	actor, _ := act["actor"].(map[string]interface{})
	target, _ := act["target"].(map[string]interface{})
	if actor == nil || target == nil {
		return false
	}
	actorID, _ := actor["id"].(string)
	authorMap, _ := target["author"].(map[string]interface{})
	if authorMap == nil {
		return false
	}
	targetAuthorID, _ := authorMap["id"].(string)
	return actorID != "" && actorID == targetAuthorID
}

// renderPinContent 将 PIN 类型的 content blocks 渲染为 HTML。
func renderPinContent(blocks []interface{}) string {
	var parts []string
	for _, block := range blocks {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if text, ok := blockMap["content"].(string); ok && text != "" {
			parts = append(parts, text)
			continue
		}
		blockType, _ := blockMap["type"].(string)
		switch blockType {
		case "image":
			url, _ := blockMap["original_url"].(string)
			if url == "" {
				url, _ = blockMap["url"].(string)
			}
			if url != "" {
				parts = append(parts, fmt.Sprintf(`<img src="%s" />`, html.EscapeString(url)))
			}
		case "link_card":
			url, _ := blockMap["url"].(string)
			linkTitle, _ := blockMap["data_draft_title"].(string)
			if linkTitle == "" {
				linkTitle = url
			}
			if url != "" {
				parts = append(parts, fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(url), html.EscapeString(linkTitle)))
			}
		}
	}
	return strings.Join(parts, "<br/>")
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
