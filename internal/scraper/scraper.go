// Package scraper 提供反爬 HTTP 客户端封装，自动模拟浏览器 TLS 指纹。
package scraper

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bogdanfinn/fhttp"
	httpclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// chromeDefaultHeaders 是 Chrome 131 (Windows) 的默认请求头集合。
//
// tls-client 只伪装 TLS/JA3 与 HTTP2 指纹，不会自动补全浏览器头——不设头时
// 它默认只发 `user-agent: Go-http-client/2.0`，会被风控直接识破。这里手动补齐
// curl_cffi `impersonate` 自动注入的那套头，作为所有请求的打底值（可被覆盖）。
var chromeDefaultHeaders = map[string]string{
	"sec-ch-ua":          `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
	"sec-ch-ua-mobile":   "?0",
	"sec-ch-ua-platform": `"Windows"`,
	"user-agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"accept":             "*/*",
	"accept-language":    "zh-CN,zh;q=0.9,en;q=0.8",
	"accept-encoding":    "gzip, deflate, br, zstd",
	"sec-fetch-site":     "same-origin",
	"sec-fetch-mode":     "cors",
	"sec-fetch-dest":     "empty",
}

// chromeHeaderOrder 是 Chrome 131 的请求头顺序（必须小写，fhttp 按 ToLower 匹配）。
//
// 这是一个覆盖导航/XHR 两种场景的超集：实际请求中不存在的头会被忽略，存在但
// 不在列表中的头会被排到有序头之后。tls-client 不管 header order，需显式指定。
var chromeHeaderOrder = []string{
	"host",
	"connection",
	"pragma",
	"cache-control",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"upgrade-insecure-requests",
	"user-agent",
	"accept",
	"x-requested-with",
	"x-zse-93",
	"x-zse-96",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-user",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"cookie",
}

// Config 是 Scraper 的配置参数。
type Config struct {
	Cookies      map[string]string
	Proxy        string
	RateLimit    float64 // 最小请求间隔（秒）
	Impersonate  string  // TLS 指纹配置名，如 "chrome_131"
	ExtraHeaders map[string]string
}

// Response 封装 HTTP 响应，提供便捷方法。
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Text 返回响应体的字符串形式。
func (r *Response) Text() string {
	return string(r.Body)
}

// Scraper 是基于 tls-client 的反爬 HTTP 客户端。
type Scraper struct {
	cookies      map[string]string
	proxy        string
	rateLimit    time.Duration
	impersonate  string
	extraHeaders map[string]string

	mu              sync.Mutex
	lastRequestTime time.Time
	client          httpclient.HttpClient
}

// impersonateProfile 将配置名映射到 tls-client 的 Chrome 指纹 profile。
var impersonateProfile = map[string]profiles.ClientProfile{
	"chrome_131":        profiles.Chrome_131,
	"chrome_131_psk":    profiles.Chrome_131_PSK,
	"chrome_130_psk":    profiles.Chrome_130_PSK,
	"chrome_124":        profiles.Chrome_124,
	"chrome_120":        profiles.Chrome_120,
	"chrome_117":        profiles.Chrome_117,
	"chrome_116_psk":    profiles.Chrome_116_PSK,
	"chrome_116_psk_pq": profiles.Chrome_116_PSK_PQ,
	"chrome_112":        profiles.Chrome_112,
	"chrome_111":        profiles.Chrome_111,
	"chrome_110":        profiles.Chrome_110,
	"chrome_109":        profiles.Chrome_109,
	"chrome_108":        profiles.Chrome_108,
	"chrome_107":        profiles.Chrome_107,
	"chrome_106":        profiles.Chrome_106,
	"chrome_105":        profiles.Chrome_105,
	"chrome_104":        profiles.Chrome_104,
	"chrome_103":        profiles.Chrome_103,
	"brave_146":         profiles.Brave_146,
	"brave_146_psk":     profiles.Brave_146_PSK,
}

// New 创建一个新的 Scraper 实例。
func New(cfg Config) *Scraper {
	imp := cfg.Impersonate
	if imp == "" {
		imp = "chrome_131"
	}

	rateLimit := cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = 1.0
	}

	profile, ok := impersonateProfile[imp]
	if !ok {
		profile = profiles.Chrome_131
	}

	// 创建 TLS 客户端
	logger := httpclient.NewNoopLogger()
	client, _ := httpclient.NewHttpClient(
		logger,
		httpclient.WithTimeoutSeconds(30),
		httpclient.WithClientProfile(profile),
		httpclient.WithNotFollowRedirects(),
	)

	// 设置代理
	if cfg.Proxy != "" {
		client.SetProxy(cfg.Proxy)
	}

	s := &Scraper{
		cookies:      cfg.Cookies,
		proxy:        cfg.Proxy,
		rateLimit:    time.Duration(rateLimit * float64(time.Second)),
		impersonate:  imp,
		extraHeaders: cfg.ExtraHeaders,
		client:       client,
	}

	return s
}

// rateLimitWait 等待直到满足最小请求间隔。
func (s *Scraper) rateLimitWait() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(s.lastRequestTime)
	if elapsed < s.rateLimit {
		time.Sleep(s.rateLimit - elapsed)
	}
	s.lastRequestTime = time.Now()
}

// doRequest 是内部请求方法，统一处理限速、header 合并。
func (s *Scraper) doRequest(method, url string, referer string, body io.Reader, extraHeaders map[string]string) (*Response, error) {
	s.rateLimitWait()

	// 合并 headers：Chrome 默认头打底 → 实例 ExtraHeaders → referer → 单次请求头
	headers := make(map[string]string, len(chromeDefaultHeaders)+len(s.extraHeaders)+len(extraHeaders)+1)
	for k, v := range chromeDefaultHeaders {
		headers[k] = v
	}
	for k, v := range s.extraHeaders {
		headers[k] = v
	}
	if referer != "" {
		headers["referer"] = referer
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}

	// 构建 fhttp 请求
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 设置 cookies
	if len(s.cookies) > 0 {
		cookieParts := make([]string, 0, len(s.cookies))
		for name, value := range s.cookies {
			cookieParts = append(cookieParts, name+"="+value)
		}
		req.Header.Set("Cookie", strings.Join(cookieParts, "; "))
	}

	// 指定 Chrome 头顺序：tls-client 只伪装 TLS，header order 需手动设置
	req.Header[http.HeaderOrderKey] = chromeHeaderOrder

	// 使用 tls-client 发送请求
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s 请求失败 %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       bodyBytes,
	}, nil
}

// Get 发送 GET 请求。headers 可以为 nil。
func (s *Scraper) Get(url string, referer string, headers map[string]string) (*Response, error) {
	return s.doRequest("GET", url, referer, nil, headers)
}

// Post 发送 POST 请求。headers 可以为 nil。
func (s *Scraper) Post(url string, referer string, body io.Reader, headers map[string]string) (*Response, error) {
	return s.doRequest("POST", url, referer, body, headers)
}

// PostJSON 发送 JSON POST 请求。
func (s *Scraper) PostJSON(url string, referer string, jsonBody string, headers map[string]string) (*Response, error) {
	h := make(map[string]string)
	h["Content-Type"] = "application/json"
	for k, v := range headers {
		h[k] = v
	}
	return s.doRequest("POST", url, referer, strings.NewReader(jsonBody), h)
}

// SetCookies 更新 cookie（运行时动态设置）。
func (s *Scraper) SetCookies(cookies map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookies = cookies
}
