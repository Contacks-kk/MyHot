package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	_ "time/tzdata"

	"aihot/backend-go/internal/admindeliveries"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/adminselectbench"
	"aihot/backend-go/internal/articlebody"
	backupdomain "aihot/backend-go/internal/backup"
	"aihot/backend-go/internal/collector"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/contentpipeline"
	"aihot/backend-go/internal/editorial"
	feedbackdomain "aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/grouping"
	"aihot/backend-go/internal/heatsnapshot"
	"aihot/backend-go/internal/hotranking"
	"aihot/backend-go/internal/indexnow"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/media"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/mpcollector"
	"aihot/backend-go/internal/operationsalerts"
	"aihot/backend-go/internal/operationsdigest"
	"aihot/backend-go/internal/provider"
	publicationdomain "aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/receiptrecovery"
	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/reports"
	"aihot/backend-go/internal/retention"
	"aihot/backend-go/internal/sourcehealth"
	"aihot/backend-go/internal/sourceicons"
	"aihot/backend-go/internal/sourceschedule"
	"aihot/backend-go/internal/storydigest"
	"aihot/backend-go/internal/storylinks"
	"aihot/backend-go/internal/tagging"
	"aihot/backend-go/internal/topics"
	"aihot/backend-go/internal/translations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type ArticleArgs struct {
	UserID            int64  `json:"userId" river:"unique"`
	ArticleID         string `json:"articleId" river:"unique"`
	Revision          int    `json:"revision"`
	ModelVersion      int    `json:"modelVersion"`
	PreferenceVersion int    `json:"preferenceVersion"`
	ColdStart         bool   `json:"coldStart,omitempty"`
	FilterMode        string `json:"filterMode,omitempty"`
}

func (ArticleArgs) Kind() string { return "myhot_personal_article" }

// TriageCandidate carries compact metadata for one Stage 2 batch triage item.
type TriageCandidate struct {
	ArticleID    string   `json:"articleId"`
	Revision     int      `json:"revision"`
	SourceID     string   `json:"sourceId"`
	Title        string   `json:"title"`
	Snippet      string   `json:"snippet"`
	MatchedHints []string `json:"matchedHints,omitempty"`
	ColdStart    bool     `json:"coldStart,omitempty"`
	FilterMode   string   `json:"filterMode,omitempty"`
}

// TriageBatchArgs represents a user-scoped Stage 2 batch triage LLM call.
type TriageBatchArgs struct {
	UserID            int64             `json:"userId" river:"unique"`
	ModelVersion      int               `json:"modelVersion"`
	PreferenceVersion int               `json:"preferenceVersion"`
	BatchKey          string            `json:"batchKey" river:"unique"`
	Items             []TriageCandidate `json:"items"`
}

/** Kind returns the River job kind for batch triage jobs. */
func (TriageBatchArgs) Kind() string { return "myhot_personal_triage" }

type DailyArgs struct {
	UserID int64  `json:"userId"`
	Date   string `json:"date"`
}

func (DailyArgs) Kind() string { return "myhot_personal_daily" }

// ManualDailyArgs identifies the user whose personal report is requested.
type ManualDailyArgs struct {
	UserID int64 `json:"userId" river:"unique"`
}

/** Kind returns the manual daily report job kind. */
func (ManualDailyArgs) Kind() string { return "myhot_personal_daily_manual" }

type ScanArgs struct{}

func (ScanArgs) Kind() string { return "myhot_personal_scan" }

type DailyScanArgs struct{}

func (DailyScanArgs) Kind() string { return "myhot_daily_scan" }

type TranslationScanArgs struct{}

func (TranslationScanArgs) Kind() string { return "myhot_translation_scan" }

type SharedBodyScanArgs struct{}

func (SharedBodyScanArgs) Kind() string { return "myhot_shared_body_scan" }

type SharedBodyArgs struct {
	ArticleID string `json:"articleId" river:"unique"`
}

func (SharedBodyArgs) Kind() string { return "myhot_shared_body_extract" }

type SelectedNotifyArgs struct {
	ArticleID string `json:"articleId" river:"unique"`
	Attempt   int    `json:"attempt,omitempty" river:"unique"`
}

func (SelectedNotifyArgs) Kind() string { return "notify.selected" }

type ArticleWorker struct {
	river.WorkerDefaults[ArticleArgs]
	Service *Service
	Enabled bool
}

type ScanWorker struct {
	river.WorkerDefaults[ScanArgs]
	Service *Service
	Client  *river.Client[pgx.Tx]
	Enabled bool
}

// TriageBatchWorker consumes user-scoped Stage 2 batch triage jobs.
type TriageBatchWorker struct {
	river.WorkerDefaults[TriageBatchArgs]
	Service *Service
	Client  *river.Client[pgx.Tx]
	Enabled bool
}

type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	Service *Service
	Enabled bool
}

// ManualDailyWorker generates an on-demand report for one user.
type ManualDailyWorker struct {
	river.WorkerDefaults[ManualDailyArgs]
	Service *Service
	Enabled bool
}

type DailyScanWorker struct {
	river.WorkerDefaults[DailyScanArgs]
	Service *Service
	Client  *river.Client[pgx.Tx]
	Enabled bool
}

type TranslationScanWorker struct {
	river.WorkerDefaults[TranslationScanArgs]
	Service *translations.Service
	DB      *gorm.DB
}

type SharedBodyScanWorker struct {
	river.WorkerDefaults[SharedBodyScanArgs]
	Service *articlebody.Service
	Client  *river.Client[pgx.Tx]
	Enabled bool
}

type SharedBodyWorker struct {
	river.WorkerDefaults[SharedBodyArgs]
	Service *articlebody.Service
	Enabled bool
}

type SelectedNotifyWorker struct {
	river.WorkerDefaults[SelectedNotifyArgs]
	Service *admindeliveries.Service
	Client  *river.Client[pgx.Tx]
}

func (w *ScanWorker) Work(ctx context.Context, _ *river.Job[ScanArgs]) error {
	if !w.Enabled {
		return nil
	}
	return w.Service.ScanWithTriage(
		ctx,
		func(ctx context.Context, tx pgx.Tx, args ArticleArgs) error {
			_, err := w.Client.InsertTx(ctx, tx, args, &river.InsertOpts{Priority: 3, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}})
			return err
		},
		func(ctx context.Context, tx pgx.Tx, args TriageBatchArgs) error {
			_, err := w.Client.InsertTx(ctx, tx, args, &river.InsertOpts{Priority: 2, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}})
			return err
		},
	)
}

/** Work runs a user-scoped Stage 2 batch triage job and enqueues Stage 3 articles. */
func (w *TriageBatchWorker) Work(ctx context.Context, job *river.Job[TriageBatchArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return callJobError(w.Service.ProcessTriageBatch(ctx, job.Args, job.ID, job.Attempt, func(ctx context.Context, args ArticleArgs) error {
		_, err := w.Client.Insert(ctx, args, &river.InsertOpts{Priority: 3, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}})
		return err
	}))
}

func (w *ArticleWorker) Work(ctx context.Context, job *river.Job[ArticleArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return callJobError(w.Service.ProcessArticle(ctx, job.Args, job.ID, job.Attempt))
}

func (w *DailyScanWorker) Work(ctx context.Context, _ *river.Job[DailyScanArgs]) error {
	if !w.Enabled {
		return nil
	}
	return w.Service.ScheduleDailyReports(ctx, func(ctx context.Context, args DailyArgs) error {
		_, err := w.Client.Insert(ctx, args, &river.InsertOpts{Priority: 2, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}})
		return err
	})
}

func (w *DailyWorker) Work(ctx context.Context, job *river.Job[DailyArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return callJobError(w.Service.ProcessDailyReport(ctx, job.Args, job.ID, job.Attempt))
}

/** Work generates a report using the original request time as its cutoff. */
func (w *ManualDailyWorker) Work(ctx context.Context, job *river.Job[ManualDailyArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return callJobError(w.Service.ProcessManualDailyReport(ctx, job.Args.UserID, job.ID, job.Attempt, job.CreatedAt))
}

/** Timeout allows large histories to be summarized in bounded model calls. */
func (w *ManualDailyWorker) Timeout(*river.Job[ManualDailyArgs]) time.Duration {
	return 10 * time.Minute
}

/** InsertManualDaily queues at most one active manual report per user. */
func InsertManualDaily(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return 0, err
	}
	result, err := client.Insert(ctx, ManualDailyArgs{UserID: userID}, &river.InsertOpts{
		Priority:    2,
		MaxAttempts: 1,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}},
	})
	if err != nil {
		return 0, err
	}
	if result.Job == nil {
		return 0, errors.New("River did not return a report job")
	}
	return result.Job.ID, nil
}

func (w *TranslationScanWorker) Work(ctx context.Context, _ *river.Job[TranslationScanArgs]) error {
	return runTrackedTask(ctx, w.DB, "content.translate", func() (any, error) {
		return w.Service.ProcessPending(ctx, 30, 4*time.Minute)
	})
}

func (w *TranslationScanWorker) Timeout(*river.Job[TranslationScanArgs]) time.Duration {
	return 7 * time.Minute
}

func (w *SharedBodyScanWorker) Work(ctx context.Context, _ *river.Job[SharedBodyScanArgs]) error {
	if !w.Enabled {
		return nil
	}
	candidates, err := w.Service.Scan(ctx, 200)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		_, err := w.Client.Insert(ctx, SharedBodyArgs{ArticleID: candidate.ID}, &river.InsertOpts{
			Queue: "shared_content", Priority: 2, MaxAttempts: 5,
			UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *SharedBodyWorker) Work(ctx context.Context, job *river.Job[SharedBodyArgs]) error {
	if !w.Enabled {
		return nil
	}
	outcome, err := w.Service.Extract(ctx, job.Args.ArticleID)
	if err != nil {
		return err
	}
	if outcome.RetryAfter > 0 {
		return river.JobSnooze(outcome.RetryAfter)
	}
	return nil
}

func (w *SharedBodyWorker) Timeout(*river.Job[SharedBodyArgs]) time.Duration { return 90 * time.Second }

func (w *SelectedNotifyWorker) Work(ctx context.Context, job *river.Job[SelectedNotifyArgs]) error {
	if job.Args.Attempt == 0 {
		if err := w.Service.WarmSelected(ctx, job.Args.ArticleID, time.Now()); err != nil {
			log.Printf("selected share image prewarm failed article_id=%s: %v", job.Args.ArticleID, err)
		}
	}
	result, err := w.Service.PushSelected(ctx, job.Args.ArticleID, time.Now())
	if err != nil {
		return err
	}
	if result.Status == "retry" && job.Args.Attempt < 6 {
		args := SelectedNotifyArgs{ArticleID: job.Args.ArticleID, Attempt: job.Args.Attempt + 1}
		_, err := w.Client.Insert(ctx, args, selectedNotifyInsertOpts(time.Now().Add(result.RetryAfter)))
		return err
	}
	return nil
}

func selectedNotifyInsertOpts(scheduledAt time.Time) *river.InsertOpts {
	return &river.InsertOpts{
		Queue: "content_delivery", Priority: 2, MaxAttempts: 1, ScheduledAt: scheduledAt,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		}},
	}
}

func (w *SelectedNotifyWorker) Timeout(*river.Job[SelectedNotifyArgs]) time.Duration {
	return 40 * time.Second
}

func translationInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue: "translations", Priority: 2, MaxAttempts: 2,
		UniqueOpts: river.UniqueOpts{ByPeriod: 5 * time.Minute},
	}
}

type fiveMinuteSchedule struct{}

func (fiveMinuteSchedule) Next(current time.Time) time.Time {
	return current.Truncate(5 * time.Minute).Add(5 * time.Minute)
}

func translationPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(fiveMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
		return TranslationScanArgs{}, translationInsertOpts()
	}, &river.PeriodicJobOpts{ID: "myhot-content-translate"})
}

// The report is always for [yesterday 08:00, today 08:00), even on a delayed retry.
func dailyWindow(date string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid report date")
	}
	end := day.Add(8 * time.Hour)
	return end.Add(-24 * time.Hour), end, nil
}

// Shorten summaries together so all articles in the fixed window can fit the
// model request. The saved report retains the full article summaries.
func dailyInput(entries []dailyEntry) ([]byte, error) {
	type item struct {
		ArticleID string `json:"articleId"`
		Score     int    `json:"score"`
		Title     string `json:"title"`
		Source    string `json:"source"`
		Summary   string `json:"summary,omitempty"`
	}
	var body []byte
	for _, summaryLength := range []int{220, 100, 40, 0} {
		input := make([]item, len(entries))
		for i, entry := range entries {
			summary := []rune(entry.Summary)
			if len(summary) > summaryLength {
				summary = summary[:summaryLength]
			}
			input[i] = item{ArticleID: entry.ArticleID, Score: entry.Score, Title: entry.Title, Source: entry.Source, Summary: string(summary)}
		}
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return nil, errors.New("日报内容无法编码")
		}
		if len(body) <= 60000 {
			return body, nil
		}
	}
	return body, errors.New("过去24小时的条目数量超出单次日报模型输入范围，请缩小关注范围后等待下一日报")
}

type beijingSchedule struct{ location *time.Location }

func (s beijingSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	next := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, s.location)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func BeijingEightSchedule() (river.PeriodicSchedule, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	return beijingSchedule{location: loc}, nil
}

func dailyScanInsertOpts() *river.InsertOpts {
	// Keep RunOnStart useful for restarts before 08:00: its uniqueness window
	// must be shorter than the interval until today's scheduled run.
	return &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Hour}}
}

/** NewClient assembles the River worker client and schedules. */
func NewClient(ctx context.Context, pool *pgxpool.Pool, orm *gorm.DB, cfg config.Config, entities map[string]interests.Entity, publication interests.PublicationSettings, chat provider.ChatFunc, topicDefinitions []topics.Definition) (*river.Client[pgx.Tx], error) {
	if err := MigrateRiver(ctx, pool); err != nil {
		return nil, err
	}
	driver := riverpgxv5.New(pool)
	repository := NewRepository(orm, pool)
	if err := repository.EnsureRollout(ctx); err != nil {
		return nil, err
	}
	service := NewService(repository, cfg, chat)
	service.SetEntities(entities)
	modelCatalog, err := adminmodels.LoadCatalog()
	if err != nil {
		return nil, err
	}
	modelPresets := make(map[string]modelcall.Preset, len(modelCatalog.Presets))
	for key, preset := range modelCatalog.Presets {
		modelPresets[key] = modelcall.Preset{
			Service:         preset.Service,
			Model:           preset.Model,
			BaseURLEnv:      preset.BaseURLEnv,
			APIKeyEnv:       preset.APIKeyEnv,
			Extra:           preset.Extra,
			ReasoningTokens: preset.ReasoningTokens,
			JSONMode:        preset.JSONMode,
			Vision:          preset.Vision,
		}
	}
	modelCapabilities := make(map[string]modelcall.Capability, len(modelCatalog.Capabilities))
	for _, capability := range modelCatalog.Capabilities {
		modelCapabilities[capability.Key] = modelcall.Capability{Key: capability.Key, Env: capability.Env, Default: capability.Default, Vision: capability.Vision}
	}
	receiptService := receipts.NewService(receipts.NewRepository(orm))
	imageService := media.NewService(media.Config{
		DataDir: cfg.DataDir, SignSecret: cfg.ImageProxySignSecret, MinuteBudgetBytes: cfg.ImageProxyMinuteBudgetBytes,
		DailyBudgetBytes: cfg.ImageProxyDailyBudgetBytes, EgressProxyURL: cfg.EgressProxyURL, DirectFetchHosts: cfg.DirectFetchHosts,
		SiteURL: cfg.SiteURL, CrawlerName: cfg.SiteCrawlerName,
	}, media.NewRepository(orm), nil)
	sharedModelClient := modelcall.NewClient(cfg.ModelCallsEnabled, modelcall.NewCatalogResolver(
		adminmodels.NewRepository(orm), modelcall.Catalog{Presets: modelPresets, PresetOrder: modelCatalog.PresetOrder,
			Defaults: modelCatalog.Defaults, Capabilities: modelCapabilities}, cfg.ModelEnvironment, cfg.ModelCredential,
	), receiptService, nil)
	service.SharedModels = sharedModelClient
	var pipelineService *contentpipeline.Service
	var pipelineEffects *publicationRiverEffects
	var publicationRepository *publicationdomain.Repository
	var sourceRepublishService *publicationdomain.Service
	if cfg.SharedPipelineEnabled {
		prompts, err := editorial.LoadPrompts(cfg.SiteName)
		if err != nil {
			return nil, err
		}
		vocabulary, err := editorial.LoadVocabulary()
		if err != nil {
			return nil, err
		}
		selection, err := editorial.LoadSelectionRules()
		if err != nil {
			return nil, err
		}
		categories, err := interests.LoadCategories()
		if err != nil {
			return nil, err
		}
		entityEntries, err := interests.LoadEntityEntries()
		if err != nil {
			return nil, err
		}
		analysis, err := editorial.NewAnalysisService(sharedModelClient, prompts, vocabulary, categories, entityEntries, selection, editorialImageLoader{service: imageService}, nil)
		if err != nil {
			return nil, err
		}
		publicationRepository = publicationdomain.NewRepository(pool, entities, publication, cfg.SiteName, cfg.SiteURL)
		pipelineEffects = &publicationRiverEffects{storyDigest: cfg.StoryDigestEnabled}
		publicationRepository.SetEffects(pipelineEffects)
		publisher := publicationdomain.NewService(publicationRepository)
		sourceRepublishService = publicationdomain.NewService(publicationRepository)
		groupRepository := grouping.NewRepository(orm)
		embeddings := grouping.NewEmbeddingStore(groupRepository, receiptService, grouping.EmbeddingConfigFromRuntime(cfg.ModelCallsEnabled, cfg.ModelCredential), nil)
		recall := grouping.NewRecallService(groupRepository, embeddings, nil)
		judge := grouping.NewJudgeService(sharedModelClient, prompts)
		groupingService := grouping.NewService(groupRepository, recall, judge, embeddings, publisher, grouping.WorkflowConfig{Entities: entities})
		analysisService := editorial.NewProcessingService(editorial.NewRepository(orm), analysis, entities)
		pipelineService = contentpipeline.NewService(contentpipeline.NewRepository(orm, cfg.SharedPipelineEnabled), analysisService, groupingService, publisher, nil)
	}
	var storyDigestService *storydigest.Service
	if cfg.StoryDigestEnabled && cfg.ModelCallsEnabled {
		prompts, err := storydigest.LoadPrompts()
		if err != nil {
			return nil, err
		}
		storyDigestService = storydigest.NewService(storydigest.NewRepository(orm), sharedModelClient, prompts, storydigest.Config{SiteName: cfg.SiteName})
	}
	translationPrompts, err := translations.LoadPromptFiles()
	if err != nil {
		return nil, err
	}
	translationService := translations.NewService(translations.NewRepository(orm), sharedModelClient, translationPrompts, cfg.SiteName)
	reportCategories, err := interests.LoadCategories()
	if err != nil {
		return nil, err
	}
	contentCategoryLabels := make(map[string]string, len(reportCategories))
	for _, category := range reportCategories {
		contentCategoryLabels[category.Key] = category.Label
	}
	reportingConfig, err := reports.LoadReportingConfig()
	if err != nil {
		return nil, err
	}
	reportPrompts, err := reports.LoadPromptFiles()
	if err != nil {
		return nil, err
	}
	reportCategoryConfig := make([]reports.Category, len(reportCategories))
	for i, category := range reportCategories {
		reportCategoryConfig[i] = reports.Category{Key: category.Key, Section: category.Section, Commentary: category.Commentary}
	}
	reportService, err := reports.NewService(reports.NewRepository(orm), sharedModelClient, reports.Config{
		SiteName: cfg.SiteName, DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly,
		EntryMeasure: cfg.SiteReportEntryMeasure, EntryNoun: cfg.SiteReportEntryNoun, Categories: reportCategoryConfig,
		Entities: entities, Reporting: reportingConfig, Prompts: reportPrompts, CommunityFeeds: publication,
	})
	if err != nil {
		return nil, err
	}
	reportSchedule, err := newHalfHourSchedule()
	if err != nil {
		return nil, err
	}
	dailySchedule, err := BeijingEightSchedule()
	if err != nil {
		return nil, err
	}
	indexNowSchedule, err := newIndexNowSchedule()
	if err != nil {
		return nil, err
	}
	scanWorker := &ScanWorker{Service: service, Enabled: cfg.PersonalizationEnabled}
	triageBatchWorker := &TriageBatchWorker{Service: service, Enabled: cfg.PersonalizationEnabled && cfg.ModelCallsEnabled}
	dailyScanWorker := &DailyScanWorker{Service: service, Enabled: cfg.PersonalizationEnabled}
	translationScanWorker := &TranslationScanWorker{Service: translationService, DB: orm}
	materialService := materials.NewService(materials.NewRepository(orm))
	if cfg.CollectorEnabled {
		if err := sourceschedule.Reconcile(ctx, orm, cfg.SourceSchedule); err != nil {
			return nil, fmt.Errorf("reconcile source schedules: %w", err)
		}
	}
	collectorService := collector.NewService(collector.NewRepository(orm, pool, cfg.SourceSchedule), collector.NewHTTPClient(), materialService)
	var xArticleReader articlebody.XArticleReader
	var xShardWorker *XShardWorker
	if strings.TrimSpace(cfg.SocialDataKey) != "" {
		xProvider := collector.NewSocialData(cfg.SocialDataKey, cfg.SocialDataBaseURL, receiptService, nil)
		collectorService.XProvider = xProvider
		xArticleReader = xProvider
		xShardWorker = &XShardWorker{Service: collectorService, Enabled: cfg.CollectorEnabled}
	}
	bodyRepository := articlebody.NewRepository(orm, xArticleReader != nil || cfg.SharedPipelineEnabled, cfg.SharedPipelineEnabled)
	if publicationRepository != nil {
		bodyRepository = articlebody.NewTransactionalRepository(orm, pool, publicationRepository, xArticleReader != nil || cfg.SharedPipelineEnabled, cfg.SharedPipelineEnabled)
	}
	sharedBodyService := articlebody.NewService(bodyRepository, articlebody.NewPageFetcher(collector.NewHTTPClient()),
		articlebody.NewJinaReader(cfg.JinaAPIKey, cfg.JinaBaseURL, collector.NewHTTPClientWithTimeout(60*time.Second), receiptService), xArticleReader)
	sharedBodyScanWorker := &SharedBodyScanWorker{Service: sharedBodyService, Enabled: cfg.SharedBodyEnabled && cfg.CollectorEnabled}
	var mpScanWorker *MPScanWorker
	var mpCheckWorker *MPCheckWorker
	var mpService *mpcollector.Service
	if strings.TrimSpace(cfg.DajialaKey) != "" {
		mpRepository := mpcollector.NewRepository(orm)
		mpProvider := mpcollector.NewDajiala(mpcollector.Config{Key: cfg.DajialaKey, BaseURL: cfg.DajialaBaseURL}, receiptService, nil)
		mpService = mpcollector.NewService(mpRepository, materialService, mpProvider)
		mpCheckWorker = &MPCheckWorker{Service: mpService, Enabled: true}
		if cfg.CollectorEnabled {
			mpScanWorker = &MPScanWorker{Service: mpService, DB: orm, Enabled: true}
		}
	}
	retentionService := retention.NewService(retention.NewRepository(orm), retention.Config{
		DataDir: cfg.DataDir, ScreenshotsForwarded: cfg.FeishuInternalEnabled && strings.TrimSpace(cfg.FeishuInternalChatID) != "",
	})
	feishuForwarder := feedbackdomain.NewFeishuForwarder(cfg.FeishuAppID, cfg.FeishuAppSecret)
	feedbackService := feedbackdomain.NewService(feedbackdomain.NewRepository(orm), feedbackdomain.Config{
		DataDir: cfg.DataDir, SessionSecret: cfg.SessionSecret, FeishuInternalEnabled: cfg.FeishuInternalEnabled,
		FeishuInternalChatID: cfg.FeishuInternalChatID,
	}, feishuForwarder)

	// 创建SourceWorker，绑定采集器Service和采集开关
	sourceScanWorker := &SourceScanWorker{Service: collectorService, Enabled: cfg.CollectorEnabled}
	retentionScanWorker := &RetentionScanWorker{Service: retentionService}
	feedbackScanWorker := &FeedbackScanWorker{
		Service: feedbackService,
		Enabled: cfg.FeishuInternalEnabled && strings.TrimSpace(cfg.FeishuInternalChatID) != ""}
	sourceHealthService := sourcehealth.NewService(sourcehealth.NewRepository(orm), feishuForwarder, sourcehealth.Config{
		Enabled:         cfg.FeishuInternalEnabled,
		AlertChatID:     cfg.FeishuAlertChatID,
		InternalChatID:  cfg.FeishuInternalChatID,
		EnvironmentName: cfg.EnvironmentName,
	})
	sourceHealthScanWorker := &SourceHealthScanWorker{Service: sourceHealthService}
	sourceHealthWorker := &SourceHealthWorker{Service: sourceHealthService, DB: orm}
	sourceIconsService := sourceicons.NewService(sourceicons.NewRepository(orm), collector.NewHTTPClient(), "Mozilla/5.0 (compatible; source icon fetcher)")
	sourceIconsScanWorker := &SourceIconsScanWorker{Service: sourceIconsService}
	sourceIconsWorker := &SourceIconsWorker{Service: sourceIconsService, DB: orm}
	indexNowService := indexnow.NewService(indexnow.NewRepository(orm), indexnow.NewHTTPSubmitter(nil), indexnow.Config{
		SiteURL: cfg.SiteURL, Key: cfg.IndexNowKey, Enabled: cfg.IndexNowSubmitEnabled,
	}, topicDefinitions)
	indexNowWorker := &IndexNowWorker{Service: indexNowService, DB: orm}
	storyLinksService := storylinks.NewService(storylinks.NewRepository(orm))
	storyLinksSchedule, err := newStoryLinksSchedule()
	if err != nil {
		return nil, err
	}
	heatRepository := heatsnapshot.NewRepository(orm)
	heatSnapshotService := heatsnapshot.NewService(heatRepository, heatsnapshot.CommunityFeeds{
		Dev: publication.CommunityFeeds.Dev, HN: publication.CommunityFeeds.HN,
	})
	heatSnapshotSchedule, err := newHeatSnapshotSchedule()
	if err != nil {
		return nil, err
	}
	hotRankingService := hotranking.NewService(hotranking.NewRepository(orm), heatSnapshotService, entities)
	hotRankingSchedule, err := newHotRankingSchedule()
	if err != nil {
		return nil, err
	}
	backupService := backupdomain.NewService(backupdomain.Config{
		DatabaseURL: cfg.DatabaseURL, DatabaseName: backupdomain.DatabaseName(cfg.DatabaseURL), DataDir: cfg.DataDir,
		Store: backupdomain.Store{SecretID: cfg.BackupStoreSecretID, SecretKey: cfg.BackupStoreSecretKey,
			Bucket: cfg.BackupStoreBucket, Region: cfg.BackupStoreRegion, Domain: cfg.BackupStoreDomain},
		ScreenshotsForwarded: cfg.FeishuInternalEnabled && strings.TrimSpace(cfg.FeishuInternalChatID) != "",
	}, backupdomain.NewRepository(orm))
	backupScanWorker := &BackupScanWorker{Service: backupService}
	operationsDigestService := operationsdigest.NewService(operationsdigest.NewRepository(orm), feishuForwarder, operationsdigest.Config{
		CollectEnabled: cfg.CollectorEnabled, BackupConfigured: backupService.Configured(),
		FeishuInternalEnabled: cfg.FeishuInternalEnabled, AlertChatID: cfg.FeishuAlertChatID,
		InternalChatID: cfg.FeishuInternalChatID, EnvironmentName: cfg.EnvironmentName,
	})
	operationsDigestScanWorker := &OpsDigestScanWorker{Service: operationsDigestService}
	operationsDigestWorker := &OpsDigestWorker{Service: operationsDigestService, DB: orm}
	operationsAlertsService := operationsalerts.NewService(operationsalerts.NewRepository(orm), feishuForwarder, operationsalerts.Config{
		CollectEnabled: cfg.CollectorEnabled, ModelCallsEnabled: cfg.ModelCallsEnabled,
		BackupConfigured: backupService.Configured(), FeishuInternalEnabled: cfg.FeishuInternalEnabled,
		AlertChatID: cfg.FeishuAlertChatID, InternalChatID: cfg.FeishuInternalChatID,
		EnvironmentName: cfg.EnvironmentName, QuietMinutes: min(cfg.AlertQuietMinutes, 1440), DailyEdition: cfg.SiteEditionDaily,
	})
	operationsAlertsWorker := &OpsAlertsWorker{Service: operationsAlertsService, DB: orm}
	deliveryRecoveryWorker := &DeliveryRecoveryWorker{Service: admindeliveries.NewService(admindeliveries.NewRepository(orm), admindeliveries.Config{}, nil), DB: orm}
	contentDeliveryService := admindeliveries.NewService(admindeliveries.NewRepository(orm), admindeliveries.Config{
		ContentPushEnabled: cfg.FeishuContentPushEnabled,
		WebhookURLs: map[string]string{
			"FEISHU_PUSH_WEBHOOK_URL":        cfg.FeishuPushWebhookURL,
			"FEISHU_PUSH_MIRROR_WEBHOOK_URL": cfg.FeishuPushMirrorWebhookURL,
		},
		SiteName: cfg.SiteName, SiteURL: cfg.SiteURL, CategoryLabels: contentCategoryLabels,
	}, admindeliveries.HTTPWebhookSender{}, admindeliveries.HTTPShareImageWarmer{
		CategoryLabels: contentCategoryLabels,
		URL:            cfg.WebOGPrewarmURL, Secret: cfg.WebOGPrewarmSecret,
	})
	selectedNotifyWorker := &SelectedNotifyWorker{Service: contentDeliveryService}
	receiptRecoveryService := receiptrecovery.NewService(receiptrecovery.NewRepository(orm))
	receiptRecoveryWorker := &ReceiptRecoveryWorker{Service: receiptRecoveryService, DB: orm}
	workers := river.NewWorkers()
	if cfg.ModelCallsEnabled {
		prompts, err := editorial.LoadPrompts(cfg.SiteName)
		if err != nil {
			return nil, err
		}
		vocabulary, err := editorial.LoadVocabulary()
		if err != nil {
			return nil, err
		}
		selection, err := editorial.LoadSelectionRules()
		if err != nil {
			return nil, err
		}
		categories, err := interests.LoadCategories()
		if err != nil {
			return nil, err
		}
		entityEntries, err := interests.LoadEntityEntries()
		if err != nil {
			return nil, err
		}
		selectionAnalysis, err := editorial.NewAnalysisService(sharedModelClient, prompts, vocabulary, categories, entityEntries, selection, nil, nil)
		if err != nil {
			return nil, err
		}
		selectionEvaluation := editorial.NewSelectionEvaluationService(
			selectionAnalysis,
			receipts.NewRepository(orm),
			append([]string{"default"}, modelCatalog.PresetOrder...),
		)
		river.AddWorker(workers, &SelectionEvaluationWorker{
			Service:  selectionEvaluation,
			Models:   sharedModelClient,
			Importer: adminselectbench.NewService(adminselectbench.NewRepository(orm)),
			DB:       orm,
			Enabled:  true,
		})
		evaluation := grouping.NewRelationEvaluationService(
			grouping.NewJudgeService(sharedModelClient, prompts),
			grouping.NewRepository(orm),
			append([]string{"default"}, modelCatalog.PresetOrder...),
		)
		river.AddWorker(workers, &RelationEvaluationWorker{Service: evaluation, Models: sharedModelClient, DB: orm})
		digestWorker := &StoryDigestEvaluationWorker{Enabled: cfg.StoryDigestEnabled, Models: sharedModelClient, DB: orm, Usage: receipts.NewRepository(orm)}
		if cfg.StoryDigestEnabled {
			digestPrompts, err := storydigest.LoadPrompts()
			if err != nil {
				return nil, err
			}
			digestWorker.Service = storydigest.NewEvaluationService(sharedModelClient, prompts, digestPrompts, cfg.SiteName, append([]string{"default"}, modelCatalog.PresetOrder...))
		}
		river.AddWorker(workers, digestWorker)
	}
	var storyDigestScanWorker *StoryDigestScanWorker
	var sharedPipelineScanWorker *SharedPipelineScanWorker
	var sourceRepublishScanWorker *SourceRepublishScanWorker
	pgBossHandoffScanWorker := &PgBossHandoffScanWorker{Pool: pool, DB: orm, CollectorEnabled: cfg.CollectorEnabled,
		ManualMPEnabled: cfg.CollectorEnabled && strings.TrimSpace(cfg.DajialaKey) != "", DigestEnabled: cfg.StoryDigestEnabled && cfg.ModelCallsEnabled}
	river.AddWorker(workers, pgBossHandoffScanWorker)
	if cfg.GoMediaPrepareEnabled {
		mediaItems := publiccontent.NewService(publiccontent.NewRepository(orm), entities)
		mediaItems.SetTopics(topicDefinitions)
		river.AddWorker(workers, &MediaPrepareWorker{Service: imageService, Articles: &ArticleMediaPrewarmer{
			items: mediaItems, images: imageService, secret: cfg.ImageProxySignSecret, xFull: cfg.XPostIsFullText,
		}})
	}
	if cfg.SharedPipelineEnabled {
		sharedPipelineScanWorker = &SharedPipelineScanWorker{Service: pipelineService, DB: orm}
		sourceRepublishScanWorker = &SourceRepublishScanWorker{Service: sourceRepublishService, DB: orm}
		river.AddWorker(workers, sharedPipelineScanWorker)
		river.AddWorker(workers, &ContentAnalyzeWorker{Service: pipelineService})
		river.AddWorker(workers, &EventGroupWorker{Service: pipelineService})
		river.AddWorker(workers, sourceRepublishScanWorker)
		river.AddWorker(workers, &SourceRepublishWorker{Service: sourceRepublishService, HotRanking: hotRankingService, DB: orm})
	}
	river.AddWorker(workers, &ArticleWorker{Service: service, Enabled: cfg.PersonalizationEnabled && cfg.ModelCallsEnabled})
	river.AddWorker(workers, &ManualDailyWorker{Service: service, Enabled: cfg.PersonalizationEnabled && cfg.ModelCallsEnabled})
	river.AddWorker(workers, scanWorker)
	river.AddWorker(workers, triageBatchWorker)
	river.AddWorker(workers, &DailyWorker{Service: service, Enabled: cfg.PersonalizationEnabled})
	river.AddWorker(workers, &ConnectionTestWorker{Service: service})
	river.AddWorker(workers, dailyScanWorker)
	river.AddWorker(workers, translationScanWorker)
	if cfg.SharedBodyEnabled && cfg.CollectorEnabled {
		river.AddWorker(workers, sharedBodyScanWorker)
		river.AddWorker(workers, &SharedBodyWorker{Service: sharedBodyService, Enabled: true})
	}

	// 注册扫描Worker，开启采集时注册SourceScanWorker
	river.AddWorker(workers, &ReportComposeWorker{Service: reportService, DB: orm})
	river.AddWorker(workers, sourceScanWorker)
	var sourceWorker *SourceWorker
	if cfg.CollectorEnabled {
		sourceWorker = &SourceWorker{Service: collectorService, DB: orm, Enabled: true}
		river.AddWorker(workers, sourceWorker)
	}

	taggingEngine := tagging.NewEngine(entities, topicDefinitions)
	taggingService := tagging.NewService(orm, taggingEngine, chat, "")
	river.AddWorker(workers, &SourceTaggingWorker{Service: taggingService})
	if xShardWorker != nil {
		river.AddWorker(workers, xShardWorker)
	}
	river.AddWorker(workers, retentionScanWorker)
	river.AddWorker(workers, &RetentionWorker{Service: retentionService})
	river.AddWorker(workers, feedbackScanWorker)
	river.AddWorker(workers, &FeedbackForwardWorker{Service: feedbackService})
	river.AddWorker(workers, sourceHealthScanWorker)
	river.AddWorker(workers, sourceHealthWorker)
	if cfg.CollectorEnabled {
		river.AddWorker(workers, sourceIconsScanWorker)
		river.AddWorker(workers, sourceIconsWorker)
	}
	river.AddWorker(workers, indexNowWorker)
	river.AddWorker(workers, &StoryLinksWorker{Service: storyLinksService, DB: orm})
	if storyDigestService != nil {
		storyDigestScanWorker = &StoryDigestScanWorker{Service: storyDigestService}
		river.AddWorker(workers, storyDigestScanWorker)
		river.AddWorker(workers, &StoryDigestWorker{Service: storyDigestService})
	}
	river.AddWorker(workers, &HeatSnapshotWorker{Service: heatSnapshotService, DB: orm})
	river.AddWorker(workers, &HotRankingWorker{Service: hotRankingService, DB: orm})
	if backupService.Configured() {
		river.AddWorker(workers, backupScanWorker)
		river.AddWorker(workers, &BackupWorker{Service: backupService})
	}
	river.AddWorker(workers, operationsDigestScanWorker)
	river.AddWorker(workers, operationsDigestWorker)
	river.AddWorker(workers, operationsAlertsWorker)
	river.AddWorker(workers, deliveryRecoveryWorker)
	river.AddWorker(workers, receiptRecoveryWorker)
	if cfg.GoContentDeliveryEnabled {
		river.AddWorker(workers, selectedNotifyWorker)
	}
	if mpScanWorker != nil {
		river.AddWorker(workers, mpScanWorker)
	}
	if mpCheckWorker != nil {
		river.AddWorker(workers, mpCheckWorker)
	}

	// 每分钟创建SourceScanArgs，配置RunInStart
	periodicJobs := []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return SourceScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-source-pool-scan", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return ScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-personalization-scan", RunOnStart: true}),
		river.NewPeriodicJob(dailySchedule, func() (river.JobArgs, *river.InsertOpts) {
			return DailyScanArgs{}, dailyScanInsertOpts()
		}, &river.PeriodicJobOpts{ID: "myhot-daily-report-scan", RunOnStart: true}),
		translationPeriodicJob(),
		pgBossHandoffPeriodicJob(),
		reportComposePeriodicJob(reportSchedule),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return RetentionScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-retention-scan", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return OpsDigestScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-ops-digest-scan", RunOnStart: true}),
		deliveryRecoveryPeriodicJob(),
		opsAlertsPeriodicJob(),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return SourceHealthScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-source-health-scan", RunOnStart: true}),
		river.NewPeriodicJob(tenMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
			return FeedbackScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: 10 * time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-feedback-forward-scan"}),
		indexNowPeriodicJob(indexNowSchedule),
		storyLinksPeriodicJob(storyLinksSchedule),
		heatSnapshotPeriodicJob(heatSnapshotSchedule),
		hotRankingPeriodicJob(hotRankingSchedule),
	}
	if storyDigestService != nil {
		periodicJobs = append(periodicJobs, storyDigestPeriodicJob())
	}
	periodicJobs = append(periodicJobs, receiptRecoveryPeriodicJob())
	if cfg.SharedBodyEnabled && cfg.CollectorEnabled {
		periodicJobs = append(periodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return SharedBodyScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-shared-body-scan", RunOnStart: true}))
	}
	if mpScanWorker != nil {
		mpSchedule, err := newMPAccountSchedule()
		if err != nil {
			return nil, err
		}
		periodicJobs = append(periodicJobs, river.NewPeriodicJob(mpSchedule, func() (river.JobArgs, *river.InsertOpts) {
			return MPScanArgs{}, &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: 14 * time.Minute}}
		}, mpAccountPeriodicOptions()))
	}
	if cfg.CollectorEnabled {
		periodicJobs = append(periodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return SourceIconsScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-source-icons-scan", RunOnStart: true}))
	}
	if backupService.Configured() {
		periodicJobs = append(periodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return BackupScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
		}, &river.PeriodicJobOpts{ID: "myhot-backup-scan", RunOnStart: true}))
	}
	queues := map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: cfg.UserJobMaxWorkers}}
	if cfg.ModelCallsEnabled {
		queues["evaluations"] = river.QueueConfig{MaxWorkers: 1}
	}
	if cfg.GoMediaPrepareEnabled {
		queues["media"] = river.QueueConfig{MaxWorkers: 1}
	}
	if cfg.GoContentDeliveryEnabled {
		queues["content_delivery"] = river.QueueConfig{MaxWorkers: 1}
	}
	if cfg.SharedPipelineEnabled {
		queues["shared_analysis"] = river.QueueConfig{MaxWorkers: 6}
		queues["shared_grouping"] = river.QueueConfig{MaxWorkers: 1}
		queues["source_republish"] = river.QueueConfig{MaxWorkers: 1}
	}
	queues["translations"] = river.QueueConfig{MaxWorkers: 1}
	queues["reports"] = river.QueueConfig{MaxWorkers: 1}
	if storyDigestService != nil {
		queues["story_digests"] = river.QueueConfig{MaxWorkers: 3}
	}
	if cfg.SharedBodyEnabled && cfg.CollectorEnabled {
		queues["shared_content"] = river.QueueConfig{MaxWorkers: 4}
	}
	if cfg.SharedPipelineEnabled {
		periodicJobs = append(periodicJobs, sharedPipelinePeriodicJob())
		periodicJobs = append(periodicJobs, sourceRepublishPeriodicJob())
	}
	if mpCheckWorker != nil {
		queues["mp_accounts"] = river.QueueConfig{MaxWorkers: 2}
	}
	if xShardWorker != nil {
		queues["x_search"] = river.QueueConfig{MaxWorkers: 2}
	}

	// rss_source队列每实例并发2，将worker和周期任务传入River
	queues["rss_sources"] = river.QueueConfig{MaxWorkers: 2}
	client, err := river.NewClient(driver, &river.Config{Queues: queues, Workers: workers, JobTimeout: 75 * time.Second, RescueStuckJobsAfter: retention.StuckRunGrace, PeriodicJobs: periodicJobs})
	if err != nil {
		return nil, err
	}
	scanWorker.Client = client
	triageBatchWorker.Client = client
	if sharedPipelineScanWorker != nil {
		sharedPipelineScanWorker.Client = client
		pipelineEffects.client = client
	}
	if sourceRepublishScanWorker != nil {
		sourceRepublishScanWorker.Client = client
	}
	pgBossHandoffScanWorker.Client = client
	if cfg.SharedBodyEnabled && cfg.CollectorEnabled {
		sharedBodyScanWorker.Client = client
	}
	if storyDigestScanWorker != nil {
		storyDigestScanWorker.Client = client
	}
	if cfg.GoContentDeliveryEnabled {
		selectedNotifyWorker.Client = client
	}
	dailyScanWorker.Client = client
	sourceScanWorker.Client = client
	if sourceWorker != nil {
		sourceWorker.Client = client
	}
	sourceHealthScanWorker.Client = client
	sourceIconsScanWorker.Client = client
	if mpScanWorker != nil {
		mpScanWorker.Client = client
	}
	retentionScanWorker.Client = client
	operationsDigestScanWorker.Client = client
	if err := runTrackedTask(ctx, orm, "ops.queue.handoff", func() (any, error) {
		return ImportPendingPgBossJobs(ctx, pool, client, cfg.CollectorEnabled, cfg.CollectorEnabled && strings.TrimSpace(cfg.DajialaKey) != "", cfg.StoryDigestEnabled && cfg.ModelCallsEnabled)
	}); err != nil {
		return nil, err
	}
	return client, nil
}

func callJobError(err error) error {
	if errors.Is(err, errConnectionEncryptedKey) {
		return river.JobCancel(err)
	}
	var permanent *permanentJobError
	if errors.As(err, &permanent) {
		return river.JobCancel(permanent.cause)
	}
	var callErr *provider.Error
	if errors.As(err, &callErr) && !callErr.Retryable {
		return river.JobCancel(err)
	}
	return err
}

func stripFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```")
		if strings.HasPrefix(strings.ToLower(value), "json") {
			value = value[4:]
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, "```"))
	}
	return value
}

func modelCall(call provider.ChatFunc, ctx context.Context, cfg provider.Config, prompt string) (provider.Completion, error) {
	if call == nil {
		call = provider.Chat
	}
	return call(ctx, cfg, prompt)
}
