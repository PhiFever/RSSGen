package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
	"github.com/PhiFever/RSSGen/internal/route"
	"github.com/PhiFever/RSSGen/internal/scraper"
	fhttp "github.com/bogdanfinn/fhttp"
)

var png = []byte("\x89PNG\r\n\x1a\n图片")

const sourceURL = "https://pic1.zhimg.com/image"

type fakeSource func(*fhttp.Request) (*fhttp.Response, error)

func (f fakeSource) Do(r *fhttp.Request) (*fhttp.Response, error) { return f(r) }
func sourceResponse(status int, body []byte, location string) *fhttp.Response {
	return &fhttp.Response{StatusCode: status, Header: fhttp.Header{"Location": []string{location}}, Body: io.NopCloser(bytes.NewReader(body))}
}

// newTestArchiver 的假 Immich 对存在性检验恒返回未命中。
func newTestArchiver(t *testing.T, handler http.HandlerFunc, source fakeSource) *Archiver {
	t.Helper()
	return newTestArchiverWithSearch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search/metadata" {
			_, _ = io.WriteString(w, `{"assets":{"items":[]}}`)
			return
		}
		handler(w, r)
	}, source)
}
func newTestArchiverWithSearch(t *testing.T, handler http.HandlerFunc, source fakeSource) *Archiver {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	a := New(config.MediaConfig{Immich: config.ImmichConfig{URL: server.URL, PublicURL: server.URL, APIKey: "secret", AlbumID: "album", ShareKey: "k"}, AllowedHosts: []string{"*.zhimg.com"}, MaxSizeMB: 1})
	a.newSourceClient = func(impersonate, proxy string) (sourceClient, error) { return source, nil }
	return a
}
func imageItems() []route.FeedItem {
	return []route.FeedItem{{Content: `<img src="` + sourceURL + `">`, Link: "https://article.example/post"}}
}
func assertImmichError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("应返回 Immich 错误")
	}
	var business *route.HTTPError
	if errors.As(err, &business) {
		t.Fatalf("不应包装 route.HTTPError：%v", err)
	}
}

func TestArchiveUploadAndMemoryCache(t *testing.T) {
	var sourceCalls, uploads, albums atomic.Int32
	published := time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC)
	a := newTestArchiver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("请求缺少 API key")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/assets":
			uploads.Add(1)
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("visibility") != "archive" {
				t.Error("visibility 应为 archive")
			}
			file, header, err := r.FormFile("assetData")
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if !bytes.Equal(data, png) || !strings.HasSuffix(header.Filename, ".png") || r.FormValue("filename") != header.Filename {
				t.Error("文件内容或魔数扩展名错误")
			}
			for _, key := range []string{"fileCreatedAt", "fileModifiedAt"} {
				if r.FormValue(key) != published.Format(time.RFC3339) {
					t.Errorf("%s 时间错误", key)
				}
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"x","status":"created"}`)
		case "PUT /api/albums/album/assets":
			albums.Add(1)
			var body struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) != 1 || body.IDs[0] != "x" {
				t.Error("入册资源错误")
			}
			_, _ = io.WriteString(w, `[{"id":"x","success":true}]`)
		default:
			t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}, func(r *fhttp.Request) (*fhttp.Response, error) {
		sourceCalls.Add(1)
		if r.URL.String() != sourceURL || r.Header.Get("User-Agent") != scraper.DefaultUserAgent || r.Header.Get("Referer") != "https://article.example/post" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("源站 URL 或请求头错误")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("源站不应携带认证头")
		}
		return sourceResponse(200, png, ""), nil
	})
	original := "<P class='keep'>字 &amp; 文</P>\n" + `<img src='https://PIC1.ZHIMG.COM:443/image#fragment' alt='a&amp;b'/>` + `<IMG src='https://outside.example/p'  ><!-- 保留 -->`
	for i := 0; i < 2; i++ {
		items := []route.FeedItem{{Content: original, Link: "https://article.example/post", PubDate: &published}}
		if err := a.Archive(config.ResolvedRouteConfig{Cookie: "不得透传"}, items); err != nil {
			t.Fatal(err)
		}
		want := "<P class='keep'>字 &amp; 文</P>\n" + `<img src="` + a.cfg.Immich.URL + `/api/assets/x/original?key=k" alt="a&amp;b"/>` + `<IMG src='https://outside.example/p'  ><!-- 保留 -->`
		if items[0].Content != want {
			t.Errorf("正文不符：\ngot  %s\nwant %s", items[0].Content, want)
		}
	}
	if sourceCalls.Load() != 1 || uploads.Load() != 1 || albums.Load() != 1 {
		t.Fatalf("内存命中后不应请求：源=%d 上传=%d 入册=%d", sourceCalls.Load(), uploads.Load(), albums.Load())
	}
	if a.Stats().Stored != 1 {
		t.Error("新建计数错误")
	}
}

func TestArchiveDuplicateAndDirectImmich(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	a := newTestArchiver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = io.WriteString(w, `{"id":"existing","status":"duplicate"}`)
		} else {
			_, _ = io.WriteString(w, `[{"id":"existing","error":"duplicate"}]`)
		}
	}, func(r *fhttp.Request) (*fhttp.Response, error) { return sourceResponse(200, png, ""), nil })
	items := imageItems()
	if err := a.Archive(config.ResolvedRouteConfig{}, items); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items[0].Content, "/api/assets/existing/original?key=k") || a.Stats().Deduplicated != 1 {
		t.Fatal("重复资源未使用已有 ID 或计数错误")
	}
	if a.store.client.Transport.(*http.Transport).Proxy != nil {
		t.Error("Immich 必须禁用环境代理")
	}
}

func TestArchiveSourceFailures(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		body             []byte
		location, reason string
	}{
		{"404", 404, nil, "", "status"},
		{"白名单外重定向", 302, nil, "https://outside.example/p", "redirect"},
		{"超过大小限制", 200, bytes.Repeat([]byte("x"), (1<<20)+1), "", "too_large"},
		{"拒绝SVG", 200, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "", "type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestArchiver(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("源图失败不应请求 Immich")
				w.WriteHeader(500)
			}, func(r *fhttp.Request) (*fhttp.Response, error) {
				return sourceResponse(tc.status, tc.body, tc.location), nil
			})
			items := imageItems()
			original := items[0].Content
			if err := a.Archive(config.ResolvedRouteConfig{}, items); err != nil {
				t.Fatal(err)
			}
			if items[0].Content != original || a.Stats().SourceFailures[tc.reason] != 1 {
				t.Fatal("源图失败应保留原文并计数")
			}
		})
	}
}

func TestArchiveImmich503(t *testing.T) {
	a := newTestArchiver(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, func(r *fhttp.Request) (*fhttp.Response, error) { return sourceResponse(200, png, ""), nil })
	items := imageItems()
	original := items[0].Content
	err := a.Archive(config.ResolvedRouteConfig{}, items)
	assertImmichError(t, err)
	if items[0].Content != original || a.Stats().LastStoreError == "" || a.Stats().LastStoreErrorAt == nil {
		t.Fatal("失败应保留正文并记录错误")
	}
}

func TestArchiveAlbumFailureDoesNotCache(t *testing.T) {
	var uploads atomic.Int32
	a := newTestArchiver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			uploads.Add(1)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"x"}`)
		} else {
			_, _ = io.WriteString(w, `[{"id":"x","success":false,"error":"no_permission"}]`)
		}
	}, func(r *fhttp.Request) (*fhttp.Response, error) { return sourceResponse(200, png, ""), nil })
	for i := 0; i < 2; i++ {
		items := imageItems()
		original := items[0].Content
		assertImmichError(t, a.Archive(config.ResolvedRouteConfig{}, items))
		if items[0].Content != original {
			t.Fatal("入册失败不应改写")
		}
	}
	if uploads.Load() != 2 {
		t.Fatal("入册失败不应缓存 URL")
	}
}

func TestHostMatching(t *testing.T) {
	a := New(config.MediaConfig{AllowedHosts: []string{"*.zhimg.com"}})
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://pic1.zhimg.com/a", true}, {"https://a.b.zhimg.com/a", true},
		{"https://zhimg.com/a", false}, {"https://evilzhimg.com/a", false},
		{"http://pic1.zhimg.com/a", false}, {"https://pic1.zhimg.com:444/a", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			_, got := a.normalize(tc.url)
			if got != tc.want {
				t.Errorf("匹配=%v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestArchiveFind(t *testing.T) {
	digest := sha256.Sum256([]byte(sourceURL))
	name := fmt.Sprintf("%x", digest[:8])
	for _, tc := range []struct {
		name, filename string
		status         int
		found          bool
	}{
		{"相册命中", name + ".jpg", 200, true},
		{"包含误命中", "x" + name + ".jpg", 200, false},
		{"分享鉴权失败", "", 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var searches, sources, uploads, albums atomic.Int32
			a := newTestArchiverWithSearch(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" {
					t.Error("API 地址不应含分享 key")
				}
				if r.URL.Path == "/api/search/metadata" {
					searches.Add(1)
					if r.Method != "POST" || r.Header.Get("x-immich-share-key") != "k" || r.Header.Get("x-api-key") != "" || r.Header.Get("Content-Type") != "application/json" {
						t.Error("查找方法或鉴权头错误")
					}
					body, _ := io.ReadAll(r.Body)
					if string(body) != `{"albumIds":["album"],"originalFileName":"`+name+`"}` {
						t.Errorf("查找请求体=%s", body)
					}
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"assets": map[string]any{"items": []map[string]string{{"id": "found", "originalFileName": tc.filename}}}})
					return
				}
				if r.Header.Get("x-api-key") != "secret" || r.Header.Get("x-immich-share-key") != "" {
					t.Error("上传和入册鉴权头错误")
				}
				switch r.Method + " " + r.URL.Path {
				case "POST /api/assets":
					uploads.Add(1)
					w.WriteHeader(201)
					_, _ = io.WriteString(w, `{"id":"uploaded"}`)
				case "PUT /api/albums/album/assets":
					albums.Add(1)
					_, _ = io.WriteString(w, `[{"id":"uploaded","success":true}]`)
				default:
					t.Errorf("意外 API 请求：%s", r.URL)
					w.WriteHeader(500)
				}
			}, func(r *fhttp.Request) (*fhttp.Response, error) {
				sources.Add(1)
				return sourceResponse(200, png, ""), nil
			})
			// 发布路径不处理 API；误用发布地址只会访问本地假服务并失败。
			a.store.cfg.PublicURL = a.cfg.Immich.URL + "/public"
			items := imageItems()
			original := items[0].Content
			err := a.Archive(config.ResolvedRouteConfig{}, items)
			if tc.status == 401 {
				if err == nil || !strings.Contains(err.Error(), "401") || items[0].Content != original {
					t.Fatalf("鉴权失败未保留原文：%v %s", err, items[0].Content)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				id := "uploaded"
				if tc.found {
					id = "found"
				}
				if items[0].Content != `<img src="`+a.store.cfg.PublicURL+`/api/assets/`+id+`/original?key=k">` {
					t.Fatalf("发布地址错误：%s", items[0].Content)
				}
			}
			var wantCalls int32
			if tc.status == 200 && !tc.found {
				wantCalls = 1
			}
			if searches.Load() != 1 || sources.Load() != wantCalls || uploads.Load() != wantCalls || albums.Load() != wantCalls {
				t.Fatalf("请求计数：查找=%d 源=%d 上传=%d 入册=%d", searches.Load(), sources.Load(), uploads.Load(), albums.Load())
			}
			var wantFound uint64
			if tc.found {
				wantFound = 1
			}
			if a.Stats().Found != wantFound {
				t.Fatalf("Found=%d", a.Stats().Found)
			}
		})
	}
}
