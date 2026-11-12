package jobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const ConnectionTestPrompt = `请只返回 JSON：{"ok":true,"message":"连接成功"}`

type ConnectionTestArgs struct {
	UserID           int64  `json:"userId"`
	ReceiptID        int64  `json:"receiptId" river:"unique"`
	BaseURL          string `json:"baseUrl"`
	Model            string `json:"model"`
	APIKeyCiphertext string `json:"apiKeyCiphertext"`
}

func (ConnectionTestArgs) Kind() string { return "myhot_connection_test" }

type ConnectionTestWorker struct {
	river.WorkerDefaults[ConnectionTestArgs]
	Service *Service
}

func InsertConnectionTest(ctx context.Context, pool *pgxpool.Pool, args ConnectionTestArgs) error {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}
	_, err = client.Insert(ctx, args, &river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

func (w *ConnectionTestWorker) Work(ctx context.Context, job *river.Job[ConnectionTestArgs]) error {
	return callJobError(w.Service.ProcessConnectionTest(ctx, job.Args))
}

func (w *ConnectionTestWorker) Timeout(*river.Job[ConnectionTestArgs]) time.Duration {
	return 75 * time.Second
}
