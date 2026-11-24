package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/** TestSiteContactDirKeepsCustomDirectory verifies branding relocation preserves explicit contact paths. */
func TestSiteContactDirKeepsCustomDirectory(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("SITE_CONTACT_DIR", custom)
	if got := siteContactDir(); got != custom {
		t.Fatalf("custom contact directory changed: %q", got)
	}
	t.Setenv("SITE_CONTACT_DIR", "")
	want := filepath.Join(repositoryRoot(), "frontend", "brand", "contact")
	if got := siteContactDir(); got != want {
		t.Fatalf("default contact directory = %q, want %q", got, want)
	}
}

func TestSourceScheduleMinutes(t *testing.T) {
	t.Setenv("SOURCE_AUTO_MIN_INTERVAL_MINUTES", "15")
	t.Setenv("SOURCE_MANUAL_REUSE_WINDOW_MINUTES", "10")
	t.Setenv("SOURCE_AUTO_REUSE_WINDOW_MINUTES", "8")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceSchedule.AutoMinInterval != 15 || cfg.SourceSchedule.ManualReuseWindow != 10 || cfg.SourceSchedule.AutoReuseWindow != 8 {
		t.Fatalf("source schedule = %#v", cfg.SourceSchedule)
	}
	for _, invalid := range []string{"0", "1441", "1.5"} {
		t.Setenv("SOURCE_AUTO_REUSE_WINDOW_MINUTES", invalid)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SOURCE_AUTO_REUSE_WINDOW_MINUTES") {
			t.Fatalf("invalid source window %q returned %v", invalid, err)
		}
	}
	t.Setenv("SOURCE_AUTO_MIN_INTERVAL_MINUTES", "")
	t.Setenv("SOURCE_MANUAL_REUSE_WINDOW_MINUTES", "")
	t.Setenv("SOURCE_AUTO_REUSE_WINDOW_MINUTES", "")
	cfg, err = Load()
	if err != nil || cfg.SourceSchedule.AutoMinInterval != 0 || cfg.SourceSchedule.AutoReuseWindow != 0 || cfg.SourceSchedule.ManualReuseWindow != 0 || len(cfg.SourceSchedule.RecommendationFeeds) != 8 {
		t.Fatalf("unset source schedule = %#v, %v", cfg.SourceSchedule, err)
	}
}

/** TestLocalDefaultsAndHTTPSCookies verifies closed feature gates and secure local defaults. */
func TestLocalDefaultsAndHTTPSCookies(t *testing.T) {
	t.Setenv("USER_API_KEY_ENCRYPTION_KEY", "")
	t.Setenv("GO_API_ADDR", "")
	t.Setenv("GO_TRUST_PROXY", "false")
	t.Setenv("OPEN_REGISTRATION_ENABLED", "")
	for _, name := range []string{
		"COLLECT_ENABLED",
		"MODEL_CALLS_ENABLED",
		"PERSONALIZATION_ENABLED",
		"GO_SHARED_BODY_ENABLED",
		"GO_SHARED_PIPELINE_ENABLED",
		"GO_STORY_DIGEST_ENABLED",
		"GO_CONTENT_DELIVERY_ENABLED",
		"GO_MEDIA_PREPARE_ENABLED",
		"FEISHU_INTERNAL_ENABLED",
		"FEISHU_CONTENT_PUSH_ENABLED",
		"INDEXNOW_SUBMIT_ENABLED",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("PUBLIC_BASE_URL", "https://myhot.example")
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:3002" || cfg.TrustProxy {
		t.Fatal("direct API must default to local without trusted forwarding headers")
	}
	if cfg.OpenRegistration {
		t.Fatal("registration must default to closed")
	}
	if cfg.CollectorEnabled || cfg.ModelCallsEnabled || cfg.PersonalizationEnabled || cfg.SharedBodyEnabled || cfg.SharedPipelineEnabled || cfg.StoryDigestEnabled || cfg.GoContentDeliveryEnabled || cfg.GoMediaPrepareEnabled || cfg.FeishuInternalEnabled || cfg.FeishuContentPushEnabled || cfg.IndexNowSubmitEnabled {
		t.Fatal("an external-work safety valve must default to closed")
	}
	if !cfg.CookieSecure {
		t.Fatal("HTTPS must always use Secure cookies")
	}
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:3000")
	t.Setenv("SESSION_COOKIE_SECURE", "")
	cfg, err = Load()
	if err != nil || cfg.CookieSecure {
		t.Fatal("local HTTP must infer non-secure Cookie when override empty")
	}
}
func TestFeatureFlagsRequireLiteralTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("PERSONALIZATION_ENABLED", value)
		if envBool("PERSONALIZATION_ENABLED", false) {
			t.Fatalf("unexpected enabled flag %q", value)
		}
	}
	t.Setenv("PERSONALIZATION_ENABLED", "true")
	if !envBool("PERSONALIZATION_ENABLED", false) {
		t.Fatal("true must enable flag")
	}
}

func TestSharedBodyOwnershipSwitchDefaultsOffAndRequiresLiteralTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("GO_SHARED_BODY_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.SharedBodyEnabled {
			t.Fatalf("shared body ownership enabled for %q: enabled=%t err=%v", value, cfg.SharedBodyEnabled, err)
		}
	}
	t.Setenv("GO_SHARED_BODY_ENABLED", "true")
	cfg, err := Load()
	if err != nil || !cfg.SharedBodyEnabled {
		t.Fatalf("literal true did not enable shared body ownership: enabled=%t err=%v", cfg.SharedBodyEnabled, err)
	}
}

func TestStoryDigestOwnershipSwitchDefaultsOffAndRequiresLiteralTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("GO_STORY_DIGEST_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.StoryDigestEnabled {
			t.Fatalf("story digest ownership enabled for %q: enabled=%t err=%v", value, cfg.StoryDigestEnabled, err)
		}
	}
	t.Setenv("GO_STORY_DIGEST_ENABLED", "true")
	if cfg, err := Load(); err != nil || !cfg.StoryDigestEnabled {
		t.Fatalf("literal true did not enable story digest ownership: enabled=%t err=%v", cfg.StoryDigestEnabled, err)
	}
}

func TestGoContentDeliverySwitchDefaultsOffAndRequiresLiteralTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("GO_CONTENT_DELIVERY_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.GoContentDeliveryEnabled {
			t.Fatalf("Go content delivery enabled for %q: enabled=%t err=%v", value, cfg.GoContentDeliveryEnabled, err)
		}
	}
	t.Setenv("GO_CONTENT_DELIVERY_ENABLED", "true")
	cfg, err := Load()
	if err != nil || !cfg.GoContentDeliveryEnabled {
		t.Fatalf("literal true did not enable Go content delivery: enabled=%t err=%v", cfg.GoContentDeliveryEnabled, err)
	}
}

func TestGoMediaPrepareOwnershipDefaultsOffAndRequiresLiteralTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("GO_MEDIA_PREPARE_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.GoMediaPrepareEnabled {
			t.Fatalf("Go media ownership enabled for %q: enabled=%t err=%v", value, cfg.GoMediaPrepareEnabled, err)
		}
	}
	t.Setenv("GO_MEDIA_PREPARE_ENABLED", "true")
	cfg, err := Load()
	if err != nil || !cfg.GoMediaPrepareEnabled {
		t.Fatalf("literal true did not enable Go media ownership: enabled=%t err=%v", cfg.GoMediaPrepareEnabled, err)
	}
}

func TestSharedPipelineSwitchDefaultsOffAndRequiresItsDependencies(t *testing.T) {
	t.Setenv("AIHOT_ENVIRONMENT", "test")
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("GO_SHARED_PIPELINE_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.SharedPipelineEnabled {
			t.Fatalf("shared pipeline enabled for %q: enabled=%t err=%v", value, cfg.SharedPipelineEnabled, err)
		}
	}
	t.Setenv("GO_SHARED_PIPELINE_ENABLED", "true")
	t.Setenv("GO_SHARED_BODY_ENABLED", "true")
	t.Setenv("COLLECT_ENABLED", "true")
	t.Setenv("MODEL_CALLS_ENABLED", "false")
	t.Setenv("GO_CONTENT_DELIVERY_ENABLED", "true")
	t.Setenv("GO_MEDIA_PREPARE_ENABLED", "true")
	t.Setenv("WEB_OG_PREWARM_URL", "http://web:3000/_internal/og/prewarm")
	t.Setenv("WEB_OG_PREWARM_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil || !cfg.SharedPipelineEnabled {
		t.Fatalf("literal true did not enable pipeline config: enabled=%t err=%v", cfg.SharedPipelineEnabled, err)
	}
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("shared pipeline started while model calls were disabled")
	}
	t.Setenv("MODEL_CALLS_ENABLED", "true")
	t.Setenv("GO_CONTENT_DELIVERY_ENABLED", "false")
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() == nil {
		t.Fatal("shared pipeline started without Go content delivery")
	}
	t.Setenv("GO_CONTENT_DELIVERY_ENABLED", "true")
	t.Setenv("GO_MEDIA_PREPARE_ENABLED", "false")
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() == nil {
		t.Fatal("shared pipeline started without Go media preparation")
	}
	t.Setenv("GO_MEDIA_PREPARE_ENABLED", "true")
	t.Setenv("GO_SHARED_BODY_ENABLED", "false")
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() == nil {
		t.Fatal("shared pipeline started without Go shared body extraction")
	}
	t.Setenv("GO_SHARED_BODY_ENABLED", "true")
	t.Setenv("COLLECT_ENABLED", "false")
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() == nil {
		t.Fatal("shared pipeline started while collection/body extraction was disabled")
	}
	t.Setenv("COLLECT_ENABLED", "true")
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() != nil {
		t.Fatalf("configured shared pipeline rejected: err=%v", err)
	}
}

func TestGoContentDeliveryRequiresSignedWebPrewarmWhenEnabled(t *testing.T) {
	t.Setenv("GO_CONTENT_DELIVERY_ENABLED", "true")
	t.Setenv("WEB_OG_PREWARM_URL", "http://web:3000/_internal/og/prewarm")
	t.Setenv("WEB_OG_PREWARM_SECRET", "short")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("content delivery started without an adequately sized Web prewarm secret")
	}
	t.Setenv("WEB_OG_PREWARM_SECRET", strings.Repeat("a", 32))
	cfg, err = Load()
	if err != nil || cfg.ValidateRuntime() != nil {
		t.Fatalf("configured content delivery rejected: cfg=%#v err=%v", cfg, err)
	}
}

func TestLegacyAlertControlsRetainTheirDefaultsAndOverrides(t *testing.T) {
	t.Setenv("MODEL_CALLS_ENABLED", "")
	t.Setenv("ALERT_QUIET_MINUTES", "")
	cfg, err := Load()
	if err != nil || cfg.ModelCallsEnabled || cfg.AlertQuietMinutes != 360 {
		t.Fatalf("legacy alert defaults = modelCalls:%t quiet:%d err:%v", cfg.ModelCallsEnabled, cfg.AlertQuietMinutes, err)
	}
	t.Setenv("MODEL_CALLS_ENABLED", "true")
	t.Setenv("ALERT_QUIET_MINUTES", "0")
	cfg, err = Load()
	if err != nil || !cfg.ModelCallsEnabled || cfg.AlertQuietMinutes != 0 {
		t.Fatalf("explicit alert settings = modelCalls:%t quiet:%d err:%v", cfg.ModelCallsEnabled, cfg.AlertQuietMinutes, err)
	}
}

func TestIndexNowKeyRequiresTheConfiguredPublicFileFormat(t *testing.T) {
	for _, value := range []string{"", "ABCDEF0123456789ABCDEF0123456789", "0123456789abcdef0123456789abcdeg", "0123456789abcdef0123456789abcdef/extra"} {
		t.Setenv("INDEXNOW_KEY", value)
		cfg, err := Load()
		if err != nil || cfg.IndexNowKey != "" {
			t.Fatalf("invalid IndexNow key %q was enabled: key=%q err=%v", value, cfg.IndexNowKey, err)
		}
	}
	key := "0123456789abcdef0123456789abcdef"
	t.Setenv("INDEXNOW_KEY", key)
	cfg, err := Load()
	if err != nil || cfg.IndexNowKey != key {
		t.Fatalf("valid IndexNow key was rejected: key=%q err=%v", cfg.IndexNowKey, err)
	}
}

func TestIndexNowSubmissionSafetyValveDefaultsOff(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "yes"} {
		t.Setenv("INDEXNOW_SUBMIT_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.IndexNowSubmitEnabled {
			t.Fatalf("IndexNow submission enabled for %q: enabled=%t err=%v", value, cfg.IndexNowSubmitEnabled, err)
		}
	}
	t.Setenv("INDEXNOW_SUBMIT_ENABLED", "true")
	cfg, err := Load()
	if err != nil || !cfg.IndexNowSubmitEnabled {
		t.Fatalf("literal true did not enable IndexNow submission: enabled=%t err=%v", cfg.IndexNowSubmitEnabled, err)
	}
}

func TestGoTopicXPostPolicyMatchesSiteSetting(t *testing.T) {
	t.Setenv("SITE_X_POST_IS_FULL_TEXT", "")
	cfg, err := Load()
	if err != nil || !cfg.XPostIsFullText {
		t.Fatalf("default X post policy = %t, %v", cfg.XPostIsFullText, err)
	}
	t.Setenv("SITE_X_POST_IS_FULL_TEXT", "false")
	cfg, err = Load()
	if err != nil || cfg.XPostIsFullText {
		t.Fatalf("explicitly hidden X post policy = %t, %v", cfg.XPostIsFullText, err)
	}
}

func TestAdminCredentialGroupsMatchRuntimePrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.env"), []byte("SESSION_SECRET=from-file\nADMIN_PASSWORD=ignored-file-password\nADMIN_EMAILS=ignored@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "integrations.env"), []byte("FEISHU_LOGIN_APP_ID=file-app\nFEISHU_LOGIN_APP_SECRET=file-secret\nFEISHU_APP_ID=file-message-app\nFEISHU_APP_SECRET=file-message-secret\nFEISHU_PUSH_WEBHOOK_URL=file-content-hook\nFEISHU_PUSH_MIRROR_WEBHOOK_URL=file-mirror-hook\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("ADMIN_PASSWORD", "environment-admin-password")
	t.Setenv("ADMIN_EMAILS", "One@example.invalid, TWO@example.invalid")
	t.Setenv("ADMIN_FEISHU_UNION_IDS", "union-a, union-b")
	t.Setenv("FEISHU_LOGIN_APP_ID", "environment-app")
	t.Setenv("FEISHU_LOGIN_APP_SECRET", "")
	t.Setenv("FEISHU_APP_ID", "environment-message-app")
	t.Setenv("FEISHU_APP_SECRET", "")
	t.Setenv("FEISHU_PUSH_WEBHOOK_URL", "environment-content-hook")
	t.Setenv("FEISHU_PUSH_MIRROR_WEBHOOK_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionSecret != "" {
		t.Fatalf("an explicitly empty environment secret must override the credential file")
	}
	if cfg.AdminPassword != "environment-admin-password" || len(cfg.AdminEmails) != 2 || cfg.AdminEmails[0] != "one@example.invalid" || cfg.AdminEmails[1] != "two@example.invalid" {
		t.Fatalf("admin credentials did not preserve environment-only runtime configuration: %#v", cfg)
	}
	if cfg.FeishuLoginAppID != "environment-app" || cfg.FeishuLoginAppSecret != "" || len(cfg.AdminFeishuUnionIDs) != 2 || cfg.AdminFeishuUnionIDs[1] != "union-b" {
		t.Fatalf("integration credential precedence changed: %#v", cfg)
	}
	if cfg.FeishuAppID != "environment-message-app" || cfg.FeishuAppSecret != "" {
		t.Fatalf("message-app credential precedence changed: %#v", cfg)
	}
	if cfg.FeishuPushWebhookURL != "environment-content-hook" || cfg.FeishuPushMirrorWebhookURL != "" {
		t.Fatalf("content webhook credential precedence changed: %#v", cfg)
	}

	t.Setenv("SESSION_SECRET", "environment-secret")
	cfg, err = Load()
	if err != nil || cfg.SessionSecret != "environment-secret" {
		t.Fatalf("environment secrets must override grouped files: secret=%q err=%v", cfg.SessionSecret, err)
	}
}

func TestModelProviderCredentialsUseEnvironmentThenModelsEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.env"), []byte("LLM_API_KEY=file-key\nZHIPU_API_KEY=file-preset-key\nZHIPU_BASE_URL=https://preset.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("LLM_API_KEY", "environment-key")
	t.Setenv("ZHIPU_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelCredential("LLM_API_KEY") != "environment-key" || cfg.ModelCredential("ZHIPU_BASE_URL") != "https://preset.example" {
		t.Fatal("model credentials did not follow environment and models.env precedence")
	}
	if cfg.ModelCredential("ZHIPU_API_KEY") != "" {
		t.Fatal("an explicitly empty process credential unexpectedly fell back to models.env")
	}
}

func TestIngestCredentialsAndRateLimitConfiguration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.env"), []byte("INGEST_TOKEN=ingest-token-from-file-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("INGEST_RATE_LIMIT", "")
	previousToken, hadToken := os.LookupEnv("INGEST_TOKEN")
	if err := os.Unsetenv("INGEST_TOKEN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadToken {
			_ = os.Setenv("INGEST_TOKEN", previousToken)
		} else {
			_ = os.Unsetenv("INGEST_TOKEN")
		}
	})
	cfg, err := Load()
	if err != nil || cfg.IngestToken != "ingest-token-from-file-123" || cfg.IngestRateLimit != 0 {
		t.Fatalf("empty environment override/default changed: token=%q limit=%d err=%v", cfg.IngestToken, cfg.IngestRateLimit, err)
	}
	t.Setenv("INGEST_TOKEN", "")
	cfg, err = Load()
	if err != nil || cfg.IngestToken != "" {
		t.Fatalf("an empty environment token must override the credential file: %q %v", cfg.IngestToken, err)
	}
	t.Setenv("INGEST_TOKEN", "ingest-token-from-env-123")
	t.Setenv("INGEST_RATE_LIMIT", "10.9")
	cfg, err = Load()
	if err != nil || cfg.IngestToken != "ingest-token-from-env-123" || cfg.IngestRateLimit != 10 {
		t.Fatalf("environment ingest settings changed: token=%q limit=%d err=%v", cfg.IngestToken, cfg.IngestRateLimit, err)
	}
	t.Setenv("INGEST_RATE_LIMIT", "-1")
	cfg, err = Load()
	if err != nil || cfg.IngestRateLimit != 0 {
		t.Fatalf("negative rate limit was not clamped: %d %v", cfg.IngestRateLimit, err)
	}
}

func TestCollectorCredentialsUseCollectorsGroupAndEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "collectors.env"), []byte("DAJIALA_KEY=file-dajiala-key\nDAJIALA_BASE_URL=https://file.dajiala.example\nSOCIALDATA_API_KEY=file-socialdata-key\nSOCIALDATA_BASE_URL=https://file.socialdata.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("DAJIALA_KEY", "")
	t.Setenv("DAJIALA_BASE_URL", "")
	t.Setenv("SOCIALDATA_API_KEY", "")
	t.Setenv("SOCIALDATA_BASE_URL", "")
	cfg, err := Load()
	if err != nil || cfg.DajialaKey != "" || cfg.DajialaBaseURL != "" || cfg.SocialDataKey != "" || cfg.SocialDataBaseURL != "" {
		t.Fatalf("explicit empty process credentials must override collectors.env: dajiala=%q socialdata=%q err=%v", cfg.DajialaKey, cfg.SocialDataKey, err)
	}
	_ = os.Unsetenv("DAJIALA_KEY")
	_ = os.Unsetenv("DAJIALA_BASE_URL")
	_ = os.Unsetenv("SOCIALDATA_API_KEY")
	_ = os.Unsetenv("SOCIALDATA_BASE_URL")
	cfg, err = Load()
	if err != nil || cfg.DajialaKey != "file-dajiala-key" || cfg.DajialaBaseURL != "https://file.dajiala.example" || cfg.SocialDataKey != "file-socialdata-key" || cfg.SocialDataBaseURL != "https://file.socialdata.example" {
		t.Fatalf("collector credentials were not loaded: dajiala=%q socialdata=%q err=%v", cfg.DajialaKey, cfg.SocialDataKey, err)
	}
	t.Setenv("DAJIALA_KEY", "environment-dajiala-key")
	t.Setenv("DAJIALA_BASE_URL", "https://env.dajiala.example/")
	t.Setenv("SOCIALDATA_API_KEY", "environment-socialdata-key")
	t.Setenv("SOCIALDATA_BASE_URL", "https://env.socialdata.example/")
	cfg, err = Load()
	if err != nil || cfg.DajialaKey != "environment-dajiala-key" || cfg.DajialaBaseURL != "https://env.dajiala.example" || cfg.SocialDataKey != "environment-socialdata-key" || cfg.SocialDataBaseURL != "https://env.socialdata.example" {
		t.Fatalf("environment collector credentials did not override file: dajiala=%q socialdata=%q err=%v", cfg.DajialaKey, cfg.SocialDataKey, err)
	}
}

func TestRetentionForwardingRequiresFlagAndConfiguredChat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integrations.env"), []byte("FEISHU_INTERNAL_CHAT_ID=chat-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("FEISHU_INTERNAL_ENABLED", "")
	t.Setenv("FEISHU_INTERNAL_CHAT_ID", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FeishuInternalEnabled || cfg.FeishuInternalChatID != "" {
		t.Fatalf("disabled internal forwarding or explicit empty chat changed: %#v", cfg)
	}
	t.Setenv("FEISHU_INTERNAL_CHAT_ID", "chat-from-env")
	t.Setenv("FEISHU_INTERNAL_ENABLED", "true")
	cfg, err = Load()
	if err != nil || !cfg.FeishuInternalEnabled || cfg.FeishuInternalChatID != "chat-from-env" {
		t.Fatalf("configured internal forwarding = %#v, %v", cfg, err)
	}
}

func TestAlertChatUsesGroupedCredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integrations.env"), []byte("FEISHU_ALERT_CHAT_ID=alert-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	t.Setenv("FEISHU_ALERT_CHAT_ID", "")
	cfg, err := Load()
	if err != nil || cfg.FeishuAlertChatID != "" {
		t.Fatalf("explicitly empty alert chat must override the grouped credential: chat=%q err=%v", cfg.FeishuAlertChatID, err)
	}
	t.Setenv("FEISHU_ALERT_CHAT_ID", "alert-from-env")
	cfg, err = Load()
	if err != nil || cfg.FeishuAlertChatID != "alert-from-env" {
		t.Fatalf("alert chat environment value was not used: chat=%q err=%v", cfg.FeishuAlertChatID, err)
	}
}

func TestBackupStoreCredentialsUseIntegrationGroupAndEnvironmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integrations.env"), []byte("DB_BACKUP_STORE_SECRET_ID=file-id\nDB_BACKUP_STORE_SECRET_KEY=file-key\nDB_BACKUP_STORE_BUCKET=file-bucket\nDB_BACKUP_STORE_REGION=file-region\nDB_BACKUP_STORE_DOMAIN=file.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHOT_CREDENTIALS_DIR", dir)
	for _, key := range []string{"DB_BACKUP_STORE_SECRET_ID", "DB_BACKUP_STORE_SECRET_KEY", "DB_BACKUP_STORE_BUCKET", "DB_BACKUP_STORE_REGION", "DB_BACKUP_STORE_DOMAIN"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil || cfg.BackupStoreSecretID != "" {
		t.Fatalf("explicitly empty environment values must override integration file values: %#v %v", cfg, err)
	}
	t.Setenv("DB_BACKUP_STORE_SECRET_ID", "env-id")
	t.Setenv("DB_BACKUP_STORE_SECRET_KEY", "env-key")
	t.Setenv("DB_BACKUP_STORE_BUCKET", "env-bucket")
	t.Setenv("DB_BACKUP_STORE_REGION", "env-region")
	t.Setenv("DB_BACKUP_STORE_DOMAIN", "")
	cfg, err = Load()
	if err != nil || cfg.BackupStoreSecretID != "env-id" || cfg.BackupStoreBucket != "env-bucket" || cfg.BackupStoreDomain != "" {
		t.Fatalf("backup store credential precedence changed: %#v %v", cfg, err)
	}
}

func TestProductionRuntimeRejectsMissingSecretsAndDevLogin(t *testing.T) {
	var developmentVariables map[string]*string
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(key, "DEV_AUTH_") {
			value := os.Getenv(key)
			developmentVariables = appendEnv(developmentVariables, key, value)
			_ = os.Unsetenv(key)
		}
	}
	t.Cleanup(func() {
		for key, value := range developmentVariables {
			if value == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *value)
			}
		}
	})
	cfg := Config{EnvironmentName: "production", SessionSecret: "safe-random-production-secret"}
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("valid production auth config rejected: %v", err)
	}
	cfg.SessionSecret = "placeholder"
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("production accepted a placeholder session secret")
	}
	cfg.SessionSecret = "safe-random-production-secret"
	t.Setenv("DEV_AUTH_ROLE", "admin")
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("production accepted a development admin bypass")
	}
	if err := (Config{EnvironmentName: "development"}).ValidateRuntime(); err != nil {
		t.Fatalf("development should not require production secrets: %v", err)
	}
}

func appendEnv(values map[string]*string, key, value string) map[string]*string {
	if values == nil {
		values = make(map[string]*string)
	}
	values[key] = &value
	return values
}

/** TestSitePublicDirPreservesCustomPathsAndResolvesTheBundledDirectory checks the OpenAPI location. */
func TestSitePublicDirPreservesCustomPathsAndResolvesTheBundledDirectory(t *testing.T) {
	t.Setenv("SITE_PUBLIC_DIR", "")
	got := sitePublicDir()
	if !strings.HasSuffix(filepath.ToSlash(got), "backend-go/config/public") {
		t.Fatalf("former bundle path was not relocated: %s", got)
	}
	if _, err := os.Stat(filepath.Join(got, "openapi-v1.json")); err != nil {
		t.Fatalf("bundled API document is missing: %v", err)
	}
	dir := t.TempDir()
	t.Setenv("SITE_PUBLIC_DIR", dir)
	if got := sitePublicDir(); got != dir {
		t.Fatalf("custom API document directory changed: got %s, want %s", got, dir)
	}
}
