package adminruns

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const heartbeatInterval = time.Minute

type heartbeatValue struct {
	Heartbeat
	At string `json:"at"`
}

/** Starts a process heartbeat that the admin runs view and worker watchdog can read. */
func StartHeartbeat(ctx context.Context, database *gorm.DB, role string) {
	role = strings.TrimSpace(role)
	if database == nil || role == "" {
		log.Printf("process heartbeat not started: database and role are required")
		return
	}

	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	release := strings.TrimSpace(os.Getenv("AIHOT_RELEASE"))
	if release == "" {
		release = "dev"
	}
	go runHeartbeat(ctx, database, role, host, release, startedAt)
}

/** Writes immediately and refreshes the heartbeat until the process context ends. */
func runHeartbeat(ctx context.Context, database *gorm.DB, role, host, release, startedAt string) {
	write := func() {
		if err := writeHeartbeat(ctx, database, role, host, release, startedAt); err != nil && ctx.Err() == nil {
			log.Printf("process heartbeat write failed: role=%s error=%v", role, err)
		}
	}
	write()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			write()
		}
	}
}

/** Upserts the process identity and current heartbeat timestamp. */
func writeHeartbeat(ctx context.Context, database *gorm.DB, role, host, release, startedAt string) error {
	value, err := json.Marshal(heartbeatValue{
		Heartbeat: Heartbeat{
			PID:       os.Getpid(),
			Host:      host,
			Release:   release,
			StartedAt: startedAt,
		},
		At: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	updatedBy := role
	setting := model.AppSetting{
		Key:       "heartbeat." + role,
		Value:     value,
		UpdatedBy: &updatedBy,
	}
	return database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"value":      value,
			"updated_by": role,
			"updated_at": gorm.Expr("now()"),
		}),
	}).Create(&setting).Error
}
