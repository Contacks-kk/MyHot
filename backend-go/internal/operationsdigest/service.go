package operationsdigest

import (
	"context"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"aihot/backend-go/internal/sourcehealth"
)

type Repository interface {
	Digest(context.Context, time.Time) (Snapshot, error)
	HasRun(context.Context, time.Time, time.Time) (bool, error)
}

type Messenger interface {
	SendText(context.Context, string, string) error
}

type Config struct {
	CollectEnabled        bool
	BackupConfigured      bool
	FeishuInternalEnabled bool
	AlertChatID           string
	InternalChatID        string
	EnvironmentName       string
}

type Service struct {
	repository Repository
	messenger  Messenger
	config     Config
	location   *time.Location
}

func NewService(repository Repository, messenger Messenger, cfg Config) *Service {
	location, _ := time.LoadLocation("Asia/Shanghai")
	if location == nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return &Service{repository: repository, messenger: messenger, config: cfg, location: location}
}

type Result struct {
	Items int `json:"items"`
}

type Queue struct {
	Name   string
	Count  int
	Oldest time.Time
}

type BackupState struct {
	At         string `json:"at"`
	Uploaded   bool   `json:"uploaded"`
	FilesError string `json:"filesError"`
}

type ImageBudget struct {
	Window    string `json:"window"`
	Reason    string `json:"reason"`
	UpdatedAt time.Time
}

type Snapshot struct {
	SourceHealth       sourcehealth.Snapshot
	SourceHealthSince  time.Time
	UnknownReceipts    int
	UnknownServices    *string
	UnknownDeliveries  int
	Backup             *BackupState
	LastBackupSuccess  *time.Time
	FirstBackupAttempt *time.Time
	ImageBudget        *ImageBudget
	Queues             []Queue
}

func (s *Service) Due(ctx context.Context, now time.Time) (string, bool, error) {
	if now.IsZero() {
		now = time.Now()
	}
	local := now.In(s.location)
	due := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, s.location)
	if local.Before(due) {
		return "", false, nil
	}
	next := due.AddDate(0, 0, 1)
	found, err := s.repository.HasRun(ctx, due.UTC(), next.UTC())
	if err != nil || found {
		return "", false, err
	}
	return local.Format("2006-01-02"), true, nil
}

func (s *Service) Run(ctx context.Context, now time.Time) (Result, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	snapshot, err := s.repository.Digest(ctx, now)
	if err != nil {
		return Result{}, err
	}
	items := s.findings(snapshot, now)
	if len(items) == 0 {
		return Result{}, nil
	}
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, "以下事项需要跟进，具体影响见各条说明；可把整条转给 AI 处理。")
	for i, item := range items {
		line := fmt.Sprintf("%d. %s", i+1, item.title)
		if item.detail != "" {
			line += "\n   " + item.detail
		}
		lines = append(lines, line)
	}
	chat := strings.TrimSpace(s.config.AlertChatID)
	if chat == "" {
		chat = strings.TrimSpace(s.config.InternalChatID)
	}
	if s.config.FeishuInternalEnabled && chat != "" {
		if s.messenger == nil {
			return Result{}, fmt.Errorf("operations digest messenger is unavailable")
		}
		prefix := ""
		if s.config.EnvironmentName != "" && s.config.EnvironmentName != "production" {
			prefix = "【" + s.config.EnvironmentName + "】"
		}
		day := beijingDay(now, s.location)
		if err := s.messenger.SendText(ctx, chat, prefix+"📋 系统日报 · "+day+"\n"+strings.Join(lines, "\n")); err != nil {
			return Result{}, err
		}
	}
	return Result{Items: len(items)}, nil
}

type finding struct{ title, detail string }

func (s *Service) findings(snapshot Snapshot, now time.Time) []finding {
	items := make([]finding, 0)
	if s.config.BackupConfigured {
		items = append(items, backupFinding(snapshot, now, s.location)...)
	}
	if snapshot.ImageBudget != nil && snapshot.ImageBudget.Window == "day" && now.Sub(snapshot.ImageBudget.UpdatedAt) < 24*time.Hour {
		items = append(items, finding{title: "图片代理今天的流量额度用完了，未缓存的图片到零点前显示不出来", detail: snapshot.ImageBudget.Reason})
	}
	if s.config.CollectEnabled {
		items = append(items, sourceFindings(snapshot)...)
	}
	if snapshot.UnknownReceipts > 0 {
		services := ""
		if snapshot.UnknownServices != nil {
			services = *snapshot.UnknownServices
		}
		items = append(items, finding{
			title:  fmt.Sprintf("%s 个付费请求的结果尚未确认", comma(snapshot.UnknownReceipts)),
			detail: services + "；可能影响内容处理，后台“运行”页查看影响；自动恢复后仍未知的请求需核对后放行",
		})
	}
	if snapshot.UnknownDeliveries > 0 {
		items = append(items, finding{title: fmt.Sprintf("%s 条飞书内容群推送不确定是否送达", comma(snapshot.UnknownDeliveries)), detail: "后台“运行”页核对群里有没有，再标记或重发"})
	}
	for _, queue := range snapshot.Queues {
		if now.Sub(queue.Oldest) <= 2*time.Hour {
			continue
		}
		items = append(items, finding{
			title:  fmt.Sprintf("后台任务排队超过 2 小时：%s", queue.Name),
			detail: fmt.Sprintf("%s 个等待，最早的等了 %s", comma(queue.Count), duration(now.Sub(queue.Oldest))),
		})
	}
	return items
}

func sourceFindings(data Snapshot) []finding {
	snapshot := data.SourceHealth
	groups := []struct{ mode, name string }{{"editorial", "编辑内容"}, {"hot_signal", "热度信号"}}
	items := make([]finding, 0)
	for _, group := range groups {
		sources := make([]sourcehealth.Source, 0)
		for _, source := range snapshot.Sources {
			if source.Mode == group.mode {
				sources = append(sources, source)
			}
		}
		failing := filterSources(sources, func(source sourcehealth.Source) bool { return source.Health == "failing" })
		unstable := filterSources(sources, func(source sourcehealth.Source) bool {
			return source.Failed >= 5 && source.Runs > 0 && float64(source.Failed)/float64(source.Runs) >= 0.1
		})
		silent := filterSources(sources, func(source sourcehealth.Source) bool {
			return source.CreatedAt.Before(snapshotSince(data)) && source.Discoveries == 0
		})
		quality := []sourcehealth.Source{}
		if group.mode == "editorial" {
			quality = filterSources(sources, func(source sourcehealth.Source) bool { return source.Undated > 0 || source.Repeated > 0 })
		}
		details := filterSources(sources, func(source sourcehealth.Source) bool { return source.DetailFailures > 0 })
		if len(failing) > 0 {
			items = append(items, finding{title: fmt.Sprintf("%s有 %s 个信源连续抓取失败", group.name, comma(len(failing))), detail: sourceList(failing, func(source sourcehealth.Source) string {
				text := "连续失败 " + comma(source.FailCount) + " 次"
				if source.LastError != nil && *source.LastError != "" {
					text += "，" + *source.LastError
				}
				return text
			}) + "；转给 AI 检查抓取错误"})
		}
		if len(unstable) > 0 {
			items = append(items, finding{title: fmt.Sprintf("%s有 %s 个信源反复抓取失败", group.name, comma(len(unstable))), detail: sourceList(unstable, func(source sourcehealth.Source) string {
				return "近 7 天失败 " + comma(source.Failed) + "/" + comma(source.Runs) + " 次"
			}) + "；即使最近成功也需检查，避免继续漏收"})
		}
		if len(silent) > 0 {
			items = append(items, finding{title: fmt.Sprintf("%s有 %s 个信源 7 天未发现新内容", group.name, comma(len(silent))), detail: sourceList(silent, func(source sourcehealth.Source) string {
				return "近 7 天 " + comma(source.Runs) + " 次抓取、0 条新发现"
			}) + "；需对照原站，区分低频更新与采集失效"})
		}
		if len(quality) > 0 {
			items = append(items, finding{title: fmt.Sprintf("%s有 %s 个信源需要核实文章质量", group.name, comma(len(quality))), detail: sourceList(quality, func(source sourcehealth.Source) string {
				return "近 7 天缺发布时间 " + comma(source.Undated) + " 篇、反复修订 " + comma(source.Repeated) + " 篇"
			}) + "；缺时间可能让新闻按历史文章处理，反复修订需核对正文是否混入变化内容"})
		}
		if len(details) > 0 {
			items = append(items, finding{title: fmt.Sprintf("%s有 %s 个信源详情补全失败", group.name, comma(len(details))), detail: sourceList(details, func(source sourcehealth.Source) string {
				return "近 7 天 " + comma(source.DetailFailures) + " 次"
			}) + "；查看抓取记录中的详情地址与错误"})
		}
	}
	return items
}

func snapshotSince(snapshot Snapshot) time.Time {
	return snapshot.SourceHealthSince
}

func filterSources(input []sourcehealth.Source, keep func(sourcehealth.Source) bool) []sourcehealth.Source {
	filtered := make([]sourcehealth.Source, 0)
	for _, source := range input {
		if keep(source) {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

func sourceList(sources []sourcehealth.Source, describe func(sourcehealth.Source) string) string {
	limit := min(len(sources), 10)
	parts := make([]string, 0, limit+1)
	for _, source := range sources[:limit] {
		parts = append(parts, source.Name+"（"+source.ID+"）："+describe(source))
	}
	if len(sources) > limit {
		parts = append(parts, fmt.Sprintf("另有 %s 个，见信源周报与后台信源列表", comma(len(sources)-limit)))
	}
	return strings.Join(parts, "；")
}

func backupFinding(snapshot Snapshot, now time.Time, location *time.Location) []finding {
	lastOK := time.Time{}
	if snapshot.LastBackupSuccess != nil {
		lastOK = *snapshot.LastBackupSuccess
	}
	if snapshot.Backup != nil && snapshot.Backup.Uploaded {
		if at, err := time.Parse(time.RFC3339Nano, snapshot.Backup.At); err == nil && at.After(lastOK) {
			lastOK = at
		}
	}
	since := lastOK
	if since.IsZero() && snapshot.FirstBackupAttempt != nil {
		since = *snapshot.FirstBackupAttempt
	}
	if since.IsZero() {
		since = now
	}
	age := now.Sub(since)
	state := ""
	if snapshot.Backup != nil && snapshot.Backup.FilesError != "" {
		state = "数据库已上传，附件打包失败：" + snapshot.Backup.FilesError
	} else if snapshot.Backup != nil && !snapshot.Backup.Uploaded {
		state = "未上传"
	}
	if age > 50*time.Hour {
		return nil // The matching finding is level "today" and is sent by ops.alerts.
	}
	if snapshot.Backup == nil || !snapshot.Backup.Uploaded || age > 30*time.Hour {
		detail := "还没有成功的备份记录"
		if snapshot.Backup != nil {
			at := since
			if parsed, err := time.Parse(time.RFC3339Nano, snapshot.Backup.At); err == nil {
				at = parsed
			}
			detail = "最近一次 " + beijingStamp(at, location)
			if state != "" {
				detail += "（" + state + "）"
			}
			detail += "；看 ops.backup"
		}
		items := []finding{{title: "数据库备份超过一天没成功", detail: detail}}
		return items
	}
	return nil
}

func beijingDay(at time.Time, location *time.Location) string {
	local := at.In(location)
	return fmt.Sprintf("%d月%d日", int(local.Month()), local.Day())
}

func beijingStamp(at time.Time, location *time.Location) string {
	local := at.In(location)
	return fmt.Sprintf("%s %02d:%02d", beijingDay(local, location), local.Hour(), local.Minute())
}

func duration(value time.Duration) string {
	minutes := max(1, int((value+30*time.Second)/time.Minute))
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	if minutes < 48*60 {
		return fmt.Sprintf("%d 小时%s", minutes/60, func() string {
			if minutes%60 == 0 {
				return ""
			}
			return " " + fmt.Sprintf("%d 分钟", minutes%60)
		}())
	}
	return fmt.Sprintf("%d 天", minutes/1440)
}

func comma(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	text := fmt.Sprint(value)
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	if negative {
		return "-" + text
	}
	return text
}
