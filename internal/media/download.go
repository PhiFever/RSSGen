package media

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime"
	"net/url"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/scraper"
	fhttp "github.com/bogdanfinn/fhttp"
)

func (a *Archiver) download(rc config.ResolvedRouteConfig, job imageJob) ([]byte, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := a.source(rc)
	if err != nil {
		return nil, "", "fetch"
	}
	source := job.source
	for redirects := 0; ; redirects++ {
		req, err := fhttp.NewRequestWithContext(ctx, "GET", source, nil)
		if err != nil {
			return nil, "", "fetch"
		}
		req.Header.Set("User-Agent", scraper.DefaultUserAgent)
		if job.referer != "" {
			req.Header.Set("Referer", job.referer)
		}
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", "fetch"
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			_ = resp.Body.Close()
			location, err := url.Parse(resp.Header.Get("Location"))
			if err != nil || redirects >= 3 || resp.Header.Get("Location") == "" {
				return nil, "", "redirect"
			}
			next, ok := a.normalize(req.URL.ResolveReference(location).String())
			if !ok {
				return nil, "", "redirect"
			}
			source = next
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return nil, "", "status"
		}
		max := int64(a.cfg.MaxSizeMB) * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		_ = resp.Body.Close()
		if err != nil {
			return nil, "", "fetch"
		}
		if int64(len(data)) > max {
			return nil, "", "too_large"
		}
		ext, mediaType := imageType(data)
		if ext == "" {
			return nil, "", "type"
		}
		declared, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if declared != "" && declared != mediaType {
			slog.Warn("图片声明类型与魔数不符", "url", safeSource(job.source))
		}
		return data, ext, ""
	}
}

func imageType(data []byte) (string, string) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "jpg", "image/jpeg"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", "image/png"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp", "image/webp"
	case len(data) >= 12 && string(data[4:8]) == "ftyp" && (string(data[8:12]) == "avif" || string(data[8:12]) == "avis"):
		return "avif", "image/avif"
	default:
		return "", ""
	}
}
