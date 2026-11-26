package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SourceSchedule holds optional platform limits in minutes.
type SourceSchedule struct {
	AutoMinInterval     int
	ManualReuseWindow   int
	AutoReuseWindow     int
	RecommendationFeeds []string
}

const RecommendationIntervalMinutes = 360

var recommendationFeeds = []string{
	"https://rss.arxiv.org/rss/cs.AI",
	"https://huggingface.co/blog/feed.xml",
	"https://simonwillison.net/atom/everything/",
	"https://latent.space/feed",
	"https://github.com/pydantic/pydantic-ai/releases.atom",
	"https://blog.cloudflare.com/rss/",
	"https://kubernetes.io/feed.xml",
	"https://developer.nvidia.com/blog/feed/",
}

type Config struct {
	Addr                        string
	DatabaseURL                 string
	RedisURL                    string
	RedisCAFile                 string
	DataDir                     string
	PublicURL                   string
	OpenRegistration            bool
	CookieSecure                bool
	TrustProxy                  bool
	UserAPIKeyEncryptionKey     []byte
	SMTPHost                    string
	SMTPPort                    int
	SMTPUsername                string
	SMTPPassword                string
	SMTPFrom                    string
	PersonalizationEnabled      bool
	CollectorEnabled            bool
	SourceSchedule              SourceSchedule
	SharedBodyEnabled           bool
	SharedPipelineEnabled       bool
	StoryDigestEnabled          bool
	GoContentDeliveryEnabled    bool
	WebOGPrewarmURL             string
	WebOGPrewarmSecret          string
	ModelCallsEnabled           bool
	SourceNamingBaseURL         string
	SourceNamingModel           string
	SourceNamingAPIKey          string
	AlertQuietMinutes           int
	UserJobMaxWorkers           int
	UserDailyModelCallLimit     int
	SiteURL                     string
	SiteName                    string
	SiteCrawlerName             string
	SiteDescription             string
	SiteTagline                 string
	SiteLocale                  string
	SiteInterfaceVersion        string
	SiteMCPPrefix               string
	SiteEditionDaily            string
	SiteEditionWeekly           string
	SiteEditionMonthly          string
	SiteReportEntryMeasure      string
	SiteReportEntryNoun         string
	SiteLLMSIntro               string
	SiteContactEmail            string
	SiteTermsCovers             string
	SitePrivacyCovers           string
	SiteTermsLLMSLicense        string
	SiteAccessRatePerMinute     int
	SiteChangelogPath           string
	SitePublicDir               string
	SiteContactDir              string
	IndexNowKey                 string
	IndexNowSubmitEnabled       bool
	MCPAllowedHosts             []string
	EnvironmentName             string
	SessionSecret               string
	ImageProxySignSecret        string
	ImageProxyMinuteBudgetBytes int64
	ImageProxyDailyBudgetBytes  int64
	GoMediaPrepareEnabled       bool
	EgressProxyURL              string
	DirectFetchHosts            []string
	IngestToken                 string
	IngestRateLimit             int
	DajialaKey                  string
	DajialaBaseURL              string
	JinaAPIKey                  string
	JinaBaseURL                 string
	SocialDataKey               string
	SocialDataBaseURL           string
	XPostIsFullText             bool
	AdminPassword               string
	AdminEmails                 []string
	AdminFeishuUnionIDs         []string
	ModelEnvironment            map[string]string
	modelCredentials            map[string]string
	FeishuLoginAppID            string
	FeishuLoginAppSecret        string
	FeishuAppID                 string
	FeishuAppSecret             string
	FeishuInternalEnabled       bool
	FeishuInternalChatID        string
	FeishuAlertChatID           string
	FeishuContentPushEnabled    bool
	FeishuPushWebhookURL        string
	FeishuPushMirrorWebhookURL  string
	BackupStoreSecretID         string
	BackupStoreSecretKey        string
	BackupStoreBucket           string
	BackupStoreRegion           string
	BackupStoreDomain           string
	DevAdmin                    bool
	DevAdminDisplayName         string
	MarketDataEnabled           bool
	MarketProvider              string
	MarketTwelveDataAPIKey      string
	MarketFinnhubAPIKey         string
}

func Load() (Config, error) {
	sourceSchedule := SourceSchedule{RecommendationFeeds: append([]string(nil), recommendationFeeds...)}
	for name, target := range map[string]*int{
		"SOURCE_AUTO_MIN_INTERVAL_MINUTES":   &sourceSchedule.AutoMinInterval,
		"SOURCE_MANUAL_REUSE_WINDOW_MINUTES": &sourceSchedule.ManualReuseWindow,
		"SOURCE_AUTO_REUSE_WINDOW_MINUTES":   &sourceSchedule.AutoReuseWindow,
	} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		minutes, err := strconv.Atoi(raw)
		if err != nil || minutes < 1 || minutes > 1440 {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and 1440 minutes", name)
		}
		*target = minutes
	}

	// 加载凭据组
	credentials := loadCredentialGroups()
	credential := func(group, name string) string {
		if value, ok := os.LookupEnv(name); ok {
			if strings.TrimSpace(value) == "" {
				return ""
			}
			return value
		}
		value := credentials[group][name]
		if strings.TrimSpace(value) == "" {
			return ""
		}
		return value
	}
	port := envInt("SMTP_PORT", 587)
	publicURL := strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:3000"), "/")
	siteURL := strings.TrimRight(env("SITE_URL", "http://localhost:3000"), "/")
	encryption := []byte(nil)
	if value := strings.TrimSpace(os.Getenv("USER_API_KEY_ENCRYPTION_KEY")); value != "" {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return Config{}, errors.New("USER_API_KEY_ENCRYPTION_KEY must be base64 for exactly 32 bytes")
		}
		encryption = decoded
	}
	secure := strings.HasPrefix(strings.ToLower(publicURL), "https://")
	indexNowKey := os.Getenv("INDEXNOW_KEY")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(indexNowKey) {
		indexNowKey = ""
	}
	if value := strings.TrimSpace(os.Getenv("SESSION_COOKIE_SECURE")); value != "" {
		secure = strings.EqualFold(value, "true")
	}
	// A public HTTPS deployment always uses Secure cookies.
	if strings.HasPrefix(strings.ToLower(publicURL), "https://") {
		secure = true
	}
	modelEnvironment := make(map[string]string)
	modelCredentials := make(map[string]string, len(credentials["models"]))
	for key, value := range credentials["models"] {
		modelCredentials[key] = value
	}
	for _, key := range []string{"LLM_MODEL", "LLM_VISION", "PREFILTER_MODEL", "SCORE_MODEL", "UNDERSTAND_MODEL", "SUMMARIZE_MODEL", "STRUCTURE_MODEL", "GROUP_MODEL", "GROUP_REVIEW_MODEL", "DIGEST_MODEL", "REPORT_MODEL", "TRANSLATE_MODEL"} {
		if value := credential("models", key); value != "" {
			modelEnvironment[key] = value
		}
	}
	return Config{
		Addr:                        env("GO_API_ADDR", "127.0.0.1:3002"),
		DatabaseURL:                 env("DATABASE_URL", "postgres://127.0.0.1:5432/aihot"),
		RedisURL:                    strings.TrimSpace(os.Getenv("REDIS_URL")),
		RedisCAFile:                 strings.TrimSpace(os.Getenv("REDIS_CA_FILE")),
		DataDir:                     env("AIHOT_DATA_DIR", filepath.Join(repositoryRoot(), ".data")),
		PublicURL:                   publicURL,
		OpenRegistration:            envBool("OPEN_REGISTRATION_ENABLED", false),
		CookieSecure:                secure,
		TrustProxy:                  envBool("GO_TRUST_PROXY", false),
		UserAPIKeyEncryptionKey:     encryption,
		SMTPHost:                    strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                    port,
		SMTPUsername:                os.Getenv("SMTP_USERNAME"),
		SMTPPassword:                os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                    strings.TrimSpace(os.Getenv("SMTP_FROM")),
		PersonalizationEnabled:      envBool("PERSONALIZATION_ENABLED", false),
		CollectorEnabled:            envBool("COLLECT_ENABLED", false),
		SourceSchedule:              sourceSchedule,
		SharedBodyEnabled:           envBool("GO_SHARED_BODY_ENABLED", false),
		SharedPipelineEnabled:       envBool("GO_SHARED_PIPELINE_ENABLED", false),
		StoryDigestEnabled:          envBool("GO_STORY_DIGEST_ENABLED", false),
		GoContentDeliveryEnabled:    envBool("GO_CONTENT_DELIVERY_ENABLED", false),
		WebOGPrewarmURL:             env("WEB_OG_PREWARM_URL", "http://127.0.0.1:3000/_internal/og/prewarm"),
		WebOGPrewarmSecret:          strings.TrimSpace(os.Getenv("WEB_OG_PREWARM_SECRET")),
		ModelCallsEnabled:           envBool("MODEL_CALLS_ENABLED", false),
		SourceNamingBaseURL:         env("SOURCE_NAMING_BASE_URL", ""),
		SourceNamingModel:           env("SOURCE_NAMING_MODEL", ""),
		SourceNamingAPIKey:          env("SOURCE_NAMING_API_KEY", ""),
		AlertQuietMinutes:           envIntAllowAny("ALERT_QUIET_MINUTES", 360),
		UserJobMaxWorkers:           envInt("USER_JOB_MAX_WORKERS", 4),
		UserDailyModelCallLimit:     envInt("USER_DAILY_MODEL_CALL_LIMIT", 100),
		SiteURL:                     siteURL,
		SiteName:                    env("SITE_NAME", "MyHOT"),
		SiteCrawlerName:             env("SITE_CRAWLER_NAME", "MyHOTBot/1.0"),
		SiteDescription:             env("SITE_DESCRIPTION", "连接自己的 OpenAI 兼容模型，选择公司和关键词，MyHOT 从公共信息源中整理与你有关的热点和每日简报。"),
		SiteTagline:                 env("SITE_TAGLINE", "个人 AI 热点工作区"),
		SiteLocale:                  env("SITE_LOCALE", "zh-CN"),
		SiteInterfaceVersion:        env("SITE_INTERFACE_VERSION", "4.0.0"),
		SiteMCPPrefix:               env("SITE_MCP_PREFIX", "myhot"),
		SiteEditionDaily:            env("SITE_EDITION_DAILY", "08:00"),
		SiteEditionWeekly:           env("SITE_EDITION_WEEKLY", "10:00"),
		SiteEditionMonthly:          env("SITE_EDITION_MONTHLY", "10:30"),
		SiteReportEntryMeasure:      env("SITE_REPORT_ENTRY_MEASURE", "件"),
		SiteReportEntryNoun:         env("SITE_REPORT_ENTRY_NOUN", "大事"),
		SiteLLMSIntro:               os.Getenv("SITE_LLMS_INTRO"),
		SiteContactEmail:            strings.TrimSpace(os.Getenv("SITE_CONTACT_EMAIL")),
		SiteTermsCovers:             os.Getenv("SITE_TERMS_COVERS"),
		SitePrivacyCovers:           os.Getenv("SITE_PRIVACY_COVERS"),
		SiteTermsLLMSLicense:        os.Getenv("SITE_TERMS_LLMS_LICENSE"),
		SiteAccessRatePerMinute:     envInt("SITE_ACCESS_RATE_PER_MINUTE", 0),
		SiteChangelogPath:           env("SITE_CHANGELOG_PATH", filepath.Join(repositoryRoot(), "backend-go", "config", "changelog.json")),
		SitePublicDir:               sitePublicDir(),
		SiteContactDir:              siteContactDir(),
		IndexNowKey:                 indexNowKey,
		IndexNowSubmitEnabled:       envBool("INDEXNOW_SUBMIT_ENABLED", false),
		MCPAllowedHosts:             splitCSV(os.Getenv("MCP_ALLOWED_HOSTS"), true),
		EnvironmentName:             env("AIHOT_ENVIRONMENT", environmentName()),
		SessionSecret:               credential("auth", "SESSION_SECRET"),
		ImageProxySignSecret:        credential("auth", "IMG_PROXY_SIGN_SECRET"),
		ImageProxyMinuteBudgetBytes: envByteBudget("IMGPROXY_UPSTREAM_MB_PER_MINUTE", 1_000_000),
		ImageProxyDailyBudgetBytes:  envByteBudget("IMGPROXY_UPSTREAM_GB_PER_DAY", 1_000_000_000),
		GoMediaPrepareEnabled:       envBool("GO_MEDIA_PREPARE_ENABLED", false),
		EgressProxyURL:              strings.TrimSpace(os.Getenv("EGRESS_PROXY_URL")),
		DirectFetchHosts:            splitCSV(os.Getenv("SITE_DIRECT_FETCH_HOSTS"), true),
		IngestToken:                 credential("auth", "INGEST_TOKEN"),
		IngestRateLimit:             max(0, envFloorInt("INGEST_RATE_LIMIT", 0)),
		DajialaKey:                  credential("collectors", "DAJIALA_KEY"),
		DajialaBaseURL:              strings.TrimRight(env("DAJIALA_BASE_URL", credential("collectors", "DAJIALA_BASE_URL")), "/"),
		JinaAPIKey:                  credential("collectors", "JINA_API_KEY"),
		JinaBaseURL:                 strings.TrimRight(env("JINA_BASE_URL", credential("collectors", "JINA_BASE_URL")), "/"),
		SocialDataKey:               credential("collectors", "SOCIALDATA_API_KEY"),
		SocialDataBaseURL:           strings.TrimRight(env("SOCIALDATA_BASE_URL", credential("collectors", "SOCIALDATA_BASE_URL")), "/"),
		XPostIsFullText:             envBool("SITE_X_POST_IS_FULL_TEXT", true),
		AdminPassword:               os.Getenv("ADMIN_PASSWORD"),
		AdminEmails:                 splitCSV(os.Getenv("ADMIN_EMAILS"), true),
		AdminFeishuUnionIDs:         splitCSV(os.Getenv("ADMIN_FEISHU_UNION_IDS"), false),
		ModelEnvironment:            modelEnvironment,
		modelCredentials:            modelCredentials,
		FeishuLoginAppID:            credential("integrations", "FEISHU_LOGIN_APP_ID"),
		FeishuLoginAppSecret:        credential("integrations", "FEISHU_LOGIN_APP_SECRET"),
		FeishuAppID:                 credential("integrations", "FEISHU_APP_ID"),
		FeishuAppSecret:             credential("integrations", "FEISHU_APP_SECRET"),
		FeishuInternalEnabled:       envBool("FEISHU_INTERNAL_ENABLED", false),
		FeishuInternalChatID:        credential("integrations", "FEISHU_INTERNAL_CHAT_ID"),
		FeishuAlertChatID:           credential("integrations", "FEISHU_ALERT_CHAT_ID"),
		FeishuContentPushEnabled:    envBool("FEISHU_CONTENT_PUSH_ENABLED", false),
		FeishuPushWebhookURL:        credential("integrations", "FEISHU_PUSH_WEBHOOK_URL"),
		FeishuPushMirrorWebhookURL:  credential("integrations", "FEISHU_PUSH_MIRROR_WEBHOOK_URL"),
		BackupStoreSecretID:         credential("integrations", "DB_BACKUP_STORE_SECRET_ID"),
		BackupStoreSecretKey:        credential("integrations", "DB_BACKUP_STORE_SECRET_KEY"),
		BackupStoreBucket:           credential("integrations", "DB_BACKUP_STORE_BUCKET"),
		BackupStoreRegion:           credential("integrations", "DB_BACKUP_STORE_REGION"),
		BackupStoreDomain:           credential("integrations", "DB_BACKUP_STORE_DOMAIN"),
		DevAdmin:                    os.Getenv("DEV_AUTH_ROLE") == "admin",
		DevAdminDisplayName:         env("DEV_AUTH_DISPLAY_NAME", "Dev Admin"),
		MarketDataEnabled:           envBool("MARKET_DATA_ENABLED", false),
		MarketProvider:              strings.ToLower(env("MARKET_PROVIDER", "mock")),
		MarketTwelveDataAPIKey:      credential("collectors", "MARKET_TWELVEDATA_API_KEY"),
		MarketFinnhubAPIKey:         credential("collectors", "MARKET_FINNHUB_API_KEY"),
	}, nil
}

// ModelCredential applies process-environment precedence to model provider credentials.
func (c Config) ModelCredential(name string) string {
	if value, ok := os.LookupEnv(name); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(c.modelCredentials[name])
}

func (c Config) ValidateRuntime() error {
	if c.SharedPipelineEnabled && (!c.ModelCallsEnabled || !c.GoContentDeliveryEnabled || !c.GoMediaPrepareEnabled || !c.SharedBodyEnabled || !c.CollectorEnabled) {
		return errors.New("GO_SHARED_PIPELINE_ENABLED requires MODEL_CALLS_ENABLED, GO_CONTENT_DELIVERY_ENABLED, GO_MEDIA_PREPARE_ENABLED, GO_SHARED_BODY_ENABLED, and COLLECT_ENABLED")
	}
	if c.GoContentDeliveryEnabled && (strings.TrimSpace(c.WebOGPrewarmURL) == "" || len(strings.TrimSpace(c.WebOGPrewarmSecret)) < 32) {
		return errors.New("GO_CONTENT_DELIVERY_ENABLED requires WEB_OG_PREWARM_URL and a WEB_OG_PREWARM_SECRET of at least 32 characters")
	}
	if c.EnvironmentName != "production" {
		return nil
	}
	secret := strings.TrimSpace(c.SessionSecret)
	if len(secret) < 8 || placeholderSecret.MatchString(secret) {
		return errors.New("Refusing to start in production: SESSION_SECRET is missing or invalid")
	}
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && strings.HasPrefix(key, "DEV_AUTH_") {
			return errors.New("Refusing to start in production: development authentication is configured")
		}
	}
	return nil
}

var placeholderSecret = regexp.MustCompile(`(?i)^(changeme|placeholder|dummy|test|xxx+|your[-_ ].*|<.*>)$`)

func environmentName() string {
	if strings.EqualFold(os.Getenv("NODE_ENV"), "production") {
		return "production"
	}
	return "development"
}

func splitCSV(value string, lower bool) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if lower {
			part = strings.ToLower(part)
		}
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

// loadCredentialGroups mirrors the TypeScript backend's auth.env and
// integrations.env loading. Process environment values always take priority.
func loadCredentialGroups() map[string]map[string]string {
	dir := strings.TrimSpace(os.Getenv("AIHOT_CREDENTIALS_DIR"))
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repositoryRoot(), dir)
	}
	out := map[string]map[string]string{}
	for _, group := range []string{"auth", "integrations", "models", "collectors"} {
		// 读取环境变量配置
		values, err := readEnvFile(filepath.Join(dir, group+".env"))
		if err == nil {
			out[group] = values
		}
	}
	return out
}

func repositoryRoot() string {
	current, err := os.Getwd()
	if err != nil {
		return "."
	}
	for dir := current; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return current
		}
	}
}

/** siteContactDir locates bundled contact images while preserving configured directories. */
func siteContactDir() string {
	root := repositoryRoot()
	return env("SITE_CONTACT_DIR", filepath.Join(root, "frontend", "brand", "contact"))
}

/** sitePublicDir locates the bundled API document while preserving custom directories. */
func sitePublicDir() string {
	root := repositoryRoot()
	return env("SITE_PUBLIC_DIR", filepath.Join(root, "backend-go", "config", "public"))
}

// 读取环境变量配置
func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			continue
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if value[0] == '"' {
				if parsed, err := strconv.Unquote(value); err == nil {
					value = parsed
				} else {
					return nil, fmt.Errorf("parse %s: %w", path, err)
				}
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value) == "true"
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envIntAllowAny(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

func envFloorInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return int(math.Floor(value))
}

func envByteBudget(name string, unit int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > float64(math.MaxInt64)/float64(unit) {
		return 0
	}
	return int64(value * float64(unit))
}
