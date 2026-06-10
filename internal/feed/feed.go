// Package feed 提供 Feed 生成（Atom/RSS XML）。
package feed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"

	"github.com/PhiFever/RSSGen/internal/route"
)

// htmlPolicy 保留安全标签和属性，与 Python nh3 白名单对齐。
var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	// 文本格式
	p.AllowElements("p", "br", "strong", "em", "b", "i", "u", "s", "span", "blockquote", "pre", "code", "sub", "sup", "hr")
	// 标题
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6")
	// 列表
	p.AllowElements("ul", "ol", "li", "dl", "dt", "dd")
	// 表格
	p.AllowElements("table", "thead", "tbody", "tr", "th", "td", "div")
	// 链接
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	// 图片
	p.AllowAttrs("src", "alt", "title").OnElements("img")
	// 通用属性
	p.AllowAttrs("class", "style").Globally()
	return p
}()

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
	XMLName  xml.Name    `xml:"feed"`
	Xmlns    string      `xml:"xmlns,attr"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle,omitempty"`
	ID       string      `xml:"id"`
	Link     atomLink    `xml:"link"`
	Updated  string      `xml:"updated"`
	Entries  []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	XMLName     xml.Name       `xml:"entry"`
	Title       string         `xml:"title"`
	ID          string         `xml:"id"`
	Updated     string         `xml:"updated"`
	Published   string         `xml:"published,omitempty"`
	Link        *atomLink      `xml:"link,omitempty"`
	Content     *atomContent   `xml:"content,omitempty"`
	Author      *atomAuthor    `xml:"author,omitempty"`
	Category    []atomCategory `xml:"category,omitempty"`
	Enclosures  []route.Enclosure // 手动序列化为 <link rel="enclosure">
}

// atomEnclosureLink 用于序列化 Atom enclosure link 元素。
type atomEnclosureLink struct {
	XMLName xml.Name `xml:"link"`
	Href    string   `xml:"href,attr"`
	Rel     string   `xml:"rel,attr"`
	Type    string   `xml:"type,attr,omitempty"`
	Length  string   `xml:"length,attr,omitempty"`
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
	Enclosure   *rssEnclosure   `xml:"enclosure,omitempty"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr,omitempty"`
	Type   string `xml:"type,attr,omitempty"`
}

type rssDescription struct {
	XMLName xml.Name `xml:"description"`
	Data    string   `xml:",cdata"`
}

func generateAtom(info *route.FeedInfo, items []route.FeedItem) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	// 逐条编码 entry，手动插入 enclosure links
	var entriesXML []string
	for _, item := range items {
		entry := atomEntry{
			Title: orDefault(item.Title, "无标题"),
			ID:    orDefault(item.GUID, item.Link, fmt.Sprintf("urn:uuid:%d", time.Now().UnixNano())),
		}

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

		// 编码 entry 基础结构
		var entryBuf bytes.Buffer
		entryEnc := xml.NewEncoder(&entryBuf)
		entryEnc.Indent("", "  ")
		if err := entryEnc.Encode(entry); err != nil {
			return "", fmt.Errorf("Atom entry XML 编码失败: %w", err)
		}
		entryStr := entryBuf.String()

		// 在 </entry> 前插入 enclosure links
		if len(item.Enclosures) > 0 {
			var encLinks string
			for _, enc := range item.Enclosures {
				if enc.URL == "" {
					continue
				}
				encLink := atomEnclosureLink{
					Href:   enc.URL,
					Rel:    "enclosure",
					Type:   enc.Type,
					Length: enc.Length,
				}
				var linkBuf bytes.Buffer
				linkEnc := xml.NewEncoder(&linkBuf)
				if err := linkEnc.Encode(encLink); err != nil {
					continue
				}
				encLinks += "  " + linkBuf.String()
			}
			if encLinks != "" {
				closeTag := "</entry>"
				idx := strings.LastIndex(entryStr, closeTag)
				if idx >= 0 {
					entryStr = entryStr[:idx] + encLinks + "  " + entryStr[idx:]
				}
			}
		}

		entriesXML = append(entriesXML, entryStr)
	}

	// 构建完整 feed XML
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">`)
	buf.WriteString("\n")
	fmt.Fprintf(&buf, "  <title>%s</title>\n", xmlEscape(info.Title))
	if info.Description != "" {
		fmt.Fprintf(&buf, "  <subtitle>%s</subtitle>\n", xmlEscape(info.Description))
	}
	fmt.Fprintf(&buf, "  <id>%s</id>\n", xmlEscape(info.Link))
	fmt.Fprintf(&buf, "  <link href=\"%s\" rel=\"alternate\"/>\n", xmlEscape(info.Link))
	fmt.Fprintf(&buf, "  <updated>%s</updated>\n", now)
	for _, entryXML := range entriesXML {
		// 去掉 xml 声明行和外层缩进，重新缩进为 feed 内容
		lines := strings.Split(entryXML, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "<?xml") {
				continue
			}
			buf.WriteString("  ")
			buf.WriteString(trimmed)
			buf.WriteString("\n")
		}
	}
	buf.WriteString("</feed>\n")
	return buf.String(), nil
}

// xmlEscape 转义 XML 特殊字符。
func xmlEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
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

		// RSS 只取第一个 enclosure
		if len(item.Enclosures) > 0 && item.Enclosures[0].URL != "" {
			rItem.Enclosure = &rssEnclosure{
				URL:    item.Enclosures[0].URL,
				Length: item.Enclosures[0].Length,
				Type:   item.Enclosures[0].Type,
			}
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

// sanitizeHTML 使用 bluemonday 清理 HTML，保留白名单标签和属性。
func sanitizeHTML(content string) string {
	return htmlPolicy.Sanitize(content)
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
