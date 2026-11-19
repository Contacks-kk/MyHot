package operationsalerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/sourcehealth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const autoReleaseNote = "自动放行：结果未知超过 30 分钟，未核对是否计费"

type GormRepository struct{ db *gorm.DB }

type backupSnapshot struct {
	At       string `json:"at"`
	Uploaded *bool  `json:"uploaded"`
	FilesErr string `json:"filesError"`
}

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) LoadState(ctx context.Context) (State, error) {
	var setting model.AppSetting
	result := r.db.WithContext(ctx).Select("value").First(&setting, "key = ?", "alerts.state")
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return State{}, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	state := State{}
	if len(setting.Value) != 0 {
		if err := json.Unmarshal(setting.Value, &state); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (r *GormRepository) SaveState(ctx context.Context, state State) error {
	if state == nil {
		state = State{}
	}
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	updatedBy := "alerts"
	setting := model.AppSetting{Key: "alerts.state", Value: value, UpdatedBy: &updatedBy}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{
		"value": value, "updated_at": gorm.Expr("now()"),
	})}).Create(&setting).Error
}

func (r *GormRepository) Findings(ctx context.Context, now time.Time, cfg Config) ([]Finding, error) {
	findings := make([]Finding, 0)
	if cfg.CollectEnabled {
		items, err := r.collectionFindings(ctx, now, cfg)
		if err != nil {
			return nil, err
		}
		findings = append(findings, items...)
		cutoff := now.Add(-7 * 24 * time.Hour)
		weekly, err := sourcehealth.NewRepository(r.db).Weekly(ctx, cutoff, cutoff.Add(-7*24*time.Hour))
		if err != nil {
			return nil, err
		}
		findings = append(findings, sourceHealthFindings(weekly.Sources, cutoff)...)
	}
	items, err := r.providerFindings(ctx)
	if err != nil {
		return nil, err
	}
	findings = append(findings, items...)
	items, err = r.deliveryFindings(ctx)
	if err != nil {
		return nil, err
	}
	findings = append(findings, items...)
	items, err = r.backupFindings(ctx, now, cfg)
	if err != nil {
		return nil, err
	}
	findings = append(findings, items...)
	return findings, nil
}

/** sourceHealthFindings retains daily follow-ups for distinct collection uses. */
func sourceHealthFindings(sources []sourcehealth.Source, cutoff time.Time) []Finding {
	groups := []struct {
		mode string
		name string
	}{
		{mode: "editorial", name: "编辑内容"},
		{mode: "hot_signal", name: "热度信号"},
	}
	findings := make([]Finding, 0)
	for _, group := range groups {
		matching := make([]sourcehealth.Source, 0)
		for _, source := range sources {
			if source.Mode == group.mode {
				matching = append(matching, source)
			}
		}
		failing := make([]sourcehealth.Source, 0)
		unstable := make([]sourcehealth.Source, 0)
		silent := make([]sourcehealth.Source, 0)
		quality := make([]sourcehealth.Source, 0)
		detailFailures := make([]sourcehealth.Source, 0)
		for _, source := range matching {
			if source.Health == "failing" {
				failing = append(failing, source)
			}
			if source.Failed >= 5 && source.Runs > 0 && float64(source.Failed)/float64(source.Runs) >= 0.1 {
				unstable = append(unstable, source)
			}
			if source.CreatedAt.Before(cutoff) && source.Discoveries == 0 {
				silent = append(silent, source)
			}
			if group.mode == "editorial" && (source.Undated > 0 || source.Repeated > 0) {
				quality = append(quality, source)
			}
			if source.DetailFailures > 0 {
				detailFailures = append(detailFailures, source)
			}
		}
		if len(failing) > 0 {
			findings = append(findings, Finding{
				Key:   "sources.failing." + group.mode,
				Level: LevelToday,
				Title: fmt.Sprintf("%s有 %d 个信源连续抓取失败", group.name, len(failing)),
				Detail: sourceHealthList(failing, func(source sourcehealth.Source) string {
					detail := fmt.Sprintf("连续失败 %d 次", source.FailCount)
					if source.LastError != nil && *source.LastError != "" {
						detail += "，" + *source.LastError
					}
					return detail
				}) + "；转给 AI 检查抓取错误",
			})
		}
		if len(unstable) > 0 {
			findings = append(findings, Finding{
				Key:   "sources.unstable." + group.mode,
				Level: LevelToday,
				Title: fmt.Sprintf("%s有 %d 个信源反复抓取失败", group.name, len(unstable)),
				Detail: sourceHealthList(unstable, func(source sourcehealth.Source) string {
					return fmt.Sprintf("近 7 天失败 %d/%d 次", source.Failed, source.Runs)
				}) + "；即使最近成功也需检查，避免继续漏收",
			})
		}
		if len(silent) > 0 {
			findings = append(findings, Finding{
				Key:   "sources.silent." + group.mode,
				Level: LevelToday,
				Title: fmt.Sprintf("%s有 %d 个信源 7 天未发现新内容", group.name, len(silent)),
				Detail: sourceHealthList(silent, func(source sourcehealth.Source) string {
					return fmt.Sprintf("近 7 天 %d 次抓取、0 条新发现", source.Runs)
				}) + "；需对照原站，区分低频更新与采集失效",
			})
		}
		if len(quality) > 0 {
			findings = append(findings, Finding{
				Key:   "sources.quality." + group.mode,
				Level: LevelToday,
				Title: fmt.Sprintf("%s有 %d 个信源需要核实文章质量", group.name, len(quality)),
				Detail: sourceHealthList(quality, func(source sourcehealth.Source) string {
					return fmt.Sprintf("近 7 天缺发布时间 %d 篇、反复修订 %d 篇", source.Undated, source.Repeated)
				}) + "；缺时间可能让新闻按历史文章处理，反复修订需核对正文是否混入变化内容",
			})
		}
		if len(detailFailures) > 0 {
			findings = append(findings, Finding{
				Key:   "sources.details." + group.mode,
				Level: LevelToday,
				Title: fmt.Sprintf("%s有 %d 个信源详情补全失败", group.name, len(detailFailures)),
				Detail: sourceHealthList(detailFailures, func(source sourcehealth.Source) string {
					return fmt.Sprintf("近 7 天 %d 次", source.DetailFailures)
				}) + "；查看抓取记录中的详情地址与错误",
			})
		}
	}
	return findings
}

/** sourceHealthList bounds source details while retaining the total count. */
func sourceHealthList(sources []sourcehealth.Source, describe func(sourcehealth.Source) string) string {
	limit := min(10, len(sources))
	items := make([]string, 0, limit)
	for _, source := range sources[:limit] {
		items = append(items, fmt.Sprintf("%s（%s）：%s", source.Name, source.ID, describe(source)))
	}
	detail := strings.Join(items, "；")
	if len(sources) > limit {
		detail += fmt.Sprintf("；另有 %d 个，见信源周报与后台信源列表", len(sources)-limit)
	}
	return detail
}

func (r *GormRepository) collectionFindings(ctx context.Context, now time.Time, cfg Config) ([]Finding, error) {
	var heartbeat struct {
		StartedAt *time.Time `gorm:"column:started_at"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT (value->>'startedAt')::timestamptz AS started_at FROM settings WHERE key = 'heartbeat.worker'`).Scan(&heartbeat).Error; err != nil {
		return nil, err
	}
	settled := heartbeat.StartedAt == nil || now.Sub(*heartbeat.StartedAt) > 20*time.Minute
	findings := make([]Finding, 0, 4)
	if settled {
		var collected struct {
			At *time.Time `gorm:"column:at"`
		}
		if err := r.db.WithContext(ctx).Raw(`SELECT max(a.discovered_at) AS at FROM articles a JOIN sources s ON s.id = a.source_id WHERE s.participation_mode = 'editorial' AND a.discovered_at > ?`, now.Add(-24*time.Hour)).Scan(&collected).Error; err != nil {
			return nil, err
		}
		if collected.At == nil || now.Sub(*collected.At) > time.Duration(cfg.QuietMinutes)*time.Minute {
			var sourceExists bool
			if err := r.db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM sources WHERE enabled AND participation_mode = 'editorial')`).Scan(&sourceExists).Error; err != nil {
				return nil, err
			}
			if sourceExists {
				impact := "一天内没有收录任何编辑内容"
				var since *time.Time
				if collected.At != nil {
					impact = "最后一篇编辑内容收录于 " + beijingStamp(*collected.At) + "，之后没有新文章进入处理"
					since = collected.At
				}
				findings = append(findings, Finding{Key: "content.collect", Level: LevelNow, Title: "网站停止收录新文章", Impact: impact, Heals: "没有", Action: "转给 AI 立即处理", Detail: fmt.Sprintf("editorial articles.discovered_at 超过 %d 分钟没有新值（热度信号单独评估）；查 sources.schedule、出网代理与采集失败", cfg.QuietMinutes), Since: since})
			}
		}
	}
	if !settled || !cfg.ModelCallsEnabled {
		return findings, nil
	}
	waiting, err := r.waitingGrouping(ctx, now)
	if err != nil {
		return nil, err
	}
	if len(waiting) > 0 {
		manual, receipt := 0, false
		for _, item := range waiting {
			if item.Recovery == "manual" {
				manual++
			}
			receipt = receipt || item.Recovery == "receipt"
		}
		heals, action := "还会自动重试，但等待已经超过正常范围", "转给 AI 检查等待原因，避免继续积压"
		if manual > 0 {
			heals = fmt.Sprintf("%d 条需要人工处理后才能恢复", manual)
			if manual < len(waiting) {
				heals += "；其余仍在自动恢复"
			}
			action = "转给 AI 处理；结果未知的付费请求需先核对计费与结果，再决定是否放行"
		} else if receipt {
			heals = "仍在等待自动恢复；付费结果未知的请求会在 30 分钟后自动放行一次"
		}
		parts := make([]string, 0, min(8, len(waiting)))
		for _, item := range waiting[:min(8, len(waiting))] {
			part := item.ArticleID + "（" + duration(now.Sub(item.Since)) + "）"
			if item.ReceiptID != nil {
				part += fmt.Sprintf(" 回执 #%d", *item.ReceiptID)
			}
			if item.Error != "" {
				part += "：" + truncateRunes(item.Error, 120)
			}
			parts = append(parts, part)
		}
		since := waiting[0].Since
		findings = append(findings, Finding{Key: "content.grouping", Level: LevelNow, Title: "有精选新闻卡在去重确认，暂未发布", Impact: fmt.Sprintf("%d 条已达到精选条件的新闻等待确认超过 10 分钟，还没进入精选，读者可能晚看到这些更新", len(waiting)), Heals: heals, Action: action, Detail: strings.Join(parts, "；"), Since: &since})
	}
	var backlog struct {
		Waiting int        `gorm:"column:waiting"`
		Oldest  *time.Time `gorm:"column:oldest"`
		Failed  int        `gorm:"column:failed"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT count(*) FILTER (WHERE processing_state = 'new' AND discovered_at < ?)::int AS waiting, min(discovered_at) FILTER (WHERE processing_state = 'new') AS oldest, count(*) FILTER (WHERE processing_state = 'failed' AND discovered_at > ?)::int AS failed FROM articles WHERE processing_state IN ('new','failed') AND discovered_at > ?`, now.Add(-2*time.Hour), now.Add(-3*time.Hour), now.Add(-48*time.Hour)).Scan(&backlog).Error; err != nil {
		return nil, err
	}
	if backlog.Waiting >= 10 || backlog.Failed >= 20 {
		var errors []struct {
			Error string `gorm:"column:error"`
			Count int    `gorm:"column:count"`
		}
		if err := r.db.WithContext(ctx).Raw(`SELECT left(coalesce(processing_error, '（无）'), 120) AS error, count(*)::int AS count FROM articles WHERE processing_state IN ('new','failed') AND discovered_at > ? AND processing_error IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 3`, now.Add(-3*time.Hour)).Scan(&errors).Error; err != nil {
			return nil, err
		}
		impact := make([]string, 0, 2)
		if backlog.Waiting >= 10 {
			impact = append(impact, fmt.Sprintf("%d 篇新文章等了 2 小时以上还没处理完", backlog.Waiting))
		}
		if backlog.Failed >= 20 {
			impact = append(impact, fmt.Sprintf("最近 3 小时 %d 篇新文章处理失败", backlog.Failed))
		}
		items := make([]string, 0, len(errors))
		for _, item := range errors {
			items = append(items, fmt.Sprintf("%s（%d）", item.Error, item.Count))
		}
		heals := "不会，需要修好后重新处理这些文章"
		if backlog.Waiting >= 10 {
			heals = "服务恢复后会自动补处理"
		}
		findings = append(findings, Finding{Key: "content.process", Level: LevelNow, Title: "新内容卡住了，进不了网站", Impact: strings.Join(impact, "；") + "，精选和热点会缺内容", Heals: heals, Action: "转给 AI 处理", Detail: fallback(strings.Join(items, "；"), "没有记录错误"), Since: backlog.Oldest})
	}
	if due, date := overdueDaily(now, cfg.DailyEdition); due {
		var exists bool
		if err := r.db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM reports WHERE kind = 'daily' AND key = ?)`, date).Scan(&exists).Error; err != nil {
			return nil, err
		}
		if !exists {
			findings = append(findings, Finding{Key: "report.daily", Level: LevelNow, Title: "今天的日报还没生成", Impact: "读者看不到今天的日报", Heals: "系统每半小时补做一次，到现在还没成功", Action: "转给 AI 处理", Detail: "reports daily " + date + " 不存在；看 reports.compose 的运行记录"})
		}
	}
	return findings, nil
}

type groupingItem struct {
	ArticleID string    `gorm:"column:article_id"`
	Since     time.Time `gorm:"column:since"`
	Failed    bool      `gorm:"column:failed"`
	ReceiptID *int64    `gorm:"column:receipt_id"`
	Error     string    `gorm:"column:error"`
	Receipt   *string   `gorm:"column:receipt_status"`
	Running   bool      `gorm:"column:running"`
	Released  bool      `gorm:"column:released"`
	Recovery  string    `gorm:"column:recovery"`
}

func (r *GormRepository) waitingGrouping(ctx context.Context, now time.Time) ([]groupingItem, error) {
	var items []groupingItem
	err := r.db.WithContext(ctx).Raw(`
		SELECT p.article_id, greatest(coalesce(p.selected_ready_at, p.discovered_at), revision.created_at,
		       (SELECT max(j.created_on) FROM pgboss.job j WHERE j.name = 'events.group' AND j.data->>'articleId' = a.id
		        AND j.singleton_key LIKE 'manual:group:%')) AS since,
		       a.grouping_status = 'failed' AS failed, a.grouping_receipt_id AS receipt_id, a.grouping_error AS error,
		       r.status AS receipt_status,
		       EXISTS (SELECT 1 FROM pgboss.job j WHERE j.name = 'events.group' AND j.state IN ('created','retry','active') AND j.data->>'articleId' = a.id) AS running,
	       EXISTS (SELECT 1 FROM receipt_attempts attempt WHERE attempt.receipt_id = r.id AND attempt.error LIKE ?) AS released
		FROM publications p JOIN articles a ON a.id = p.article_id LEFT JOIN receipts r ON r.id = a.grouping_receipt_id
		LEFT JOIN article_revisions revision ON revision.article_id = a.id AND revision.revision = a.revision
		WHERE p.selection_candidate AND p.eligible AND p.visibility = 'public' AND a.grouping_status <> 'complete'
		ORDER BY since, p.article_id`, autoReleaseNote+"%").Scan(&items).Error
	if err != nil {
		return nil, err
	}
	filtered := make([]groupingItem, 0, len(items))
	for i := range items {
		item := &items[i]
		if now.Sub(item.Since) < 10*time.Minute {
			continue
		}
		switch {
		case item.Receipt != nil && *item.Receipt == "unknown" && item.Released:
			item.Recovery = "manual"
		case item.Receipt != nil && *item.Receipt == "unknown":
			item.Recovery = "receipt"
		case !item.Failed || item.Running || item.Receipt != nil && *item.Receipt == "pending":
			item.Recovery = "automatic"
		default:
			item.Recovery = "manual"
		}
		filtered = append(filtered, *item)
	}
	return filtered, nil
}

func (r *GormRepository) providerFindings(ctx context.Context) ([]Finding, error) {
	var refused []struct {
		Service string `gorm:"column:service"`
		Count   int    `gorm:"column:count"`
		Last    string `gorm:"column:last"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT service, count(*)::int AS count, (array_agg(left(error,200) ORDER BY started_at DESC))[1] AS last FROM receipt_attempts WHERE status = 'failed' AND started_at > now() - interval '1 hour' AND error ~* '(HTTP 40[123]\M|insufficient|balance|arrear|good standing|欠费|余额)' GROUP BY 1 HAVING count(*) >= 3`).Scan(&refused).Error; err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, len(refused))
	for _, item := range refused {
		name, stops, console := providerSummary(item.Service)
		findings = append(findings, Finding{Key: "provider.refused." + item.Service, Level: LevelToday, Title: name + " 拒绝服务，可能欠费或账号失效", Impact: stops, Heals: "不会", Action: "去" + console + "看余额和账号状态；充值或恢复后系统会自动继续", Detail: fmt.Sprintf("最近 1 小时被拒 %d 次：%s", item.Count, item.Last)})
	}
	var capped []struct {
		Service string `gorm:"column:service"`
		PerDay  int    `gorm:"column:per_day"`
		Used    int    `gorm:"column:used"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT b.service,b.per_day,count(a.id)::int AS used FROM budgets b JOIN receipt_attempts a ON a.service=b.service AND a.origin='live' AND a.started_at > now() - interval '1 day' WHERE b.per_day > 0 GROUP BY 1,2 HAVING count(a.id) >= b.per_day`).Scan(&capped).Error; err != nil {
		return nil, err
	}
	for _, item := range capped {
		name, stops, _ := providerSummary(item.Service)
		findings = append(findings, Finding{Key: "budget.day." + item.Service, Level: LevelToday, Title: name + " 过去 24 小时的调用额度用完了", Impact: stops + "，直到额度随时间腾出来", Heals: "会，额度按 24 小时滚动恢复", Action: "这次不用处理；如果经常出现，再决定要不要调高额度", Detail: fmt.Sprintf("24 小时内 %d 次，上限 %d（budgets 表）", item.Used, item.PerDay)})
	}
	return findings, nil
}

func (r *GormRepository) deliveryFindings(ctx context.Context) ([]Finding, error) {
	var value struct {
		Count    int     `gorm:"column:count"`
		Target   *string `gorm:"column:target"`
		Response *string `gorm:"column:response"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT count(*)::int AS count,max(t.note) AS target,left(max(d.response),200) AS response FROM deliveries d JOIN notify_targets t ON t.key=d.target_key WHERE d.status='failed' AND d.updated_at > now() - interval '1 day'`).Scan(&value).Error
	if err != nil {
		return nil, err
	}
	if value.Count == 0 {
		return nil, nil
	}
	target := "内容群"
	if value.Target != nil && *value.Target != "" {
		target = *value.Target
	}
	response := ""
	if value.Response != nil {
		response = *value.Response
	}
	return []Finding{{Key: "deliveries.failed", Level: LevelToday, Title: "飞书内容群有推送没发出去", Impact: fmt.Sprintf("过去 24 小时 %d 条精选通知没进%s", value.Count, target), Heals: "不会自动重发", Action: "转给 AI 处理；如果推送机器人被移出了群，需要你把它加回去", Detail: response}}, nil
}

func (r *GormRepository) backupFindings(ctx context.Context, now time.Time, cfg Config) ([]Finding, error) {
	if !cfg.BackupConfigured {
		return nil, nil
	}
	var setting struct {
		Value json.RawMessage `gorm:"column:value"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT value FROM settings WHERE key='backup.last'`).Scan(&setting).Error; err != nil {
		return nil, err
	}
	var backup backupSnapshot
	if len(setting.Value) > 0 {
		if err := json.Unmarshal(setting.Value, &backup); err != nil {
			return nil, err
		}
	}
	var runs struct {
		LastOK       *time.Time `gorm:"column:last_ok"`
		FirstAttempt *time.Time `gorm:"column:first_attempt"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT max(finished_at) FILTER (WHERE status='ok') AS last_ok,min(started_at) AS first_attempt FROM job_runs WHERE job='ops.backup'`).Scan(&runs).Error; err != nil {
		return nil, err
	}
	var lastSuccess *time.Time
	if runs.LastOK != nil {
		lastSuccess = runs.LastOK
	}
	return backupFailureFinding(now, backup, lastSuccess, runs.FirstAttempt), nil
}

/** Builds the backup alert after applying uploaded-state and escalation-age rules. */
func backupFailureFinding(now time.Time, backup backupSnapshot, lastSuccess, firstAttempt *time.Time) []Finding {
	if backup.Uploaded != nil && *backup.Uploaded {
		if at, err := time.Parse(time.RFC3339Nano, backup.At); err == nil && (lastSuccess == nil || at.After(*lastSuccess)) {
			lastSuccess = &at
		}
	}
	since := now
	if lastSuccess != nil {
		since = *lastSuccess
	} else if firstAttempt != nil {
		since = *firstAttempt
	}
	age := now.Sub(since)
	if age <= 50*time.Hour {
		return nil
	}
	state := ""
	if backup.FilesErr != "" {
		state = "；数据库已上传，附件打包失败：" + backup.FilesErr
	} else if backup.Uploaded != nil && !*backup.Uploaded {
		state = "；未上传"
	}
	impact := "还没有成功的完整备份，服务器出事时可能无法恢复数据"
	if lastSuccess != nil {
		impact = fmt.Sprintf("万一服务器出事，最近 %s 的数据可能无法完整恢复", duration(age))
	}
	kind := "首次备份尝试"
	if lastSuccess != nil {
		kind = "最近一次成功"
	}
	return []Finding{{Key: "backup.failed", Level: LevelToday, Title: "数据库备份连续两天没成功", Impact: impact, Heals: "不会", Action: "转给 AI 处理", Detail: kind + " " + beijingStamp(since) + state + "；看 ops.backup 的运行记录", Since: &since}}
}

func overdueDaily(now time.Time, edition string) (bool, string) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	local := now.In(location)
	parts := strings.Split(edition, ":")
	if len(parts) != 2 {
		return false, ""
	}
	hour, err1 := time.Parse("15", parts[0])
	minute, err2 := time.Parse("04", parts[1])
	if err1 != nil || err2 != nil {
		return false, ""
	}
	deadline := time.Date(local.Year(), local.Month(), local.Day(), hour.Hour(), minute.Minute(), 0, 0, location).Add(2 * time.Hour)
	return !local.Before(deadline), local.Format("2006-01-02")
}

func providerSummary(service string) (name, stops, console string) {
	switch service {
	case "llm":
		return "默认模型服务", "新文章的精选、摘要、分类和全文翻译，事件的归组和综述都停了，周报月报出刊时没有总述", "模型服务商的控制台"
	case "zhipu":
		return "智谱", "用到这家模型的步骤停了（看后台“模型与评测”），新内容可能进不了精选", "智谱开放平台"
	case "dashscope":
		return "阿里云百炼", "用到这家模型的步骤停了（看后台“模型与评测”），新内容可能进不了精选", "阿里云百炼控制台"
	case "deepseek":
		return "DeepSeek", "用到这家模型的步骤停了（看后台“模型与评测”），新内容可能进不了精选", "DeepSeek 开放平台"
	case "mimo":
		return "小米 MiMo", "用到这家模型的步骤停了（看后台“模型与评测”），新内容可能进不了精选", "小米 MiMo 开放平台"
	case "socialdata":
		return "SocialData", "X（推特）上的新内容收不到", "SocialData 后台"
	case "jina":
		return "Jina", "部分文章取不到正文", "Jina 后台"
	case "dajiala":
		return "极致了（Dajiala）", "公众号新文章收不到", "极致了后台"
	default:
		return service, "相关功能停了", service + " 后台"
	}
}

func fallback(value, otherwise string) string {
	if value == "" {
		return otherwise
	}
	return value
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

var _ Repository = (*GormRepository)(nil)
