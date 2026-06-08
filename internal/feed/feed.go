// Package feed 提供 Feed 生成（Atom/RSS XML）。
package feed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/route"
)

// Generate 将 FeedInfo 和 FeedItem 列表转为 Atom 或 RSS XML。
func Generate(info *route.FeedInfo, items []route.FeedItem, format string) (string, error) {
	switch format {
	case "rss":
		return generateRSS(info, items)
	default:
		return generateAtom(info, items)
	}
}

// atomFeed 是 Atom 1.0 feed 的 XML 结构。
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Xmlns   string      `xml:"xmlns,attr"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Link    atomLink    `xml:"link"`
	Updated string      `xml:"updated"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title     string        `xml:"title"`
	ID        string        `xml:"id"`
	Updated   string        `xml:"updated"`
	Published string        `xml:"published,omitempty"`
	Link      *atomLink     `xml:"link,omitempty"`
	Content   *atomContent  `xml:"content,omitempty"`
	Author    *atomAuthor   `xml:"author,omitempty"`
	Category  []atomCategory `xml:"category,omitempty"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Data string `xml:",chardata"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

// rssFeed 是 RSS 2.0 feed 的 XML 结构。
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string          `xml:"title"`
	Link        string          `xml:"link"`
	Description *rssDescription `xml:"description,omitempty"`
	PubDate     string          `xml:"pubDate,omitempty"`
	GUID        string          `xml:"guid,omitempty"`
	Author      string          `xml:"author,omitempty"`
	Category    []string        `xml:"category,omitempty"`
}

type rssDescription struct {
	XMLName xml.Name `xml:"description"`
	Data    string   `xml:",cdata"`
}

var epoch = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

func generateAtom(info *route.FeedInfo, items []route.FeedItem) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	feed := atomFeed{
		Xmlns: "http://www.w3.org/2005/Atom",
		Title: info.Title,
		ID:    info.Link,
		Link:  atomLink{Href: info.Link, Rel: "alternate"},
		Updated: now,
	}

	for _, item := range items {
		entry := atomEntry{
			Title: orDefault(item.Title, "无标题"),
			ID:    orDefault(item.GUID, item.Link, fmt.Sprintf("urn:uuid:%d", time.Now().UnixNano())),
		}

		// 时间处理
		if item.PubDate != nil {
			entry.Updated = item.PubDate.Format(time.RFC3339)
			entry.Published = item.PubDate.Format(time.RFC3339)
		} else {
			entry.Updated = now
		}

		if item.Link != "" {
			entry.Link = &atomLink{Href: item.Link}
		}

		if item.Content != "" {
			safeContent := sanitizeHTML(item.Content)
			entry.Content = &atomContent{Type: "html", Data: safeContent}
		}

		if item.Author != "" {
			entry.Author = &atomAuthor{Name: item.Author}
		}

		for _, cat := range item.Categories {
			entry.Category = append(entry.Category, atomCategory{Term: cat})
		}

		feed.Entries = append(feed.Entries, entry)
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		return "", fmt.Errorf("Atom XML 编码失败: %w", err)
	}
	return buf.String(), nil
}

func generateRSS(info *route.FeedInfo, items []route.FeedItem) (string, error) {
	channel := rssChannel{
		Title:       info.Title,
		Link:        info.Link,
		Description: info.Description,
	}

	for _, item := range items {
		rItem := rssItem{
			Title: orDefault(item.Title, "无标题"),
			Link:  item.Link,
			GUID:  orDefault(item.GUID, item.Link),
		}

		if item.PubDate != nil {
			rItem.PubDate = item.PubDate.Format(time.RFC1123Z)
		}

		if item.Content != "" {
			safeContent := sanitizeHTML(item.Content)
			rItem.Description = &rssDescription{Data: safeContent}
		}

		if item.Author != "" {
			rItem.Author = item.Author
		}

		for _, cat := range item.Categories {
			rItem.Category = append(rItem.Category, cat)
		}

		channel.Items = append(channel.Items, rItem)
	}

	feed := rssFeed{
		Version: "2.0",
		Channel: channel,
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		return "", fmt.Errorf("RSS XML 编码失败: %w", err)
	}
	return buf.String(), nil
}

// sanitizeHTML 对 HTML 内容进行基本的安全处理。
// 保留安全标签，转义危险属性。
func sanitizeHTML(content string) string {
	// 简单实现：保留基本安全标签，转义 script 等危险标签
	// 生产环境应使用更完善的 HTML 清理库
	content = strings.ReplaceAll(content, "<script", "&lt;script")
	content = strings.ReplaceAll(content, "</script>", "&lt;/script&gt;")
	content = strings.ReplaceAll(content, "<iframe", "&lt;iframe")
	content = strings.ReplaceAll(content, "</iframe>", "&lt;/iframe&gt;")
	return content
}

// orDefault 返回第一个非空字符串。
func orDefault(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// EscapeXML 转义 XML 特殊字符。
func EscapeXML(s string) string {
	return html.EscapeString(s)
}
