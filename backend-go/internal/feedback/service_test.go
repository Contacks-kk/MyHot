package feedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type memoryRepository struct {
	result     ListResult
	screenshot *string
	updated    bool
	erased     bool
	banned     bool
	banReason  string
	lastQuery  string
	lastPage   int
	updateErr  error
}

func (r *memoryRepository) List(_ context.Context, _ string, query string, page int) (ListResult, error) {
	r.lastQuery, r.lastPage = query, page
	return r.result, nil
}
func (r *memoryRepository) ScreenshotKey(context.Context, int64) (*string, error) {
	return r.screenshot, nil
}
func (r *memoryRepository) Update(context.Context, int64, Patch, string) (map[string]any, bool, error) {
	r.updated = true
	return map[string]any{"updated": true}, true, r.updateErr
}
func (r *memoryRepository) Erase(context.Context, int64, string, string) (bool, error) {
	r.erased = true
	return true, nil
}
func (r *memoryRepository) Ban(_ context.Context, _, reason, _ string) error {
	r.banned, r.banReason = true, reason
	return nil
}
func (r *memoryRepository) Unban(context.Context, string, string) error { return nil }

func TestListNormalizesPageSearchAndEmptyCollections(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo, Config{})
	result, err := service.List(context.Background(), "new", "  urgent  ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Page != 1 || result.Rows == nil || result.Bans == nil || result.Counts == nil || repo.lastPage != 1 || repo.lastQuery != "urgent" {
		t.Fatalf("list contract was not normalized: result=%#v repo=%#v", result, repo)
	}
}

func TestUpdateRejectsUnknownStatusAndPassesConflictsThrough(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo, Config{})
	unknown := "closed"
	if _, _, err := service.Update(context.Background(), 1, Patch{Status: &unknown}, "admin:1"); err == nil || repo.updated {
		t.Fatal("unknown feedback status reached persistence")
	}
	repo.updateErr = ErrConflict
	if _, _, err := service.Update(context.Background(), 1, Patch{Version: "stale"}, "admin:1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("optimistic conflict was lost: %v", err)
	}
}

func TestScreenshotOnlyReadsLocalSafeNamesAndEraseRemovesFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "feedback-screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "shot-1.png")
	if err := os.WriteFile(file, []byte("fixture image"), 0o600); err != nil {
		t.Fatal(err)
	}
	localKey := "local:shot-1.png"
	repo := &memoryRepository{screenshot: &localKey}
	service := NewService(repo, Config{DataDir: root})
	screenshot, err := service.Screenshot(context.Background(), 1)
	if err != nil || screenshot == nil || screenshot.ContentType != "image/png" || string(screenshot.Bytes) != "fixture image" {
		t.Fatalf("local screenshot read failed: %#v err=%v", screenshot, err)
	}
	if _, err := service.Erase(context.Background(), 1, "privacy request", "admin:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) || !repo.erased {
		t.Fatal("erase must remove the local attachment and anonymize the record")
	}

	repo.screenshot = stringRef("local:../outside.png")
	if value, err := service.Screenshot(context.Background(), 1); err != nil || value != nil {
		t.Fatalf("unsafe local key was served: %#v err=%v", value, err)
	}
	repo.screenshot = stringRef("feishu:remote-image")
	if value, err := service.Screenshot(context.Background(), 1); err != nil || value != nil {
		t.Fatalf("remote screenshot was served as a local file: %#v err=%v", value, err)
	}
}

func TestBanRequiresReasonWithoutWriting(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo, Config{})
	if err := service.Ban(context.Background(), "source", "  ", "admin:1"); err == nil || repo.banned {
		t.Fatal("empty reason should not create a source ban")
	}
	if err := service.Ban(context.Background(), "source", "policy", "admin:1"); err != nil || !repo.banned || repo.banReason != "policy" {
		t.Fatalf("valid ban failed: banned=%t reason=%q err=%v", repo.banned, repo.banReason, err)
	}
}

func stringRef(value string) *string { return &value }
