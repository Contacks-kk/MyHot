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
	"aihot/backend-go/internal/adminlabels"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/adminops"
	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/adminselectbench"
	"aihot/backend-go/internal/adminsettings"
	"aihot/backend-go/internal/adminsources"
	"aihot/backend-go/internal/agentmd"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/discovery"
	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/feeddiscovery"
	"aihot/backend-go/internal/feeds"
	"aihot/backend-go/internal/health"
	"aihot/backend-go/internal/ingest"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/market"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/mcpapi"
	"aihot/backend-go/internal/media"
	"aihot/backend-go/internal/personal"
	publicationdomain "aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/receiptrecovery"
	"aihot/backend-go/internal/siteinfo"
	"aihot/backend-go/internal/tagging"
	"aihot/backend-go/internal/topics"
	"aihot/backend-go/internal/usersources"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type RuntimeCatalogs struct {
	CategoryKeys       []string
	CategoryLabels     map[string]string
	CategoryFeedLabels map[string]string
	ReleaseUnit        *string
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
		changelogPath = "backend-go/config/changelog.json"
	}
	server := &Server{
		Config:             cfg,
		ContactDir:         cfg.SiteContactDir,
		Entities:           entities,
		limiter:            newRateLimiter(),
		Health:             health.NewService(health.NewRepository(orm)),
		ConnectionTestKind: jobs.ConnectionTestArgs{}.Kind(),
	}
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
	server.PublicCategoryMembers = make(map[string][]string, len(categoryKeys))
	server.PublicCategoryLabels = make(map[string]string, len(categoryKeys))
	for _, key := range categoryKeys {
		publicKey := key
		if merged, exists := publicationSettings.PublicCategories.Merge[key]; exists {
			publicKey = merged
		}
		server.PublicCategoryMembers[publicKey] = append(server.PublicCategoryMembers[publicKey], key)
	}
	for _, key := range categoryKeys {
		if _, merged := publicationSettings.PublicCategories.Merge[key]; merged {
			continue
		}
		server.PublicCategoryOrder = append(server.PublicCategoryOrder, key)
		label := publicationSettings.PublicCategories.FeedLabels[key]
		if label == "" {
			label = feedLabels[key]
		}
		if label == "" {
			label = server.CategoryLabels[key]
		}
		server.PublicCategoryLabels[key] = label
	}
	var topicGroups []interests.CatalogTopicGroup
	var topicEntries []interests.CatalogTopic
	if len(catalogs) > 0 {
		for _, group := range catalogs[0].TopicGroups {
			topicGroups = append(topicGroups, interests.CatalogTopicGroup{
				Key:   group.Key,
				Name:  group.Name,
				Blurb: group.Blurb,
			})
		}
		for _, topic := range catalogs[0].Topics {
			topicEntries = append(topicEntries, interests.CatalogTopic{
				Slug:       topic.Slug,
				Name:       topic.Name,
				Group:      topic.Group,
				Definition: topic.Definition,
			})
		}
	}
	var releaseUnit *string
	if len(catalogs) > 0 {
		releaseUnit = catalogs[0].ReleaseUnit
	}
	server.Catalog = interests.NewCatalogService(categoryKeys, server.CategoryLabels, feedLabels, publicationSettings, topicGroups, topicEntries, releaseUnit)
	server.CategoryKeys = make(map[string]struct{}, len(categoryKeys))
	for _, key := range categoryKeys {
		server.CategoryKeys[key] = struct{}{}
		feedCategories = append(feedCategories, feeds.Category{Key: key, Label: server.CategoryLabels[key], FeedLabel: feedLabels[key]})
	}
	server.SiteInfo = siteinfo.NewService(changelogPath, siteinfo.OpenAPIConfig{
		Path: cfg.SitePublicDir + string(os.PathSeparator) + "openapi-v1.json", SiteName: cfg.SiteName, SiteURL: cfg.SiteURL,
		Description: cfg.SiteDescription, Tagline: cfg.SiteTagline, Locale: cfg.SiteLocale, Version: cfg.SiteInterfaceVersion,
		DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly, Categories: server.PublicCategoryOrder,
	})
	server.Accounts = account.NewService(account.NewRepository(orm), cfg, accountMailer{server: server})
	server.AdminAuth = adminauth.NewService(adminauth.NewRepository(orm), cfg, nil)
	transactionClient, transactionErr := jobs.NewTransactionClient(db)
	publicationRepository := publicationdomain.NewRepository(db, entities, publicationSettings, cfg.SiteName, cfg.SiteURL)
	if transactionErr == nil {
		publicationRepository.SetEffects(jobs.NewPublicationEffects(transactionClient, cfg.StoryDigestEnabled))
	}
	materialService := materials.NewService(materials.NewRepository(orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(db, publicationRepository))
	server.Ingest = ingest.NewService(ingest.NewRepository(orm), materialService)
	server.AdminContent = admincontent.NewService(admincontent.NewRepository(orm))
	if transactionErr == nil {
		commands := admincontent.NewCommandsRepository(db, publicationRepository, transactionClient, cfg.StoryDigestEnabled, reportCorrection)
		server.AdminContent.SetCommands(commands, categoryKeys)
		server.AdminContent.SetXEncodingRepairRepository(commands)
		server.AdminContent.SetPublicationDateCorrectionRepository(commands)
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
	vocabulary, _ := editorial.LoadVocabulary()
	server.AdminLabels = adminlabels.NewService(adminlabels.NewRepository(orm, vocabulary))
	server.UserSources = usersources.NewService(orm, cfg.SourceSchedule)
	server.UserSources.SetImageProxySecret(cfg.ImageProxySignSecret)
	server.UserSources.SetMaterials(materialService)
	server.UserSources.SetCollectorEnabled(cfg.CollectorEnabled)
	server.UserSources.SetEnqueuer(func(ctx context.Context, sourceID, sourceKind string, userID int64) (int64, error) {
		return jobs.InsertManualSourceFetch(ctx, db, sourceID, sourceKind, userID)
	})
	server.UserSources.SetInitialEnqueuer(func(ctx context.Context, sourceID string) error {
		return jobs.InsertInitialSourceFetch(ctx, db, sourceID)
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
	server.InterestLabels = tagging.NewService(tagging.NewRepository(orm), nil, nil)
	server.EnqueueManualDaily = func(ctx context.Context, userID int64) (int64, error) {
		return jobs.InsertManualDaily(ctx, db, userID)
	}
	publicRepository := publiccontent.NewRepository(orm)
	server.PublicContent = publiccontent.NewService(publicRepository, entities)
	server.PublicContent.SetPublicCategoryMembers(server.PublicCategoryMembers)
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
	server.Agent = agentmd.NewService(server.PublicContent, cfg.SiteName, cfg.SiteURL, server.PublicCategoryLabels)
	server.Agent.SetReportTerms(cfg.SiteReportEntryMeasure, cfg.SiteReportEntryNoun)
	server.Agent.SetCategoryOrder(server.PublicCategoryOrder)
	server.MCPService = mcpapi.NewService(server.PublicContent, mcpapi.Config{
		Prefix: cfg.SiteMCPPrefix, Version: cfg.SiteInterfaceVersion, SiteName: cfg.SiteName, SiteURL: cfg.SiteURL,
		DailyTime: cfg.SiteEditionDaily, WeeklyTime: cfg.SiteEditionWeekly, MonthlyTime: cfg.SiteEditionMonthly,
		ReportMeasure: cfg.SiteReportEntryMeasure, ReportNoun: cfg.SiteReportEntryNoun,
		Categories: server.PublicCategoryLabels, CategoryOrder: server.PublicCategoryOrder,
	})
	server.MCPHandler = server.MCPService.Handler()
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
	server.MarketHub = market.NewHub(nil, server.Logger)
	server.Market = market.NewService(market.NewRepository(orm), nil, server.PublicContent, marketServiceConfig(cfg))
	return server
}

func marketServiceConfig(cfg config.Config) market.ServiceConfig {
	providerMode := "live"
	switch cfg.MarketProvider {
	case "twelvedata":
		if cfg.MarketTwelveDataAPIKey == "" {
			providerMode = "mock"
		}
	case "finnhub":
		if cfg.MarketFinnhubAPIKey == "" {
			providerMode = "mock"
		}
	default:
		providerMode = "mock"
	}

	return market.ServiceConfig{
		Enabled:         cfg.MarketDataEnabled,
		ProviderName:    cfg.MarketProvider,
		ProviderMode:    providerMode,
		ImgProxySecret:  cfg.ImageProxySignSecret,
		XPostIsFullText: cfg.XPostIsFullText,
	}
}

/** AttachMarketRedis binds Redis caching and Pub/Sub fan-out to market services and starts the hub consumer. */
func (s *Server) AttachMarketRedis(ctx context.Context, client *redis.Client) {
	if s == nil || client == nil {
		return
	}

	store := market.NewRedisStore(client)
	if s.Market != nil {
		s.Market.SetRedis(store)
	}
	s.MarketHub = market.NewHub(store, s.Logger)
	go s.MarketHub.Run(ctx)
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
