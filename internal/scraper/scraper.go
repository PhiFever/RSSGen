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
	"chrome_131":     profiles.Chrome_131,
	"chrome_131_psk": profiles.Chrome_131_PSK,
	"chrome_130_psk": profiles.Chrome_130_PSK,
	"chrome_124":     profiles.Chrome_124,
	"chrome_120":     profiles.Chrome_120,
	"chrome_117":     profiles.Chrome_117,
	"chrome_116_psk": profiles.Chrome_116_PSK,
	"chrome_116_psk_pq": profiles.Chrome_116_PSK_PQ,
	"chrome_112":     profiles.Chrome_112,
	"chrome_111":     profiles.Chrome_111,
	"chrome_110":     profiles.Chrome_110,
	"chrome_109":     profiles.Chrome_109,
	"chrome_108":     profiles.Chrome_108,
	"chrome_107":     profiles.Chrome_107,
	"chrome_106":     profiles.Chrome_106,
	"chrome_105":     profiles.Chrome_105,
	"chrome_104":     profiles.Chrome_104,
	"chrome_103":     profiles.Chrome_103,
	"brave_146":      profiles.Brave_146,
	"brave_146_psk":  profiles.Brave_146_PSK,
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

	// 合并 headers
	headers := make(map[string]string)
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
