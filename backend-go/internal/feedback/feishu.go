package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const feishuAPI = "https://open.feishu.cn/open-apis"

var ErrImageRefused = errors.New("feishu refused the feedback image")

type FeishuForwarder struct {
	AppID     string
	AppSecret string
	BaseURL   string
	Client    *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

func NewFeishuForwarder(appID, appSecret string) *FeishuForwarder {
	return &FeishuForwarder{AppID: appID, AppSecret: appSecret, BaseURL: feishuAPI, Client: &http.Client{}}
}

func (f *FeishuForwarder) UploadImage(ctx context.Context, data []byte, filename string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("image_type", "message"); err != nil {
		return "", err
	}
	file, err := form.CreateFormFile("image", filename)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}
	token, err := f.tenantToken(ctx)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.baseURL(), "/")+"/im/v1/images", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.client().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImageKey string `json:"image_key"`
		} `json:"data"`
	}
	if err := decodeFeishuResponse(response, &result); err != nil {
		return "", err
	}
	if result.Code == 234001 || result.Code == 234006 || result.Code == 234010 || result.Code == 234011 {
		return "", fmt.Errorf("%w: %s", ErrImageRefused, result.Msg)
	}
	if result.Code != 0 || result.Data.ImageKey == "" {
		return "", fmt.Errorf("feishu upload: %s", result.Msg)
	}
	return result.Data.ImageKey, nil
}

func (f *FeishuForwarder) SendFeedback(ctx context.Context, chat string, id int64, content, pageURL, email, note string, imageKey, imageNote *string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := f.tenantToken(ctx)
	if err != nil {
		return err
	}
	paragraphs := [][]map[string]string{
		{{"tag": "text", "text": content}},
		{{"tag": "text", "text": "页面：" + emptyDash(pageURL)}},
		{{"tag": "text", "text": "邮箱：" + emptyEmail(email)}},
	}
	if note != "" {
		paragraphs = append(paragraphs, []map[string]string{{"tag": "text", "text": note}})
	}
	if imageKey != nil && *imageKey != "" {
		paragraphs = append(paragraphs, []map[string]string{{"tag": "img", "image_key": *imageKey}})
	} else if imageNote != nil && *imageNote != "" {
		paragraphs = append(paragraphs, []map[string]string{{"tag": "text", "text": *imageNote}})
	}
	contentJSON, err := json.Marshal(map[string]any{"zh_cn": map[string]any{"title": fmt.Sprintf("反馈 #%d", id), "content": paragraphs}})
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"receive_id": chat, "msg_type": "post", "content": string(contentJSON)})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(f.baseURL(), "/") + "/im/v1/messages?" + url.Values{"receive_id_type": []string{"chat_id"}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := decodeFeishuResponse(response, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("feishu send: %s", result.Msg)
	}
	return nil
}

func (f *FeishuForwarder) SendText(ctx context.Context, chat, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := f.tenantToken(ctx)
	if err != nil {
		return err
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"receive_id": chat, "msg_type": "text", "content": string(content)})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(f.baseURL(), "/") + "/im/v1/messages?" + url.Values{"receive_id_type": []string{"chat_id"}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := decodeFeishuResponse(response, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("feishu send: %s", result.Msg)
	}
	return nil
}

func (f *FeishuForwarder) tenantToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && f.tokenExpiry.After(time.Now().Add(time.Minute)) {
		return f.token, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]string{"app_id": f.AppID, "app_secret": f.AppSecret})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.baseURL(), "/")+"/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := f.client().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := decodeFeishuResponse(response, &result); err != nil {
		return "", err
	}
	if result.Code != 0 || result.Token == "" {
		return "", fmt.Errorf("feishu token: %s", result.Msg)
	}
	expire := result.Expire
	if expire <= 0 {
		expire = 3600
	}
	f.token = result.Token
	f.tokenExpiry = time.Now().Add(time.Duration(expire) * time.Second)
	return f.token, nil
}

func decodeFeishuResponse(response *http.Response, result any) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode feishu response: %w", err)
	}
	return nil
}

func (f *FeishuForwarder) baseURL() string {
	if strings.TrimSpace(f.BaseURL) != "" {
		return f.BaseURL
	}
	return feishuAPI
}

func (f *FeishuForwarder) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func emptyEmail(value string) string {
	if value == "" {
		return "（未留）"
	}
	return value
}
