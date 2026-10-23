package media

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PhiFever/RSSGen/internal/config"
)

// errRejected 表示存储拒收了文件本身，属于源侧失败，只影响这一张图。
var errRejected = errors.New("存储拒收该文件")

// immichStore 封装 Immich 的上传、入册与访问地址格式；Archiver 只通过它访问 Immich。
type immichStore struct {
	cfg    config.ImmichConfig
	client *http.Client
}

func newImmichStore(cfg config.ImmichConfig) *immichStore {
	return &immichStore{
		cfg: cfg,
		// 显式不走环境代理：容器继承的 HTTP_PROXY 会把内网请求发给宿主机代理。
		client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second},
	}
}

// request 发起 Immich 请求；shared 为 true 时只用分享 key 鉴权，否则只用 API key。
func (s *immichStore) request(method, path, contentType string, body io.Reader, shared bool) (*http.Response, error) {
	apiURL := s.cfg.APIURL
	if apiURL == "" {
		apiURL = s.cfg.URL
	}
	req, err := http.NewRequest(method, apiURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("创建 Immich 请求失败")
	}
	if shared {
		req.Header.Set("x-immich-share-key", s.cfg.ShareKey)
	} else {
		req.Header.Set("x-api-key", s.cfg.APIKey)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := s.client.Do(req)
	// 请求地址不含凭据（API key 和分享 key 都在请求头），底层错误可以原样带上。
	if err != nil {
		return nil, fmt.Errorf("Immich 请求失败: %w", err)
	}
	return resp, nil
}

// put 上传图片并返回资源 ID；duplicate 表示 Immich 按内容判定为已有资源。
// Immich 按 filename 的扩展名判断文件类型，调用方须按魔数结果给出扩展名。
func (s *immichStore) put(data []byte, filename string, published time.Time) (id string, duplicate bool, err error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("assetData", filename)
	if err == nil {
		_, err = part.Write(data)
	}
	if err != nil {
		return "", false, fmt.Errorf("构建 Immich 图片上传失败")
	}
	for _, field := range []struct{ name, value string }{
		{"filename", filename}, {"visibility", "archive"},
		{"fileCreatedAt", published.Format(time.RFC3339)}, {"fileModifiedAt", published.Format(time.RFC3339)},
	} {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return "", false, fmt.Errorf("构建 Immich 上传字段失败")
		}
	}
	if err := writer.Close(); err != nil {
		return "", false, fmt.Errorf("结束 Immich 上传表单失败")
	}
	resp, err := s.request("POST", "/api/assets", writer.FormDataContentType(), &body, false)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		return "", false, errRejected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("Immich 上传失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.ID == "" {
		return "", false, fmt.Errorf("Immich 上传响应无有效资源 ID")
	}
	return result.ID, resp.StatusCode == http.StatusOK && result.Status == "duplicate", nil
}

// commit 把资源加入分享相册；加入之前分享链接访问不到资源，所以必须在发布地址前完成。
func (s *immichStore) commit(ids []string) error {
	expected := make(map[string]bool)
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := expected[id]; !ok {
			expected[id] = false
			unique = append(unique, id)
		}
	}
	body, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{unique})
	if err != nil {
		return fmt.Errorf("构建 Immich 入册请求失败")
	}
	resp, err := s.request("PUT", "/api/albums/"+url.PathEscape(s.cfg.AlbumID)+"/assets", "application/json", bytes.NewReader(body), false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Immich 加入相册失败：HTTP %d", resp.StatusCode)
	}
	var results []struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return fmt.Errorf("Immich 入册响应解析失败")
	}
	for _, result := range results {
		if !result.Success && result.Error != "duplicate" {
			return fmt.Errorf("Immich 加入相册失败：资源未获准入册")
		}
		if _, ok := expected[result.ID]; ok {
			expected[result.ID] = true
		}
	}
	for _, success := range expected {
		if !success {
			return fmt.Errorf("Immich 入册响应缺少资源确认")
		}
	}
	return nil
}

// url 返回经分享链接访问资源原图的地址。
func (s *immichStore) url(id string) string {
	return s.cfg.URL + "/api/assets/" + url.PathEscape(id) + "/original?key=" + url.QueryEscape(s.cfg.ShareKey)
}

// find 按文件主名查找分享相册中的资源，并排除包含匹配的误命中。
func (s *immichStore) find(name string) (string, error) {
	body, err := json.Marshal(struct {
		AlbumIDs         []string `json:"albumIds"`
		OriginalFileName string   `json:"originalFileName"`
	}{[]string{s.cfg.AlbumID}, name})
	if err != nil {
		return "", fmt.Errorf("构建 Immich 查找请求失败")
	}
	resp, err := s.request("POST", "/api/search/metadata", "application/json", bytes.NewReader(body), true)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Immich 查找失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Assets struct {
			Items []struct {
				ID               string `json:"id"`
				OriginalFileName string `json:"originalFileName"`
			} `json:"items"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("Immich 查找响应解析失败")
	}
	for _, item := range result.Assets.Items {
		base, _, _ := strings.Cut(item.OriginalFileName, ".")
		if base == name {
			return item.ID, nil
		}
	}
	return "", nil
}
