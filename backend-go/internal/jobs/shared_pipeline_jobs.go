package jobs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/contentpipeline"
	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/media"
	"aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/publiccontent"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type SharedPipelineScanArgs struct{}

func (SharedPipelineScanArgs) Kind() string { return "myhot_shared_pipeline_scan" }

type SharedPipelineScanWorker struct {
	river.WorkerDefaults[SharedPipelineScanArgs]
	Service *contentpipeline.Service
	Client  *river.Client[pgx.Tx]
	DB      *gorm.DB
}

type ContentAnalyzeArgs struct {
	ArticleID  string `json:"articleId" river:"unique"`
	AttemptTag string `json:"attemptTag,omitempty" river:"unique"`
}

func (ContentAnalyzeArgs) Kind() string { return "content.analyze" }

type ContentAnalyzeWorker struct {
	river.WorkerDefaults[ContentAnalyzeArgs]
	Service *contentpipeline.Service
	Client  *river.Client[pgx.Tx]
}

type EventGroupArgs struct {
	ArticleID  string `json:"articleId" river:"unique"`
	SignalOnly bool   `json:"signalOnly,omitempty"`
}

func (EventGroupArgs) Kind() string { return "events.group" }

type EventGroupWorker struct {
	river.WorkerDefaults[EventGroupArgs]
	Service *contentpipeline.Service
}

func (w *SharedPipelineScanWorker) Work(ctx context.Context, _ *river.Job[SharedPipelineScanArgs]) error {
	return runTrackedTask(ctx, w.DB, "content.sweep", func() (any, error) {
		analysis, groups, err := w.Service.Scan(ctx, 500)
		if err != nil {
			return nil, err
		}
		for _, task := range analysis {
			if _, err := w.Client.Insert(ctx, ContentAnalyzeArgs{ArticleID: task.ArticleID, AttemptTag: task.AttemptTag}, contentAnalyzeInsertOpts(task.Priority)); err != nil {
				return nil, err
			}
		}
		for _, task := range groups {
			if _, err := w.Client.Insert(ctx, EventGroupArgs{ArticleID: task.ArticleID, SignalOnly: task.SignalOnly}, eventGroupInsertOpts(task.Priority)); err != nil {
				return nil, err
			}
		}
		return map[string]int{"analysis": len(analysis), "grouping": len(groups)}, nil
	})
}

func (w *ContentAnalyzeWorker) Work(ctx context.Context, job *river.Job[ContentAnalyzeArgs]) error {
	outcome, err := w.Service.Analyze(ctx, job.Args.ArticleID, job.Args.AttemptTag)
	if err != nil {
		return err
	}
	if outcome.RetryAfter > 0 {
		return river.JobSnooze(outcome.RetryAfter)
	}
	if outcome.NeedGrouping {
		_, err = w.Client.Insert(ctx, EventGroupArgs{ArticleID: job.Args.ArticleID, SignalOnly: outcome.SignalOnly}, eventGroupInsertOpts(3))
	}
	return err
}

func (w *ContentAnalyzeWorker) Timeout(*river.Job[ContentAnalyzeArgs]) time.Duration {
	return 15 * time.Minute
}

func (w *EventGroupWorker) Work(ctx context.Context, job *river.Job[EventGroupArgs]) error {
	_, err := w.Service.Group(ctx, job.Args.ArticleID, job.Args.SignalOnly)
	return err
}

func (w *EventGroupWorker) Timeout(*river.Job[EventGroupArgs]) time.Duration { return 8 * time.Minute }

func contentAnalyzeInsertOpts(priority int) *river.InsertOpts {
	return &river.InsertOpts{Queue: "shared_analysis", Priority: priority, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled},
	}}
}

func eventGroupInsertOpts(priority int) *river.InsertOpts {
	return &river.InsertOpts{Queue: "shared_grouping", Priority: priority, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled},
	}}
}

func sharedPipelinePeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(fiveMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
		return SharedPipelineScanArgs{}, &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: 5 * time.Minute}}
	}, &river.PeriodicJobOpts{ID: "myhot-shared-content-pipeline"})
}

type publicationRiverEffects struct {
	client      *river.Client[pgx.Tx]
	storyDigest bool
}

// NewTransactionClient creates a River inserter for HTTP transactions that need to enqueue work
// atomically without starting a second Worker process.
func NewTransactionClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// MigrateRiver ensures producer processes can enqueue jobs before a Worker has started.
// River owns these tables and its migrator serializes concurrent API/Worker startup.
func MigrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	const lockID int64 = 0x4d59484f54524956
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			_ = connection.Hijack().Close(unlockCtx)
		}
	}()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

func NewPublicationEffects(client *river.Client[pgx.Tx], storyDigest bool) publication.Effects {
	return &publicationRiverEffects{client: client, storyDigest: storyDigest}
}

func (e *publicationRiverEffects) EnqueueSelectedTx(ctx context.Context, tx pgx.Tx, articleID string, scheduledAt time.Time) error {
	if e.client == nil {
		return errors.New("River client is not ready for publication effects")
	}
	_, err := e.client.InsertTx(ctx, tx, SelectedNotifyArgs{ArticleID: articleID}, selectedNotifyInsertOpts(scheduledAt))
	return err
}

func (e *publicationRiverEffects) EnqueueMediaTx(ctx context.Context, tx pgx.Tx, articleID string) error {
	if e.client == nil {
		return errors.New("River client is not ready for publication effects")
	}
	_, err := e.client.InsertTx(ctx, tx, MediaPrepareArgs{ArticleID: articleID}, articleMediaInsertOpts())
	return err
}

// This site's SERVER_MODULES list is empty. Story digest refresh is still a durable content
// effect and must commit with the projection that changed its evidence.
func (e *publicationRiverEffects) ArticleChangedTx(ctx context.Context, tx pgx.Tx, change publication.ArticleChanged) error {
	if !e.storyDigest || e.client == nil {
		return nil
	}
	seen := make(map[int64]struct{}, len(change.StoryIDs))
	for _, storyID := range change.StoryIDs {
		if storyID <= 0 {
			continue
		}
		if _, ok := seen[storyID]; ok {
			continue
		}
		seen[storyID] = struct{}{}
		if _, err := e.client.InsertTx(ctx, tx, StoryDigestArgs{StoryID: storyID}, &river.InsertOpts{
			Queue: "story_digests", Priority: 2, MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func articleMediaInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "media", Priority: 2, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled},
	}}
}

type editorialImageLoader struct{ service *media.Service }

func (l editorialImageLoader) FirstImage(ctx context.Context, input editorial.Input) (string, error) {
	images := make([]editorial.Media, 0, len(input.Media)+2)
	if input.XPost != nil {
		images = append(images, input.XPost.Media...)
		if input.XPost.Quoted != nil {
			images = append(images, input.XPost.Quoted.Media...)
		}
	}
	images = append(images, input.Media...)
	for _, image := range images {
		if image.Kind != "image" || strings.TrimSpace(image.URL) == "" {
			continue
		}
		prepared, err := l.service.Produce(ctx, image.URL, "thumb")
		if err != nil {
			return "", err
		}
		if prepared.Type != "image/jpeg" && prepared.Type != "image/png" && prepared.Type != "image/webp" {
			return "", nil
		}
		return "data:" + prepared.Type + ";base64," + base64.StdEncoding.EncodeToString(prepared.Body), nil
	}
	return "", nil
}

type ArticleMediaPrewarmer struct {
	items  *publiccontent.Service
	images *media.Service
	secret string
	xFull  bool
}

type ArticleMediaResult struct {
	Renditions    int `json:"renditions"`
	Failed        int `json:"failed"`
	AnimatedSaved int `json:"animatedSaved"`
}

var imageProxyPattern = regexp.MustCompile(`/api/img-proxy\?u=([^&"'\\\s]+)&mode=([a-z0-9-]+)`)

func (w *ArticleMediaPrewarmer) Prepare(ctx context.Context, articleID string) (ArticleMediaResult, error) {
	if w == nil || w.items == nil || w.images == nil {
		return ArticleMediaResult{}, errors.New("article media prewarmer is unavailable")
	}
	now := time.Now()
	zh, err := w.items.SiteItem(ctx, articleID, "zh", now, w.secret, w.xFull)
	if err != nil {
		return ArticleMediaResult{}, err
	}
	original, err := w.items.SiteItem(ctx, articleID, "original", now, w.secret, w.xFull)
	if err != nil {
		return ArticleMediaResult{}, err
	}
	if !zh.Found || zh.Item == nil || !original.Found || original.Item == nil {
		return ArticleMediaResult{}, nil
	}
	pairs, err := proxiedRenditions([]any{zh.Item, original.Item})
	if err != nil {
		return ArticleMediaResult{}, err
	}
	result := ArticleMediaResult{Renditions: len(pairs)}
	for _, pair := range pairs {
		image, produceErr := w.images.Produce(ctx, pair.URL, pair.Mode)
		if produceErr != nil {
			result.Failed++
			continue
		}
		if image.Type == "image/gif" {
			count, convertErr := w.images.ConvertAnimated(ctx, pair.URL, pair.Mode)
			if convertErr != nil {
				result.Failed++
			} else {
				result.AnimatedSaved += int(count)
			}
		}
	}
	return result, nil
}

type proxiedRendition struct{ URL, Mode string }

func proxiedRenditions(answers []any) ([]proxiedRendition, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(answers); err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(encoded.String(), "&amp;", "&")
	text = strings.ReplaceAll(text, `\u0026`, "&")
	seen := make(map[string]struct{})
	result := make([]proxiedRendition, 0)
	for _, match := range imageProxyPattern.FindAllStringSubmatch(text, -1) {
		rawURL, err := url.QueryUnescape(match[1])
		if err != nil || rawURL == "" {
			continue
		}
		mode := match[2]
		key := mode + "|" + rawURL
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, proxiedRendition{URL: rawURL, Mode: mode})
	}
	return result, nil
}
