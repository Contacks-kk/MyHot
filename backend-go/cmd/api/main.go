package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aihot/backend-go/internal/admincontent"
	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/feedback"
	"aihot/backend-go/internal/httpapi"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/operationsalerts"
	"aihot/backend-go/internal/reports"
	"aihot/backend-go/internal/topics"
)

func main() {
	// 获取配置信息
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.ValidateRuntime(); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := jobs.MigrateRiver(ctx, pool); err != nil {
		log.Fatal(err)
	}
	orm, err := db.OpenGORM(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.CloseGORM(orm)

	// Require Redis for user session authentication and attach shared client before serving traffic.
	if strings.TrimSpace(cfg.RedisURL) == "" {
		log.Fatal("Refusing to start API: REDIS_URL is required for user sessions")
	}
	redisClient, err := db.OpenRedis(ctx, cfg.RedisURL, cfg.RedisCAFile)
	if err != nil {
		log.Fatalf("Refusing to start API: failed to connect to Redis: %v", err)
	}
	defer redisClient.Close()

	adminruns.StartHeartbeat(ctx, orm, "api")
	workerWatchdog := operationsalerts.NewWorkerWatchdog(operationsalerts.NewRepository(orm), feedback.NewFeishuForwarder(cfg.FeishuAppID, cfg.FeishuAppSecret), operationsalerts.Config{
		FeishuInternalEnabled: cfg.FeishuInternalEnabled,
		AlertChatID:           cfg.FeishuAlertChatID,
		InternalChatID:        cfg.FeishuInternalChatID,
		EnvironmentName:       cfg.EnvironmentName,
	})
	go workerWatchdog.Run(ctx)
	entities, err := interests.LoadEntities()
	if err != nil {
		log.Fatal(err)
	}
	categories, err := interests.LoadCategories()
	if err != nil {
		log.Fatal(err)
	}
	publicationSettings, err := interests.LoadPublicationSettings()
	if err != nil {
		log.Fatal(err)
	}
	categoryKeys := make([]string, 0, len(categories))
	categoryLabels := make(map[string]string, len(categories))
	categoryFeedLabels := make(map[string]string, len(categories))
	reportCorrection := admincontent.ReportCorrectionConfig{SectionByCategory: make(map[string]string, len(categories))}
	sectionSeen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		categoryKeys = append(categoryKeys, category.Key)
		categoryLabels[category.Key] = category.Label
		categoryFeedLabels[category.Key] = category.FeedLabel
		reportCorrection.SectionByCategory[category.Key] = category.Section
		if _, ok := sectionSeen[category.Section]; !ok {
			sectionSeen[category.Section] = struct{}{}
			reportCorrection.SectionOrder = append(reportCorrection.SectionOrder, category.Section)
		}
	}
	reportingConfig, err := reports.LoadReportingConfig()
	if err != nil {
		log.Fatal(err)
	}
	var releaseUnit *string
	if reportingConfig.Release != nil {
		releaseUnit = &reportingConfig.Release.Unit
		reportCorrection.ReleaseCategory,
			reportCorrection.ReleaseTag = reportingConfig.Release.Category,
			reportingConfig.Release.Tag
	}
	modelCatalog, err := adminmodels.LoadCatalog()
	if err != nil {
		log.Fatal(err)
	}
	topicGroups, topicDefinitions, err := topics.LoadCatalog(entities)
	if err != nil {
		log.Fatal(err)
	}
	api := httpapi.New(cfg, pool, orm, entities, httpapi.RuntimeCatalogs{
		CategoryKeys:       categoryKeys,
		CategoryLabels:     categoryLabels,
		CategoryFeedLabels: categoryFeedLabels,
		ReleaseUnit:        releaseUnit,
		ReportCorrection:   reportCorrection,
		Publication:        publicationSettings,
		Models:             modelCatalog,
		TopicGroups:        topicGroups,
		Topics:             topicDefinitions})
	go api.Accounts.RunVerificationCleanup(ctx)
	api.AttachAccountRedis(redisClient)
	api.AttachMarketRedis(ctx, redisClient)
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20}
	go func() {
		<-ctx.Done()
		api.ShutdownMCP()
		api.ShutdownMarketWS()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	log.Printf("MyHOT Go API listening on %s", cfg.Addr)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
