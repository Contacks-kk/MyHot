package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

func runTrackedTask(ctx context.Context, db *gorm.DB, name string, run func() (any, error)) error {
	started := time.Now().UTC()
	record := model.JobRun{Job: name, StartedAt: started, Status: "running"}
	if err := db.WithContext(ctx).Create(&record).Error; err != nil {
		return err
	}
	result, taskErr := run()
	finished := time.Now().UTC()
	updates := map[string]any{"finished_at": finished}
	if taskErr != nil {
		message := taskErr.Error()
		if !utf8.ValidString(message) {
			message = string([]rune(message))
		}
		runes := []rune(message)
		if len(runes) > 4000 {
			message = string(runes[:4000])
		}
		updates["status"] = "failed"
		updates["error"] = message
	} else {
		detail, err := json.Marshal(result)
		if err != nil {
			taskErr = errors.New("scheduled task result could not be encoded")
			updates["status"] = "failed"
			updates["error"] = taskErr.Error()
		} else {
			updates["status"] = "ok"
			updates["detail"] = gorm.Expr("?::jsonb", string(detail))
		}
	}
	updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.WithContext(updateCtx).Model(&model.JobRun{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
		if taskErr != nil {
			return errors.Join(taskErr, err)
		}
		return err
	}
	return taskErr
}
