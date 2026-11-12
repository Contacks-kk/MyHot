package adminsettings

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

type memoryRepository struct {
	overview Overview
	result   map[string]any
	found    bool
	budget   BudgetInput
	actor    string
	called   bool
	contact  Contact
}

func (r *memoryRepository) Overview(context.Context) (Overview, error) { return r.overview, nil }
func (r *memoryRepository) Contact(context.Context) (Contact, error)   { return r.contact, nil }
func (r *memoryRepository) SetTargetEnabled(_ context.Context, key string, enabled bool, reason, actor string) (map[string]any, bool, error) {
	r.called, r.actor = true, actor
	return map[string]any{"key": key, "enabled": enabled, "reason": reason}, r.found, nil
}
func (r *memoryRepository) UpdateBudget(_ context.Context, service string, input BudgetInput, actor string) (map[string]any, error) {
	r.called, r.budget, r.actor = true, input, actor
	return map[string]any{"service": service}, nil
}
func (r *memoryRepository) ReplaceContactQR(_ context.Context, slot, imageURL, actor string) (Contact, error) {
	r.called, r.actor = true, actor
	if slot == "wechatQr" {
		r.contact.WechatQR = &imageURL
	} else {
		r.contact.FeishuQR = &imageURL
	}
	return r.contact, nil
}

func TestSetTargetRequiresReasonAndAddsLocalPushState(t *testing.T) {
	repo := &memoryRepository{found: true}
	service := NewService(repo, Config{FeishuContentPushEnabled: true})
	if _, _, err := service.SetTargetEnabled(context.Background(), "weekly", true, "  ", "admin:1"); err == nil || repo.called {
		t.Fatalf("blank reason reached repository: called=%v err=%v", repo.called, err)
	}
	result, found, err := service.SetTargetEnabled(context.Background(), "weekly", true, "approved", "admin:1")
	if err != nil || !found || result["pushEnabledHere"] != true || repo.actor != "admin:1" {
		t.Fatalf("unexpected target result: %#v found=%v actor=%q err=%v", result, found, repo.actor, err)
	}
}

func TestUpdateBudgetValidatesNonNegativeIntegersBeforeRepository(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo, Config{})
	for _, input := range []BudgetInput{
		{PerMinute: -1, PerHour: 1, PerDay: 1, Reason: "reason"},
		{PerMinute: 1, PerHour: -1, PerDay: 1, Reason: "reason"},
		{PerMinute: 1, PerHour: 1, PerDay: -1, Reason: "reason"},
		{PerMinute: 1, PerHour: 1, PerDay: 1, Reason: "  "},
	} {
		if _, err := service.UpdateBudget(context.Background(), "openai", input, "admin:1"); err == nil || repo.called {
			t.Fatalf("invalid budget reached repository: %#v called=%v err=%v", input, repo.called, err)
		}
	}
	input := BudgetInput{PerMinute: 0, PerHour: 20, PerDay: 50, Reason: "stop or limit"}
	if _, err := service.UpdateBudget(context.Background(), "openai", input, "admin:1"); err != nil || !repo.called || repo.budget != input {
		t.Fatalf("valid budget was not passed through: got=%#v called=%v err=%v", repo.budget, repo.called, err)
	}
}

type failingRepository struct{ memoryRepository }

func (*failingRepository) Overview(context.Context) (Overview, error) {
	return Overview{}, errors.New("database unavailable")
}

func (*failingRepository) Contact(context.Context) (Contact, error) {
	return Contact{}, errors.New("database unavailable")
}

func TestOverviewReturnsRepositoryFailure(t *testing.T) {
	_, err := NewService(&failingRepository{}, Config{}).Overview(context.Background())
	if err == nil {
		t.Fatal("expected repository error")
	}
}

func TestReplaceContactQRValidatesImagePersistsAndAuditsThroughRepository(t *testing.T) {
	var data bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 120, 120))
	imageData.Set(10, 10, color.Black)
	if err := png.Encode(&data, imageData); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	repo := &memoryRepository{}
	service := NewService(repo, Config{DataDir: directory})
	contact, err := service.ReplaceContactQR(context.Background(), "wechatQr", data.Bytes(), "admin:42")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "/contact/qr-wechat-"
	if contact.WechatQR == nil || len(*contact.WechatQR) != len(wantURL)+8+len(".png") || (*contact.WechatQR)[:len(wantURL)] != wantURL || (*contact.WechatQR)[len(*contact.WechatQR)-4:] != ".png" {
		t.Fatalf("contact URL = %#v", contact.WechatQR)
	}
	stored, err := os.ReadFile(filepath.Join(directory, "uploads", filepath.Base(*contact.WechatQR)))
	if err != nil || !bytes.Equal(stored, data.Bytes()) {
		t.Fatalf("stored image did not match input: err=%v", err)
	}
	if !repo.called || repo.actor != "admin:42" {
		t.Fatalf("contact change did not pass through repository audit: %#v", repo)
	}
}

func TestReplaceContactQRRejectsInvalidSlotSmallOrUnsupportedImages(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo, Config{DataDir: t.TempDir()})
	for _, test := range []struct {
		slot string
		data []byte
	}{
		{slot: "unknown", data: []byte("image")},
		{slot: "wechatQr", data: []byte("image")},
	} {
		if _, err := service.ReplaceContactQR(context.Background(), test.slot, test.data, "admin:1"); err == nil {
			t.Fatalf("accepted invalid contact image: slot=%s", test.slot)
		}
	}
	var small bytes.Buffer
	if err := png.Encode(&small, image.NewRGBA(image.Rect(0, 0, 119, 120))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceContactQR(context.Background(), "wechatQr", small.Bytes(), "admin:1"); err == nil || repo.called {
		t.Fatalf("small image reached repository: called=%v err=%v", repo.called, err)
	}
}
