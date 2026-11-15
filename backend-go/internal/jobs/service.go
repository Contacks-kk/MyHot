package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/modelconfig"
	"aihot/backend-go/internal/provider"

	"github.com/jackc/pgx/v5"
)

const (
	defaultUserDailyModelCallLimit = 100
	coldStartSliceLimit            = 10
	perSourceUserRoundCap          = 25
	triageThreshold                = 5
	triageBatchSize                = 25
	triageTopK                     = 5
	triageSnippetRunes             = 180
)

var errConnectionEncryptedKey = errors.New("connection test key ciphertext cannot be decrypted")

type permanentJobError struct{ cause error }

func (e *permanentJobError) Error() string { return e.cause.Error() }
func (e *permanentJobError) Unwrap() error { return e.cause }

// Service coordinates private article processing, connection tests, and
// daily reports for both River workers and HTTP-triggered queue operations.
type Service struct {
	repository   *Repository
	config       config.Config
	chat         provider.ChatFunc
	entities     map[string]interests.Entity
	SharedModels *modelcall.Client
}

func NewService(repository *Repository, cfg config.Config, chat provider.ChatFunc) *Service {
	return &Service{repository: repository, config: cfg, chat: chat}
}

/** SetEntities configures the entity catalog used for Stage 1 interest matching. */
func (s *Service) SetEntities(entities map[string]interests.Entity) {
	s.entities = entities
}

type ArticleEnqueuer func(context.Context, pgx.Tx, ArticleArgs) error
type TriageEnqueuer func(context.Context, pgx.Tx, TriageBatchArgs) error
type TriageArticleEnqueuer func(context.Context, ArticleArgs) error
type DailyEnqueuer func(context.Context, DailyArgs) error

type userScanCandidate struct {
	articleID         string
	revision          int
	sourceID          string
	title             string
	excerpt           string
	body              string
	modelVersion      int
	preferenceVersion int
	matchedHints      []string
	coldStart         bool
	filterMode        string
}

type userScanBucket struct {
	items          []userScanCandidate
	seenArticles   map[string]bool
	perSourceCount map[string]int
}

/** Scan enqueues personal article jobs for eligible users. */
func (s *Service) Scan(ctx context.Context, enqueue ArticleEnqueuer) error {
	return s.ScanWithTriage(ctx, enqueue, nil)
}

/** ScanWithTriage scans unscanned incremental articles and cold-start subscriptions inside one transaction. */
func (s *Service) ScanWithTriage(ctx context.Context, enqueueArticle ArticleEnqueuer, enqueueTriage TriageEnqueuer) error {
	if err := s.repository.EnsureRollout(ctx); err != nil {
		return err
	}

	dayStartUTC, err := shanghaiDayStartUTC(time.Now())
	if err != nil {
		return err
	}

	return s.repository.WithPersonalScanLockTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		byUser := make(map[int64]*userScanBucket)
		appendCandidate := func(row scanUserArticle) {
			var hits []string
			if row.FilterMode == "all" {
				hits, _ = s.matchesUserInterests(row.Title, row.Excerpt, row.Body, row.Keywords, row.EntityIDs)
			} else {
				var ok bool
				hits, ok = s.matchesUserInterests(row.Title, row.Excerpt, row.Body, row.Keywords, row.EntityIDs)
				if !ok {
					return
				}
			}
			bucket := byUser[row.UserID]
			if bucket == nil {
				bucket = &userScanBucket{
					seenArticles:   make(map[string]bool),
					perSourceCount: make(map[string]int),
				}
				byUser[row.UserID] = bucket
			}
			if bucket.seenArticles[row.ArticleID] {
				return
			}
			if bucket.perSourceCount[row.SourceID] >= perSourceUserRoundCap {
				return
			}
			bucket.perSourceCount[row.SourceID]++
			bucket.seenArticles[row.ArticleID] = true
			bucket.items = append(bucket.items, userScanCandidate{
				articleID:         row.ArticleID,
				revision:          row.Revision,
				sourceID:          row.SourceID,
				title:             row.Title,
				excerpt:           row.Excerpt,
				body:              row.Body,
				modelVersion:      row.ModelVersion,
				preferenceVersion: row.PreferenceVersion,
				matchedHints:      hits,
				coldStart:         row.ColdStart,
				filterMode:        row.FilterMode,
			})
		}

		// Collect incremental unscanned articles first so live arrivals retain incremental semantics.
		articles, err := s.repository.UnscannedArticlesTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, article := range articles {
			candidates, err := s.repository.IncrementalCandidatesTx(ctx, tx, article)
			if err != nil {
				return err
			}
			for _, candidate := range candidates {
				appendCandidate(candidate)
			}
			if err := s.repository.MarkArticleScannedTx(ctx, tx, article); err != nil {
				return err
			}
		}

		// Slice up to 10 recent non-backfill articles for late or cold-start subscriptions.
		coldSubs, err := s.repository.ColdStartSubscriptionsTx(ctx, tx)
		if err != nil {
			return err
		}
		clearedSubIDs := make([]int64, 0, len(coldSubs))
		for _, sub := range coldSubs {
			sliceItems, err := s.repository.ColdStartArticlesTx(ctx, tx, sub, coldStartSliceLimit)
			if err != nil {
				return err
			}
			for _, candidate := range sliceItems {
				appendCandidate(candidate)
			}
			clearedSubIDs = append(clearedSubIDs, sub.SubscriptionID)
		}
		if err := s.repository.ClearColdStartPendingTx(ctx, tx, clearedSubIDs); err != nil {
			return err
		}

		userIDs := make([]int64, 0, len(byUser))
		for userID := range byUser {
			userIDs = append(userIDs, userID)
		}
		sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })

		dailyLimit := s.dailyCallLimit()
		for _, userID := range userIDs {
			used, err := s.repository.DailyModelCallCountTx(ctx, tx, userID, dayStartUTC)
			if err != nil {
				return err
			}
			remaining := dailyLimit - used
			if remaining <= 0 {
				continue
			}

			articleDispatches, triageDispatches := planUserDispatches(userID, byUser[userID].items, remaining, enqueueTriage != nil)
			if enqueueArticle != nil {
				for _, args := range articleDispatches {
					if err := enqueueArticle(ctx, tx, args); err != nil {
						return err
					}
				}
			}
			if enqueueTriage != nil {
				for _, args := range triageDispatches {
					if err := enqueueTriage(ctx, tx, args); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

/** ProcessTriageBatch runs one compact LLM triage call over <=25 candidates and enqueues up to Top 5 deep articles. */
func (s *Service) ProcessTriageBatch(ctx context.Context, args TriageBatchArgs, jobID int64, attempt int, enqueueArticle TriageArticleEnqueuer) error {
	if !s.config.PersonalizationEnabled || len(args.Items) == 0 {
		return nil
	}
	state, err := s.repository.TriageUserState(ctx, args.UserID, args.ModelVersion, args.PreferenceVersion)
	if errors.Is(err, errRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	articleIDs := make([]string, 0, len(args.Items))
	for _, item := range args.Items {
		articleIDs = append(articleIDs, item.ArticleID)
	}
	existing, err := s.repository.ExistingArticleResultIDs(ctx, args.UserID, articleIDs)
	if err != nil {
		return err
	}

	candidates := make([]TriageCandidate, 0, len(args.Items))
	for _, item := range args.Items {
		if existing[item.ArticleID] {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}

	dayStartUTC, err := shanghaiDayStartUTC(time.Now())
	if err != nil {
		return err
	}
	used, err := s.repository.DailyModelCallCount(ctx, args.UserID, dayStartUTC)
	if err != nil {
		return err
	}
	remaining := s.dailyCallLimit() - used
	if remaining <= 0 {
		return nil
	}

	// Skip the triage LLM call when remaining candidates fit under the threshold or only 1 quota slot remains.
	if len(candidates) <= triageThreshold || remaining == 1 {
		if enqueueArticle == nil {
			return nil
		}
		limit := min(len(candidates), remaining)
		for _, item := range candidates[:limit] {
			if err := enqueueArticle(ctx, ArticleArgs{
				UserID:            args.UserID,
				ArticleID:         item.ArticleID,
				Revision:          item.Revision,
				ModelVersion:      state.ModelVersion,
				PreferenceVersion: state.PreferenceVersion,
				ColdStart:         item.ColdStart,
				FilterMode:        item.FilterMode,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	maxPick := min(triageTopK, remaining-1)
	prompt := buildTriagePrompt(candidates, maxPick, state.Keywords, state.EntityIDs, s.entities)
	secret, err := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, state.EncryptedKey)
	if err != nil {
		return err
	}
	defer clear(secret)

	logical := fmt.Sprintf("triage:%d:%s:job:%d:attempt:%d", args.UserID, args.BatchKey, jobID, attempt)
	receipt, err := s.startQuotaReceipt(ctx, args.UserID, "article", "", state.Model, logical, prompt, dayStartUTC)
	if errors.Is(err, errDailyQuotaReached) {
		return nil
	}
	if err != nil {
		return err
	}

	out, callErr := modelCall(s.chat, ctx, provider.Config{BaseURL: state.BaseURL, APIKey: string(secret), Model: state.Model}, prompt)
	if callErr != nil {
		_ = s.finishReceipt(ctx, receipt, out.Usage, callErr)
		return callErr
	}
	picked, parseErr := parseTriageSelection(out.Text, len(candidates), maxPick)
	if parseErr != nil {
		return s.failReceipt(ctx, receipt, out.Usage, parseErr.Error())
	}
	if err := s.finishReceipt(ctx, receipt, out.Usage, nil); err != nil {
		return err
	}
	if enqueueArticle == nil {
		return nil
	}
	for _, oneBased := range picked {
		chosen := candidates[oneBased-1]
		if err := enqueueArticle(ctx, ArticleArgs{
			UserID:            args.UserID,
			ArticleID:         chosen.ArticleID,
			Revision:          chosen.Revision,
			ModelVersion:      state.ModelVersion,
			PreferenceVersion: state.PreferenceVersion,
			ColdStart:         chosen.ColdStart,
			FilterMode:        chosen.FilterMode,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ProcessArticle(ctx context.Context, args ArticleArgs, jobID int64, attempt int) error {
	if !s.config.PersonalizationEnabled {
		return nil
	}
	exists, err := s.repository.ArticleResultExists(ctx, args.UserID, args.ArticleID)
	if err != nil || exists {
		return err
	}
	input, err := s.repository.ArticleInput(ctx, args)
	if errors.Is(err, errRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	hits, matched := s.matchesUserInterests(input.Title, input.Excerpt, input.Body, input.Keywords, input.EntityIDs)
	if !matched && args.FilterMode != "all" {
		return nil
	}
	dayStartUTC, err := shanghaiDayStartUTC(time.Now())
	if err != nil {
		return err
	}

	body := truncateRunes(input.Body, 16000, "\n[内容已截断]")
	excerpt := truncateRunes(input.Excerpt, 3000, "")
	interestContext := ""
	if len(hits) > 0 {
		interestContext = "\n命中的用户关注项：" + strings.Join(hits, "、") + "。"
	}

	prompt := fmt.Sprintf(
		"你是用户的私人热点信息助手。"+
			"用户已关注这篇文章所属的信源。"+
			"只根据给出的文章事实生成 JSON，字段为 "+
			"relevanceScore（0 到 100 的整数，表示文章的信息价值）、"+
			"title（中文简洁标题）、"+
			"summary（中文摘要，80 到 220 字）、"+
			"reason（说明评分依据）。"+
			"不要编造事实，不要声称命中了用户未设置的关键词或公司偏好。%s"+
			"\n\n文章标题：%s"+
			"\n来源正文摘要：%s"+
			"\n文章正文：%s"+
			"\n原文链接：%s",
		interestContext,
		input.Title,
		excerpt,
		body,
		input.URL,
	)
	secret, err := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, input.EncryptedKey)
	if err != nil {
		return err
	}
	defer clear(secret)
	logical := fmt.Sprintf("article:%d:%s:job:%d:attempt:%d", args.UserID, args.ArticleID, jobID, attempt)
	receipt, err := s.startQuotaReceipt(ctx, args.UserID, "article", args.ArticleID, input.Model, logical, prompt, dayStartUTC)
	if errors.Is(err, errDailyQuotaReached) {
		return nil
	}
	if err != nil {
		return err
	}
	out, callErr := modelCall(s.chat, ctx, provider.Config{BaseURL: input.BaseURL, APIKey: string(secret), Model: input.Model}, prompt)
	if callErr != nil {
		_ = s.finishReceipt(ctx, receipt, out.Usage, callErr)
		return callErr
	}
	receiptFinished := false
	defer func() {
		if !receiptFinished {
			_ = s.finishReceipt(ctx, receipt, out.Usage, errors.New("模型调用完成，但结果持久化未完成"))
		}
	}()
	var output struct {
		Score   *int   `json:"relevanceScore"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Reason  string `json:"reason"`
	}
	if err = json.Unmarshal([]byte(stripFence(out.Text)), &output); err != nil {
		receiptFinished = true
		return s.failReceipt(ctx, receipt, out.Usage, "模型输出不是有效 JSON")
	}
	output.Title = strings.TrimSpace(output.Title)
	output.Summary = strings.TrimSpace(output.Summary)
	output.Reason = strings.TrimSpace(output.Reason)
	if output.Score == nil || *output.Score < 0 || *output.Score > 100 || output.Title == "" || output.Summary == "" || len([]rune(output.Title)) > 160 || len([]rune(output.Summary)) > 3000 || len([]rune(output.Reason)) > 1000 {
		receiptFinished = true
		return s.failReceipt(ctx, receipt, out.Usage, "模型输出字段无效")
	}
	var reason *string
	if output.Reason != "" {
		reason = &output.Reason
	}
	result := model.UserArticleResult{UserID: args.UserID, ArticleID: args.ArticleID, InputRevision: input.Revision, Model: input.Model, RelevanceScore: *output.Score, Title: output.Title, Summary: output.Summary, Reason: reason}
	usage, _ := json.Marshal(out.Usage)
	if err = s.repository.SaveArticleResultAndReceipt(ctx, result, receipt, usage); err != nil {
		return err
	}
	receiptFinished = true
	return nil
}

func (s *Service) ScheduleDailyReports(ctx context.Context, enqueue DailyEnqueuer) error {
	local, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	now := time.Now().In(local)
	if now.Hour() < 8 {
		return nil
	}
	date := now.Format("2006-01-02")
	start, end, err := dailyWindow(date)
	if err != nil {
		return err
	}
	ids, err := s.repository.DailyUsers(ctx, date, start, end)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := enqueue(ctx, DailyArgs{UserID: id, Date: date}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ProcessDailyReport(ctx context.Context, args DailyArgs, jobID int64, attempt int) error {
	if !s.config.PersonalizationEnabled {
		return nil
	}
	start, end, err := dailyWindow(args.Date)
	if err != nil {
		return &permanentJobError{cause: err}
	}
	already, err := s.repository.DailyReportExists(ctx, args.UserID, args.Date)
	if err != nil || already {
		return err
	}
	return s.generateDailyReport(ctx, args.UserID, args.Date, jobID, attempt, start, end, false)
}

/** ProcessManualDailyReport generates an issue covering results since the previous issue. */
func (s *Service) ProcessManualDailyReport(ctx context.Context, userID, jobID int64, attempt int, end time.Time) error {
	if !s.config.PersonalizationEnabled || !s.config.ModelCallsEnabled {
		return errors.New("日报生成功能当前已关闭")
	}
	start, err := s.repository.ManualDailyStart(ctx, userID, end)
	if err != nil {
		return err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	return s.generateDailyReport(ctx, userID, end.In(location).Format("2006-01-02"), jobID, attempt, start, end, true)
}

/** generateDailyReport summarizes a fixed interval using the user's configured model. */
func (s *Service) generateDailyReport(ctx context.Context, userID int64, reportDate string, jobID int64, attempt int, start, end time.Time, manual bool) error {
	config, err := s.repository.DailyModel(ctx, userID)
	if errors.Is(err, errRecordNotFound) {
		if manual {
			return errors.New("请先配置个人模型")
		}
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := s.repository.DailyEntries(ctx, userID, start, end)
	if err != nil || len(entries) == 0 {
		return err
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return errors.New("日报内容无法编码")
	}
	input, inputErr := dailyInput(entries)
	secret, err := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, config.EncryptedKey)
	if err != nil {
		return err
	}
	defer clear(secret)
	if manual && inputErr != nil {
		input, inputErr = s.compactManualDaily(ctx, userID, jobID, attempt, config, secret, entries)
	}
	if inputErr != nil {
		return &permanentJobError{cause: inputErr}
	}
	prompt := "根据以下用户个人热点结果，写一份中文日报。只总结输入内容，不编造事实。只返回 JSON，字段为 title 和 summary（分主题分点，简洁）。\n\n" + string(input)
	logical := fmt.Sprintf("daily:%d:%s:job:%d:attempt:%d", userID, reportDate, jobID, attempt)
	receipt, err := s.startReceipt(ctx, userID, "daily", "", config.Model, logical, prompt)
	if err != nil {
		return err
	}
	out, callErr := modelCall(s.chat, ctx, provider.Config{BaseURL: config.BaseURL, APIKey: string(secret), Model: config.Model}, prompt)
	if callErr != nil {
		_ = s.finishReceipt(ctx, receipt, out.Usage, callErr)
		return callErr
	}
	receiptFinished := false
	defer func() {
		if !receiptFinished {
			_ = s.finishReceipt(ctx, receipt, out.Usage, errors.New("模型调用完成，但结果持久化未完成"))
		}
	}()
	var output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err = json.Unmarshal([]byte(stripFence(out.Text)), &output); err != nil {
		receiptFinished = true
		return s.failReceipt(ctx, receipt, out.Usage, "模型输出不是有效 JSON")
	}
	output.Title = strings.TrimSpace(output.Title)
	output.Summary = strings.TrimSpace(output.Summary)
	if output.Title == "" || output.Summary == "" || len([]rune(output.Title)) > 200 || len([]rune(output.Summary)) > 8000 {
		receiptFinished = true
		return s.failReceipt(ctx, receipt, out.Usage, "模型输出字段无效")
	}
	usage, _ := json.Marshal(out.Usage)
	date, _ := time.Parse("2006-01-02", reportDate)
	report := model.UserDailyReport{UserID: userID, ReportDate: date, Model: config.Model, Title: output.Title, Summary: output.Summary, Entries: body}
	if manual {
		issue, marshalErr := json.Marshal(map[string]any{
			"id":        fmt.Sprintf("manual-%d", jobID),
			"date":      reportDate,
			"model":     config.Model,
			"title":     output.Title,
			"summary":   output.Summary,
			"entries":   json.RawMessage(body),
			"createdAt": time.Now().UTC(),
			"windowEnd": end.UTC(),
		})
		if marshalErr != nil {
			return marshalErr
		}
		err = s.repository.SaveManualDailyReportAndReceipt(ctx, report, issue, receipt, usage)
	} else {
		err = s.repository.SaveDailyReportAndReceipt(ctx, report, receipt, usage)
	}
	if err != nil {
		return err
	}
	receiptFinished = true
	return nil
}

/** compactManualDaily summarizes large histories in bounded batches before the final report. */
func (s *Service) compactManualDaily(ctx context.Context, userID, jobID int64, attempt int, config dailyModel, secret []byte, entries []dailyEntry) ([]byte, error) {
	sequence := 0
	for {
		var condensed []dailyEntry
		for offset := 0; offset < len(entries); {
			batchEnd := min(offset+100, len(entries))
			input, err := dailyInput(entries[offset:batchEnd])
			for err != nil && batchEnd-offset > 1 {
				batchEnd = offset + (batchEnd-offset)/2
				input, err = dailyInput(entries[offset:batchEnd])
			}
			if err != nil {
				return nil, err
			}
			prompt := "整理以下个人热点，保留重要事实供最终日报汇总。不要编造，只返回 JSON，字段为 title 和 summary。\n\n" + string(input)
			logical := fmt.Sprintf("daily-batch:%d:job:%d:attempt:%d:part:%d", userID, jobID, attempt, sequence)
			receipt, err := s.startReceipt(ctx, userID, "daily", "", config.Model, logical, prompt)
			if err != nil {
				return nil, err
			}
			out, err := modelCall(s.chat, ctx, provider.Config{BaseURL: config.BaseURL, APIKey: string(secret), Model: config.Model}, prompt)
			if err != nil {
				_ = s.finishReceipt(ctx, receipt, out.Usage, err)
				return nil, err
			}
			var result struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(stripFence(out.Text)), &result); err != nil || strings.TrimSpace(result.Title) == "" || strings.TrimSpace(result.Summary) == "" || len([]rune(result.Title)) > 200 || len([]rune(result.Summary)) > 8000 {
				return nil, s.failReceipt(ctx, receipt, out.Usage, "日报分批整理结果无效")
			}
			if err := s.finishReceipt(ctx, receipt, out.Usage, nil); err != nil {
				return nil, err
			}
			condensed = append(condensed, dailyEntry{ArticleID: fmt.Sprintf("batch-%d", sequence), Title: strings.TrimSpace(result.Title), Summary: strings.TrimSpace(result.Summary)})
			sequence++
			offset = batchEnd
		}
		input, err := dailyInput(condensed)
		if err == nil {
			return input, nil
		}
		entries = condensed
	}
}

func (s *Service) ProcessConnectionTest(ctx context.Context, args ConnectionTestArgs) error {
	status, err := s.repository.ConnectionReceiptStatus(ctx, args.UserID, args.ReceiptID)
	if errors.Is(err, errRecordNotFound) {
		return nil
	}
	if err != nil || status != "pending" {
		return err
	}
	secret, err := modelconfig.Decrypt(s.config.UserAPIKeyEncryptionKey, args.APIKeyCiphertext)
	if err != nil {
		_ = s.finishReceipt(ctx, args.ReceiptID, provider.Usage{}, errors.New("模型密钥配置无法解密"))
		return errConnectionEncryptedKey
	}
	defer clear(secret)
	out, callErr := modelCall(s.chat, ctx, provider.Config{BaseURL: args.BaseURL, Model: args.Model, APIKey: string(secret)}, ConnectionTestPrompt)
	if err := s.finishReceipt(ctx, args.ReceiptID, out.Usage, callErr); err != nil {
		return err
	}
	return callErr
}

func (s *Service) startReceipt(ctx context.Context, userID int64, purpose, articleID, modelName, logical, prompt string) (int64, error) {
	sum := sha256.Sum256([]byte(prompt))
	return s.repository.StartReceipt(ctx, userID, purpose, articleID, modelName, logical, hex.EncodeToString(sum[:]))
}

func (s *Service) finishReceipt(ctx context.Context, id int64, usage provider.Usage, err error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(usage)
	return s.repository.FinishReceipt(ctx, id, raw, err)
}

func (s *Service) failReceipt(ctx context.Context, id int64, usage provider.Usage, message string) error {
	err := errors.New(message)
	_ = s.finishReceipt(ctx, id, usage, err)
	return err
}

func truncateRunes(value string, limit int, suffix string) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + suffix
	}
	return value
}

func (s *Service) dailyCallLimit() int {
	if s.config.UserDailyModelCallLimit <= 0 {
		return defaultUserDailyModelCallLimit
	}
	return s.config.UserDailyModelCallLimit
}

func (s *Service) startQuotaReceipt(ctx context.Context, userID int64, purpose, articleID, modelName, logical, prompt string, dayStartUTC time.Time) (int64, error) {
	sum := sha256.Sum256([]byte(prompt))
	return s.repository.StartReceiptWithQuota(ctx, userID, purpose, articleID, modelName, logical, hex.EncodeToString(sum[:]), dayStartUTC, s.dailyCallLimit())
}

/** matchesUserInterests runs Stage 1 local keyword/entity matching, allowing empty preferences into triage. */
func (s *Service) matchesUserInterests(title, excerpt, body string, keywords, entityIDs []string) ([]string, bool) {
	if !hasConfiguredInterests(keywords, entityIDs) {
		return nil, true
	}
	combinedBody := strings.TrimSpace(excerpt + "\n" + body)
	hits := interests.Match(title, combinedBody, keywords, entityIDs, s.entities)
	return hits, len(hits) > 0
}

func hasConfiguredInterests(keywords, entityIDs []string) bool {
	for _, k := range keywords {
		if strings.TrimSpace(k) != "" {
			return true
		}
	}
	for _, id := range entityIDs {
		if strings.TrimSpace(id) != "" {
			return true
		}
	}
	return false
}

/** planUserDispatches partitions surviving user candidates into direct Stage 3 articles or Stage 2 triage batches. */
func planUserDispatches(userID int64, items []userScanCandidate, remaining int, allowTriage bool) ([]ArticleArgs, []TriageBatchArgs) {
	if len(items) == 0 || remaining <= 0 {
		return nil, nil
	}

	var articles []ArticleArgs
	var triages []TriageBatchArgs
	toArticleArgs := func(item userScanCandidate) ArticleArgs {
		return ArticleArgs{
			UserID:            userID,
			ArticleID:         item.articleID,
			Revision:          item.revision,
			ModelVersion:      item.modelVersion,
			PreferenceVersion: item.preferenceVersion,
			ColdStart:         item.coldStart,
			FilterMode:        item.filterMode,
		}
	}

	if !allowTriage || len(items) <= triageThreshold || remaining == 1 {
		limit := min(len(items), remaining)
		if !allowTriage && len(items) > triageTopK {
			limit = min(limit, triageTopK)
		}
		for _, item := range items[:limit] {
			articles = append(articles, toArticleArgs(item))
		}
		return articles, nil
	}

	for start := 0; start < len(items) && remaining > 0; start += triageBatchSize {
		end := min(start+triageBatchSize, len(items))
		chunk := items[start:end]
		if len(chunk) <= triageThreshold || remaining == 1 {
			limit := min(len(chunk), remaining)
			for _, item := range chunk[:limit] {
				articles = append(articles, toArticleArgs(item))
			}
			remaining -= limit
			continue
		}

		triageItems := make([]TriageCandidate, 0, len(chunk))
		for _, item := range chunk {
			triageItems = append(triageItems, TriageCandidate{
				ArticleID:    item.articleID,
				Revision:     item.revision,
				SourceID:     item.sourceID,
				Title:        item.title,
				Snippet:      compactSnippet(item.excerpt, item.body, triageSnippetRunes),
				MatchedHints: item.matchedHints,
				ColdStart:    item.coldStart,
				FilterMode:   item.filterMode,
			})
		}
		modelVersion := chunk[0].modelVersion
		prefVersion := chunk[0].preferenceVersion
		triages = append(triages, TriageBatchArgs{
			UserID:            userID,
			ModelVersion:      modelVersion,
			PreferenceVersion: prefVersion,
			BatchKey:          triageBatchKey(userID, modelVersion, prefVersion, triageItems),
			Items:             triageItems,
		})
		budgetCost := 1 + min(triageTopK, remaining-1)
		remaining -= budgetCost
	}
	return articles, triages
}

func triageBatchKey(userID int64, modelVersion, prefVersion int, items []TriageCandidate) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s:%d", item.ArticleID, item.Revision))
	}
	sort.Strings(parts)
	payload := fmt.Sprintf("%d|%d|%d|%s", userID, modelVersion, prefVersion, strings.Join(parts, ","))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16])
}

func compactSnippet(excerpt, body string, limit int) string {
	text := strings.TrimSpace(excerpt)
	if text == "" {
		text = strings.TrimSpace(body)
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return ""
	}
	runes := []rune(collapsed)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return collapsed
}

func buildTriagePrompt(candidates []TriageCandidate, maxPick int, keywords, entityIDs []string, entities map[string]interests.Entity) string {
	var interestTerms []string
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" {
			interestTerms = append(interestTerms, k)
		}
	}
	for _, id := range entityIDs {
		if entity, ok := entities[id]; ok && strings.TrimSpace(entity.Name) != "" {
			interestTerms = append(interestTerms, entity.Name)
		} else if id = strings.TrimSpace(id); id != "" {
			interestTerms = append(interestTerms, id)
		}
	}
	interestLine := ""
	if len(interestTerms) > 0 {
		interestLine = "\n用户关注主题：" + strings.Join(interestTerms, "、")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"你是用户的私人热点信息初筛助手。\n"+
			"请从以下编号 1 到 %d 的候选文章中，挑选最值得精读的最多 %d 篇。\n"+
			"筛选原则：优先保留重要技术/产品发布、关键融资并购、重大监管政策、深度分析或与用户关注主题强相关的文章；剔除广告、招聘、纯搬运水文或重复信息。%s\n"+
			"只返回严格 JSON，格式为 {\"selected\":[编号数组]}，编号为 1 到 %d 的不重复整数，按推荐优先级从高到低排序。\n\n候选列表：\n",
		len(candidates),
		maxPick,
		interestLine,
		len(candidates),
	))
	for i, item := range candidates {
		sb.WriteString(fmt.Sprintf("%d. 标题：%s\n", i+1, item.Title))
		if item.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   摘要：%s\n", item.Snippet))
		}
		if len(item.MatchedHints) > 0 {
			sb.WriteString(fmt.Sprintf("   命中：%s\n", strings.Join(item.MatchedHints, "、")))
		}
	}
	return sb.String()
}

type triageSelectionResponse struct {
	Selected []int `json:"selected"`
}

func parseTriageSelection(raw string, candidateCount, maxPick int) ([]int, error) {
	var parsed triageSelectionResponse
	if err := json.Unmarshal([]byte(stripFence(raw)), &parsed); err != nil {
		return nil, errors.New("模型初筛输出不是有效 JSON")
	}
	if parsed.Selected == nil {
		return nil, errors.New("模型初筛输出缺少 selected 字段")
	}
	if len(parsed.Selected) == 0 {
		return []int{}, nil
	}
	seen := make(map[int]struct{}, len(parsed.Selected))
	picked := make([]int, 0, min(len(parsed.Selected), maxPick))
	for _, oneBased := range parsed.Selected {
		if oneBased < 1 || oneBased > candidateCount {
			continue
		}
		if _, exists := seen[oneBased]; exists {
			continue
		}
		seen[oneBased] = struct{}{}
		picked = append(picked, oneBased)
		if len(picked) >= maxPick {
			break
		}
	}
	if len(picked) == 0 {
		return nil, errors.New("模型初筛未返回有效候选编号")
	}
	return picked, nil
}

func shanghaiDayStartUTC(now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, err
	}
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return midnight.UTC(), nil
}
