package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound = errors.New("feedback not found")
	ErrConflict = errors.New("这条反馈已被修改，请刷新后再操作")
)

type Repository interface {
	List(context.Context, string, string, int) (ListResult, error)
	ScreenshotKey(context.Context, int64) (*string, error)
	Update(context.Context, int64, Patch, string) (map[string]any, bool, error)
	Erase(context.Context, int64, string, string) (bool, error)
	Ban(context.Context, string, string, string) error
	Unban(context.Context, string, string) error
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type Submission struct {
	Content       string
	Email         *string
	PageURL       *string
	ScreenshotKey *string
	SourceHash    string
}

type ForwardRecord struct {
	ID            int64
	Content       string
	Email         *string
	PageURL       *string
	Note          *string
	ScreenshotKey *string
	CreatedAt     time.Time
}

type SubmissionRepository interface {
	Banned(context.Context, []string) (bool, error)
	Create(context.Context, Submission) (int64, error)
	ForwardRecord(context.Context, int64) (*ForwardRecord, error)
	PendingForwardIDs(context.Context, time.Time, time.Time, int) ([]int64, error)
	SetScreenshotKey(context.Context, int64, string) error
	SetForwardError(context.Context, int64, string) error
	MarkForwarded(context.Context, int64, time.Time) error
}

func (r *GormRepository) Banned(ctx context.Context, hashes []string) (bool, error) {
	if len(hashes) == 0 {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&model.FeedbackBan{}).Where("source_hash IN ?", hashes).Count(&count).Error
	return count > 0, err
}

func (r *GormRepository) Create(ctx context.Context, input Submission) (int64, error) {
	row := model.Feedback{Content: input.Content, Email: input.Email, PageURL: input.PageURL, ScreenshotKey: input.ScreenshotKey, SourceHash: input.SourceHash, Status: "new", ForwardError: stringPtr("pending")}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (r *GormRepository) ForwardRecord(ctx context.Context, id int64) (*ForwardRecord, error) {
	var row ForwardRecord
	result := r.db.WithContext(ctx).Model(&model.Feedback{}).
		Select("id", "content", "email", "page_url", "note", "screenshot_key", "created_at").
		Where("id = ? AND forwarded_at IS NULL", id).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, result.Error
}

func (r *GormRepository) PendingForwardIDs(ctx context.Context, olderThan, newerThan time.Time, limit int) ([]int64, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var ids []int64
	err := r.db.WithContext(ctx).Model(&model.Feedback{}).Where(
		"forwarded_at IS NULL AND forward_error IS NOT NULL AND created_at < ? AND created_at > ?", olderThan, newerThan,
	).Order("id").Limit(limit).Pluck("id", &ids).Error
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

func (r *GormRepository) SetScreenshotKey(ctx context.Context, id int64, key string) error {
	return r.db.WithContext(ctx).Model(&model.Feedback{}).Where("id = ? AND forwarded_at IS NULL", id).Update("screenshot_key", key).Error
}

func (r *GormRepository) SetForwardError(ctx context.Context, id int64, message string) error {
	return r.db.WithContext(ctx).Model(&model.Feedback{}).Where("id = ? AND forwarded_at IS NULL", id).Update("forward_error", message).Error
}

func (r *GormRepository) MarkForwarded(ctx context.Context, id int64, at time.Time) error {
	return r.db.WithContext(ctx).Model(&model.Feedback{}).Where("id = ? AND forwarded_at IS NULL", id).Updates(map[string]any{"forwarded_at": at, "forward_error": nil}).Error
}

func (r *GormRepository) List(ctx context.Context, status, query string, page int) (ListResult, error) {
	if page < 1 {
		page = 1
	}
	var rows []FeedbackRow
	db := r.db.WithContext(ctx).Table("feedback AS fb").Select(`
		fb.id, fb.content, fb.email, fb.page_url, split_part(fb.screenshot_key, ':', 1) AS screenshot,
		fb.source_hash, fb.status, fb.note, fb.forwarded_at, fb.forward_error, fb.created_at, fb.updated_at,
		EXISTS (SELECT 1 FROM feedback_bans b WHERE b.source_hash = fb.source_hash) AS banned,
		(SELECT count(*)::int FROM feedback o WHERE o.source_hash = fb.source_hash) AS from_source`).
		Where("(? = '' OR fb.status = ?)", status, status)
	if query != "" {
		like := "%" + query + "%"
		db = db.Where("fb.content ILIKE ? OR fb.email ILIKE ? OR fb.page_url ILIKE ?", like, like, like)
	}
	if err := db.Order("fb.created_at DESC").Limit(50).Offset((page - 1) * 50).Scan(&rows).Error; err != nil {
		return ListResult{}, err
	}
	var statusCounts []struct {
		Status string
		Count  int
	}
	if err := r.db.WithContext(ctx).Model(&model.Feedback{}).Select("status, count(*)::int AS count").Group("status").Scan(&statusCounts).Error; err != nil {
		return ListResult{}, err
	}
	var bans []BanRow
	if err := r.db.WithContext(ctx).Model(&model.FeedbackBan{}).Select("source_hash, reason, created_by, created_at").Order("created_at DESC").Limit(100).Find(&bans).Error; err != nil {
		return ListResult{}, err
	}
	counts := make(map[string]int, len(statusCounts))
	for _, count := range statusCounts {
		counts[count.Status] = count.Count
	}
	return ListResult{Page: page, Rows: rows, Counts: counts, Bans: bans}, nil
}

func (r *GormRepository) ScreenshotKey(ctx context.Context, id int64) (*string, error) {
	var row model.Feedback
	result := r.db.WithContext(ctx).Select("screenshot_key").First(&row, id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return row.ScreenshotKey, result.Error
}

func (r *GormRepository) Update(ctx context.Context, id int64, patch Patch, actor string) (map[string]any, bool, error) {
	var response map[string]any
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.Feedback
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&before, id)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if result.Error != nil {
			return result.Error
		}
		version := before.UpdatedAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
		if version != patch.Version {
			return ErrConflict
		}
		updates := map[string]any{"updated_at": gorm.Expr("now()")}
		if patch.Status != nil {
			updates["status"] = *patch.Status
		}
		if patch.NoteSet {
			updates["note"] = patch.Note
		}
		if err := tx.Model(&model.Feedback{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		var after model.Feedback
		if err := tx.Select("id", "status", "note", "updated_at").First(&after, id).Error; err != nil {
			return err
		}
		beforeJSON, _ := json.Marshal(map[string]any{"status": before.Status, "note": before.Note})
		afterJSON, _ := json.Marshal(map[string]any{"status": after.Status, "note": after.Note})
		if err := addAudit(tx, actor, "feedback.update", "feedback:"+formatID(id), nil, beforeJSON, afterJSON); err != nil {
			return err
		}
		response = map[string]any{"id": after.ID, "status": after.Status, "note": after.Note, "updated_at": after.UpdatedAt}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	return response, response != nil, err
}

func (r *GormRepository) Erase(ctx context.Context, id int64, reason, actor string) (bool, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.Feedback
		result := tx.Select("id").First(&row, id)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Model(&model.Feedback{}).Where("id = ?", id).Updates(map[string]any{
			"content": "（已按要求删除）", "email": nil, "page_url": nil, "screenshot_key": nil, "updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
			return err
		}
		return addAudit(tx, actor, "feedback.erase", "feedback:"+formatID(id), &reason, nil, nil)
	})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *GormRepository) Ban(ctx context.Context, sourceHash, reason, actor string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ban := model.FeedbackBan{SourceHash: sourceHash, Reason: &reason, CreatedBy: &actor}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_hash"}}, DoNothing: true}).Create(&ban).Error; err != nil {
			return err
		}
		return addAudit(tx, actor, "feedback.ban", "feedback-source:"+sourceHash, &reason, nil, nil)
	})
}

func (r *GormRepository) Unban(ctx context.Context, sourceHash, actor string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ban model.FeedbackBan
		result := tx.First(&ban, "source_hash = ?", sourceHash)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Delete(&model.FeedbackBan{}, "source_hash = ?", sourceHash).Error; err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"reason": ban.Reason})
		return addAudit(tx, actor, "feedback.unban", "feedback-source:"+sourceHash, nil, before, nil)
	})
}

func addAudit(tx *gorm.DB, actor, action, subject string, reason *string, before, after []byte) error {
	return tx.Create(&model.AdminAuditLog{Actor: actor, Action: action, Subject: &subject, Reason: reason, Before: before, After: after}).Error
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}
