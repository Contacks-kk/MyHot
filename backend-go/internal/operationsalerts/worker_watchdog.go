package operationsalerts

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const workerHeartbeatStaleAfter = 30 * time.Minute
const workerWatchdogInterval = 5 * time.Minute

type WorkerWatchdogState struct {
	State string    `json:"state"`
	Since time.Time `json:"since"`
}

type WorkerWatchdogStateRepository interface {
	/** Reads the last heartbeat timestamp while the watchdog lock is held. */
	WorkerHeartbeatUpdatedAt(context.Context) (*time.Time, error)
	/** Loads the last acknowledged up/down transition. */
	LoadWorkerWatchdogState(context.Context) (*WorkerWatchdogState, error)
	/** Persists a transition after alert delivery succeeds. */
	SaveWorkerWatchdogState(context.Context, WorkerWatchdogState) error
}

type WorkerWatchdogRepository interface {
	/** Serializes the state read, alert send, and state write. */
	WithWorkerWatchdogLock(context.Context, func(WorkerWatchdogStateRepository) error) error
}

type WorkerWatchdog struct {
	repository WorkerWatchdogRepository
	messenger  Messenger
	config     Config
}

/** Creates the API-owned check that can report a stopped Worker process. */
func NewWorkerWatchdog(repository WorkerWatchdogRepository, messenger Messenger, cfg Config) *WorkerWatchdog {
	return &WorkerWatchdog{
		repository: repository,
		messenger:  messenger,
		config:     cfg,
	}
}

/** Checks worker liveness and records state only after any required alert succeeds. */
func (s *WorkerWatchdog) Check(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	return s.repository.WithWorkerWatchdogLock(ctx, func(repository WorkerWatchdogStateRepository) error {
		return s.checkLocked(ctx, repository, now)
	})
}

/** Runs periodic checks until the API process is shutting down. */
func (s *WorkerWatchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(workerWatchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Check(ctx, now); err != nil && ctx.Err() == nil {
				log.Printf("worker watchdog check failed: %v", err)
			}
		}
	}
}

/** Performs one state transition while the PostgreSQL advisory lock is held. */
func (s *WorkerWatchdog) checkLocked(ctx context.Context, repository WorkerWatchdogStateRepository, now time.Time) error {
	heartbeatAt, err := repository.WorkerHeartbeatUpdatedAt(ctx)
	if err != nil || heartbeatAt == nil {
		return err
	}
	prior, err := repository.LoadWorkerWatchdogState(ctx)
	if err != nil {
		return err
	}
	stale := now.Sub(*heartbeatAt) > workerHeartbeatStaleAfter
	nextState := "up"
	if stale {
		nextState = "down"
	}
	if prior != nil && prior.State == nextState {
		return nil
	}

	since := now
	if stale {
		since = heartbeatAt.UTC()
		finding := Finding{
			Key:    "worker",
			Level:  LevelNow,
			Title:  "后台处理服务停了，网站不会出现新内容",
			Impact: "新内容的采集、处理、推送和日报全部暂停，网站停在旧内容上",
			Heals:  "系统自动重启没有成功",
			Action: "转给 AI 立即处理",
			Detail: "worker 心跳停在 " + beijingStamp(*heartbeatAt) + "；看 Go Worker 的日志（docker compose logs go-worker）",
		}
		title, lines := formatAlert(finding, since, now, false)
		if err := sendOperationsAlert(ctx, s.messenger, s.config, title, lines); err != nil {
			return err
		}
	} else if prior != nil {
		title, lines := formatRecovery("后台处理服务停了，网站不会出现新内容", prior.Since, now)
		if err := sendOperationsAlert(ctx, s.messenger, s.config, title, lines); err != nil {
			return err
		}
	}

	return repository.SaveWorkerWatchdogState(ctx, WorkerWatchdogState{
		State: nextState,
		Since: since,
	})
}

/** Formats and sends an internal operations alert using the shared safety valve and chat selection. */
func sendOperationsAlert(ctx context.Context, messenger Messenger, cfg Config, title string, lines []string) error {
	if !cfg.FeishuInternalEnabled {
		return nil
	}
	chat := strings.TrimSpace(cfg.AlertChatID)
	if chat == "" {
		chat = strings.TrimSpace(cfg.InternalChatID)
	}
	if chat == "" {
		return nil
	}
	if messenger == nil {
		return fmt.Errorf("operations alert messenger is unavailable")
	}
	prefix := ""
	if cfg.EnvironmentName != "" && cfg.EnvironmentName != "production" {
		prefix = "【" + cfg.EnvironmentName + "】"
	}
	return messenger.SendText(ctx, chat, prefix+title+"\n"+strings.Join(lines, "\n"))
}
