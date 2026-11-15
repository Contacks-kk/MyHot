package httpapi

import (
	"context"
	"os"
	"regexp"
	"strings"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/adminauth"
	"aihot/backend-go/internal/admincontent"
	"aihot/backend-go/internal/admindeliveries"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/adminops"
	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/adminselectbench"
	"aihot/backend-go/internal/adminsettings"
	"aihot/backend-go/internal/adminsources"
	"aihot/backend-go/internal/agentmd"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/discovery"
	"aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/feeddiscovery"
	"aihot/backend-go/internal/feeds"
	"aihot/backend-go/internal/health"
	"aihot/backend-go/internal/ingest"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/mcpapi"
	"aihot/backend-go/internal/media"
	"aihot/backend-go/internal/personal"
	publicationdomain "aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/receiptrecovery"
	"aihot/backend-go/internal/siteinfo"
	"aihot/backend-go/internal/topics"
	"aihot/backend-go/internal/usersources"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type RuntimeCatalogs struct {
	CategoryKeys       []string
	CategoryLabels     map[string]string
	CategoryFeedLabels map[string]string
	ReportCorrection   admincontent.ReportCorrectionConfig
	Publication        interests.PublicationSettings
	Models             adminmodels.Catalog
	TopicGroups        []topics.Group
	Topics             []topics.Definition
}

/** New assembles the API server and its services. */
func New(cfg config.Config, db *pgxpool.Pool, orm *gorm.DB, entities map[string]interests.Entity, catalogs ...RuntimeCatalogs) *Server {
	changelogPath := cfg.SiteChangelogPath
	if changelogPath == "" {
		changelogPath = "shared/site/changelog.json"
	}
	server := &Server{Config: cfg, ContactDir: cfg.SiteContactDir, Entities: entities, limiter: newRateLimiter(), Health: health.NewService(health.NewRepository(orm)), ConnectionTestKind: jobs.ConnectionTestArgs{}.Kind()}
	imageService := media.NewService(media.Config{
		DataDir: cfg.DataDir, SignSecret: cfg.ImageProxySignSecret,
		MinuteBudgetBytes: cfg.ImageProxyMinuteBudgetBytes, DailyBudgetBytes: cfg.ImageProxyDailyBudgetBytes,
		EgressProxyURL: cfg.EgressProxyURL, DirectFetchHosts: cfg.DirectFetchHosts,
		SiteURL: cfg.SiteURL, CrawlerName: cfg.SiteCrawlerName,
	}, media.NewRepository(orm), nil)
	var enqueueMediaPrepare media.Enqueuer
	if cfg.GoMediaPrepareEnabled {
		enqueueMediaPrepare = func(ctx context.Context, rawURL, mode string) error {
			return jobs.InsertMediaPrepare(ctx, db, rawURL, mode)
		}
	}
	server.MediaProxy = media.NewHandler(imageService, enqueueMediaPrepare)
	var categoryKeys []string
	var modelCatalog adminmodels.Catalog
	reportCorrection := admincontent.ReportCorrectionConfig{}
	publicationSettings := interests.PublicationSettings{Subject: "AI", PublicCategories: interests.PublicCategories{Merge: map[string]string{}, FeedLabels: map[string]string{}}}
	feedCategories := make([]feeds.Category, 0)
	feedLabels := map[string]string{}
	if len(catalogs) > 0 {
		categoryKeys = catalogs[0].CategoryKeys
		modelCatalog = catalogs[0].Models
		server.CategoryLabels = catalogs[0].CategoryLabels
		feedLabels = catalogs[0].CategoryFeedLabels
		if catalogs[0].Publication.Subject != "" {
			publicationSettings = catalogs[0].Publication
		}
		reportCorrection = catalogs[0].ReportCorrection
	}
	server.CategoryOrder = append([]string(nil), categoryKeys...)
	server.ReportSubject = publicationSettings.Subject
	server.CategoryKeys = make(map[string]struct{}, len(categoryKeys))
	for _, key := range categoryKeys {
		server.CategoryKeys[key] = struct{}{}
		feedCategories = append(feedCategories, feeds.Category{Key: key, Label: server.CategoryLabels[key], FeedLabel: feedLabels[key]})
	}
	server.SiteInfo = siteinfo.NewService(changelogPath, siteinfo.OpenAPIConfig{
		Path: cfg.SitePublicDir + string(os.PathSeparator) + "openapi-v1.json", SiteName: cfg.SiteName, SiteURL: cfg.SiteURL,
		Description: cfg.SiteDescription, Tagline: cfg.SiteTagline, Locale: cfg.SiteLocale, Version: cfg.SiteInterfaceVersion,
		DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly, Categories: categoryKeys,
	})
	server.Accounts = account.NewService(account.NewRepository(orm), cfg, accountMailer{server: server})
	server.AdminAuth = adminauth.NewService(adminauth.NewRepository(orm), cfg, nil)
	materialService := materials.NewService(materials.NewRepository(orm))
	server.Ingest = ingest.NewService(ingest.NewRepository(orm), materialService)
	server.AdminContent = admincontent.NewService(admincontent.NewRepository(orm))
	if transactionClient, err := jobs.NewTransactionClient(db); err == nil {
		publicationRepository := publicationdomain.NewRepository(db, entities, publicationSettings, cfg.SiteName, cfg.SiteURL)
		publicationRepository.SetEffects(jobs.NewPublicationEffects(transactionClient, cfg.StoryDigestEnabled))
		server.AdminContent.SetCommands(admincontent.NewCommandsRepository(db, publicationRepository, transactionClient, cfg.StoryDigestEnabled, reportCorrection), categoryKeys)
	}
	server.AdminDeliveries = admindeliveries.NewService(admindeliveries.NewRepository(orm), admindeliveries.Config{
		ContentPushEnabled: cfg.FeishuContentPushEnabled,
		WebhookURLs: map[string]string{
			"FEISHU_PUSH_WEBHOOK_URL":        cfg.FeishuPushWebhookURL,
			"FEISHU_PUSH_MIRROR_WEBHOOK_URL": cfg.FeishuPushMirrorWebhookURL,
		},
		SiteName: cfg.SiteName, SiteURL: cfg.SiteURL, CategoryLabels: server.CategoryLabels,
	}, admindeliveries.HTTPWebhookSender{})
	server.ReceiptRecovery = receiptrecovery.NewService(receiptrecovery.NewRepository(orm))
	server.AdminOps = adminops.NewService(adminops.NewRepository(orm))
	server.AdminRuns = adminruns.NewService(adminruns.NewRepository(orm))
	server.AdminSources = adminsources.NewService(adminsources.NewRepository(orm))
	server.UserSources = usersources.NewService(orm)
	server.UserSources.SetMaterials(materialService)
	server.UserSources.SetCollectorEnabled(cfg.CollectorEnabled)
	server.UserSources.SetEnqueuer(func(ctx context.Context, sourceID, sourceKind string) (int64, error) {
		return jobs.InsertManualSourceFetch(ctx, db, sourceID, sourceKind)
	})
	server.FeedDiscovery = feeddiscovery.NewService()
	server.AdminSelectBench = adminselectbench.NewService(adminselectbench.NewRepository(orm))
	server.AdminSettings = adminsettings.NewService(adminsettings.NewRepository(orm, bundledContactDefaults(cfg.SiteContactDir)), adminsettings.Config{FeishuContentPushEnabled: cfg.FeishuContentPushEnabled, DataDir: cfg.DataDir})
	server.AdminModels = adminmodels.NewService(adminmodels.NewRepository(orm), modelCatalog, cfg.ModelEnvironment)
	server.Feedback = feedback.NewService(feedback.NewRepository(orm), feedback.Config{
		DataDir: cfg.DataDir, SessionSecret: cfg.SessionSecret, FeishuInternalEnabled: cfg.FeishuInternalEnabled,
		FeishuInternalChatID: cfg.FeishuInternalChatID,
	}, feedback.NewFeishuForwarder(cfg.FeishuAppID, cfg.FeishuAppSecret))
	server.Feedback.SetForwardEnqueuer(func(ctx context.Context, id int64) error {
		return jobs.InsertFeedbackForward(ctx, db, id)
	})
	server.Personal = personal.NewService(personal.NewRepository(orm), cfg, entities)
	server.EnqueueManualDaily = func(ctx context.Context, userID int64) (int64, error) {
		return jobs.InsertManualDaily(ctx, db, userID)
	}
	publicRepository := publiccontent.NewRepository(orm)
	server.PublicContent = publiccontent.NewService(publicRepository, entities)
	var sitemapTopics []topics.Definition
	if len(catalogs) > 0 {
		sitemapTopics = catalogs[0].Topics
		server.TopicDefinitions = append([]topics.Definition(nil), catalogs[0].Topics...)
		server.PublicContent.SetTopics(catalogs[0].Topics)
	}
	server.Sitemap = publiccontent.NewSitemapService(publicRepository, publiccontent.SitemapConfig{
		SiteURL: cfg.SiteURL, DataDir: cfg.DataDir, Topics: sitemapTopics,
	})
	server.Feeds = feeds.NewService(feeds.NewGormRepository(orm), server.PublicContent, feeds.Config{
		SiteURL: cfg.SiteURL, SiteName: cfg.SiteName, Subject: publicationSettings.Subject,
		Categories: feedCategories, CategoryMerge: publicationSettings.PublicCategories.Merge, FeedLabels: publicationSettings.PublicCategories.FeedLabels,
		AllLeavesOut: publicationSettings.AllFeedLeavesOut, DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly,
		ReportMeasure: cfg.SiteReportEntryMeasure, ReportNoun: cfg.SiteReportEntryNoun, ImageProxySecret: cfg.ImageProxySignSecret,
	})
	server.Agent = agentmd.NewService(server.PublicContent, cfg.SiteName, cfg.SiteURL, server.CategoryLabels)
	server.Agent.SetReportTerms(cfg.SiteReportEntryMeasure, cfg.SiteReportEntryNoun)
	server.Agent.SetCategoryOrder(server.CategoryOrder)
	server.MCPHandler = mcpapi.NewService(server.PublicContent, mcpapi.Config{
		Prefix: cfg.SiteMCPPrefix, Version: cfg.SiteInterfaceVersion, SiteName: cfg.SiteName, SiteURL: cfg.SiteURL,
		DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly,
		ReportMeasure: cfg.SiteReportEntryMeasure, ReportNoun: cfg.SiteReportEntryNoun,
		Categories: server.CategoryLabels, CategoryOrder: server.CategoryOrder,
	}).Handler()
	if len(catalogs) > 0 && len(catalogs[0].TopicGroups) > 0 && len(catalogs[0].Topics) > 0 {
		server.TopicGroups = append([]topics.Group(nil), catalogs[0].TopicGroups...)
		server.Topics = topics.NewService(publicRepository, catalogs[0].TopicGroups, catalogs[0].Topics, topics.RuntimeConfig{
			ImageProxySecret: cfg.ImageProxySignSecret,
			XPostIsFullText:  cfg.XPostIsFullText,
		})
	}
	feedDiscovery, categoryKeys := server.Feeds.DiscoveryFeeds()
	discoveryFeeds := make(map[string]discovery.Feed, len(feedDiscovery))
	for kind, feed := range feedDiscovery {
		discoveryFeeds[kind] = discovery.Feed{Path: feed.Path, Description: feed.Description}
	}
	server.Discovery = discovery.NewService(publicRepository, server.Topics, discovery.Config{
		SiteName: cfg.SiteName, SiteURL: cfg.SiteURL, Description: cfg.SiteDescription, Intro: cfg.SiteLLMSIntro,
		Version: cfg.SiteInterfaceVersion, Subject: publicationSettings.Subject, DailyTime: cfg.SiteEditionDaily,
		WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly, ReportNoun: cfg.SiteReportEntryNoun,
		MCPPrefix: cfg.SiteMCPPrefix, CategoryKeys: categoryKeys, TopicGroups: server.TopicGroups, Feeds: discoveryFeeds,
		ContactEmail: cfg.SiteContactEmail, TermsCovers: cfg.SiteTermsCovers, PrivacyCovers: cfg.SitePrivacyCovers,
		TermsLicense: cfg.SiteTermsLLMSLicense, RatePerMinute: cfg.SiteAccessRatePerMinute,
	})
	server.EnqueueConnectionTest = func(ctx context.Context, request personal.ConnectionTest) error {
		return jobs.InsertConnectionTest(ctx, db, jobs.ConnectionTestArgs{UserID: request.UserID, ReceiptID: request.ReceiptID, BaseURL: request.BaseURL, Model: request.Model, APIKeyCiphertext: request.APIKeyCiphertext})
	}
	return server
}

var bundledContactImage = regexp.MustCompile(`^[\w.-]+\.(?:png|jpe?g|webp)$`)

func bundledContactDefaults(directory string) adminsettings.Contact {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return adminsettings.Contact{}
	}
	var defaults adminsettings.Contact
	for _, entry := range entries {
		if entry.IsDir() || !bundledContactImage.MatchString(entry.Name()) {
			continue
		}
		name := entry.Name()
		if defaults.WechatQR == nil && strings.HasPrefix(name, "qr-wechat") {
			value := "/contact/" + name
			defaults.WechatQR = &value
		}
		if defaults.FeishuQR == nil && strings.HasPrefix(name, "qr-feishu") {
			value := "/contact/" + name
			defaults.FeishuQR = &value
		}
	}
	return defaults
}

type accountMailer struct{ server *Server }

func (m accountMailer) Send(to, subject, body string) error {
	return m.server.mailer().Send(to, subject, body)
}
