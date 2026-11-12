package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFeishuForwarderUploadsImageAndSendsFeedbackPost(t *testing.T) {
	var tokenCalls, uploadCalls, messageCalls int
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v3/tenant_access_token/internal":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "fixture-token", "expire": 3600})
		case "/im/v1/images":
			uploadCalls++
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Errorf("upload authorization changed: %q", r.Header.Get("Authorization"))
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse image upload: %v", err)
			}
			if r.FormValue("image_type") != "message" {
				t.Errorf("image type changed: %q", r.FormValue("image_type"))
			}
			file, _, err := r.FormFile("image")
			if err != nil {
				t.Errorf("image form field missing: %v", err)
			} else {
				uploaded, _ = io.ReadAll(file)
				_ = file.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"image_key": "image-fixture"}})
		case "/im/v1/messages":
			messageCalls++
			if r.URL.Query().Get("receive_id_type") != "chat_id" || r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Errorf("message request envelope changed: url=%s auth=%q", r.URL, r.Header.Get("Authorization"))
			}
			var message struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
				t.Errorf("decode message: %v", err)
			}
			if message.ReceiveID != "chat-fixture" || message.MsgType != "post" {
				t.Errorf("message target/type changed: %#v", message)
			}
			var content struct {
				Chinese struct {
					Title string                `json:"title"`
					Rows  [][]map[string]string `json:"content"`
				} `json:"zh_cn"`
			}
			if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
				t.Errorf("decode post content: %v", err)
			}
			if content.Chinese.Title != "反馈 #42" || len(content.Chinese.Rows) != 4 || content.Chinese.Rows[0][0]["text"] != "内容" || content.Chinese.Rows[3][0]["image_key"] != "image-fixture" {
				t.Errorf("feedback post fields changed: %#v", content.Chinese)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "msg-fixture"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	forwarder := NewFeishuForwarder("fixture-app", "fixture-secret")
	forwarder.BaseURL = server.URL
	forwarder.Client = server.Client()
	image := []byte("fixture-image")
	key, err := forwarder.UploadImage(context.Background(), image, "fixture.png")
	if err != nil || key != "image-fixture" || string(uploaded) != string(image) {
		t.Fatalf("image upload failed: key=%q data=%q err=%v", key, uploaded, err)
	}
	imageNote := (*string)(nil)
	if err := forwarder.SendFeedback(context.Background(), "chat-fixture", 42, "内容", "https://example.invalid", "a@example.invalid", "", &key, imageNote); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || uploadCalls != 1 || messageCalls != 1 {
		t.Fatalf("Feishu calls token=%d upload=%d message=%d", tokenCalls, uploadCalls, messageCalls)
	}
}

func TestFeishuImageRefusalIsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v3/tenant_access_token/internal" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "fixture-token", "expire": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 234010, "msg": "invalid image"})
	}))
	defer server.Close()
	forwarder := NewFeishuForwarder("fixture-app", "fixture-secret")
	forwarder.BaseURL, forwarder.Client = server.URL, server.Client()
	_, err := forwarder.UploadImage(context.Background(), []byte("bad"), "fixture.png")
	if !errors.Is(err, ErrImageRefused) {
		t.Fatalf("permanent image refusal was not classified: %v", err)
	}
}

func TestFeishuForwarderSendsTextToChat(t *testing.T) {
	var tokenCalls, messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v3/tenant_access_token/internal":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "fixture-token", "expire": 3600})
		case "/im/v1/messages":
			messageCalls++
			if r.URL.Query().Get("receive_id_type") != "chat_id" || r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Errorf("message request envelope changed: url=%s auth=%q", r.URL, r.Header.Get("Authorization"))
			}
			var message struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
				t.Errorf("decode message: %v", err)
			}
			var content struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
				t.Errorf("decode text content: %v", err)
			}
			if message.ReceiveID != "alert-chat" || message.MsgType != "text" || content.Text != "weekly status" {
				t.Errorf("text message fields changed: %#v, %#v", message, content)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	forwarder := NewFeishuForwarder("fixture-app", "fixture-secret")
	forwarder.BaseURL, forwarder.Client = server.URL, server.Client()
	if err := forwarder.SendText(context.Background(), "alert-chat", "weekly status"); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || messageCalls != 1 {
		t.Fatalf("Feishu calls token=%d message=%d", tokenCalls, messageCalls)
	}
}
