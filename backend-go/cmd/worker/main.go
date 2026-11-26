package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"aihot/backend-go/internal/adminruns"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/market"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/topics"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	orm, err := db.OpenGORM(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.CloseGORM(orm)

	// Connect Redis optionally for market collector leader locking, hot window cache, and Pub/Sub.
	var redisClient *redis.Client
	if cfg.RedisURL != "" {
		redisClient, err = db.OpenRedis(ctx, cfg.RedisURL, cfg.RedisCAFile)
		if err != nil {
			log.Printf("warning: Redis unavailable (%v); market collector running without Redis cache", err)
		} else {
			defer redisClient.Close()
		}
	}

	adminruns.StartHeartbeat(ctx, orm, "worker")
	entities, err := interests.LoadEntities()
	if err != nil {
		log.Fatal(err)
	}
	publication, err := interests.LoadPublicationSettings()
	if err != nil {
		log.Fatal(err)
	}
	_, topicDefinitions, err := topics.LoadCatalog(entities)
	if err != nil {
		log.Fatal(err)
	}

	// 包级构造创建river.Client
	client, err := jobs.NewClient(ctx, pool, orm, cfg, entities, publication, provider.Chat, topicDefinitions)
	if err != nil {
		log.Fatal(err)
	}

	// client.start启动worker
	if err = client.Start(context.WithoutCancel(ctx)); err != nil {
		log.Fatal(err)
	}

	// Start the real-time market collector only when the MARKET_DATA_ENABLED safety valve is true.
	var marketWG sync.WaitGroup
	if cfg.MarketDataEnabled {
		marketProvider := market.NewProvider(market.ProviderConfig{
			Name:          cfg.MarketProvider,
			TwelveDataKey: cfg.MarketTwelveDataAPIKey,
			FinnhubKey:    cfg.MarketFinnhubAPIKey,
		}, slog.Default())
		marketCollector := market.NewCollector(
			market.NewRepository(orm, cfg.MarketProvider),
			market.NewRedisStore(redisClient, cfg.MarketProvider),
			marketProvider,
			slog.Default(),
		)
		marketWG.Add(1)
		go func() {
			defer marketWG.Done()
			marketCollector.Run(ctx)
		}()
	}

	select {
	case <-client.Stopped():
		marketWG.Wait()
		return
	case <-ctx.Done():
	}

	// Block new paid calls before River drains the jobs already in flight.
	receipts.BeginWorkerShutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 195*time.Second)
	defer cancel()
	if err := client.Stop(shutdownCtx); err != nil {
		log.Print("worker graceful shutdown timed out; cancelling unfinished jobs")
		forceCtx, forceCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer forceCancel()
		if err := client.StopAndCancel(forceCtx); err != nil {
			log.Print("worker shutdown did not finish before timeout")
		}
	}
	marketWG.Wait()
}
