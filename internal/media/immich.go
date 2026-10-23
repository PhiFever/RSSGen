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

func (s *immichStore) request(method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, s.cfg.URL+path, body)
	if err != nil {
		return nil, fmt.Errorf("创建 Immich 请求失败")
	}
	req.Header.Set("x-api-key", s.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)
	resp, err := s.client.Do(req)
	// 请求地址不含凭据（API key 在请求头），底层错误可以原样带上。
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
	resp, err := s.request("POST", "/api/assets", writer.FormDataContentType(), &body)
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
	resp, err := s.request("PUT", "/api/albums/"+url.PathEscape(s.cfg.AlbumID)+"/assets", "application/json", bytes.NewReader(body))
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
