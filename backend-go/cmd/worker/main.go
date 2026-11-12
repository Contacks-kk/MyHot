package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/topics"
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
	select {
	case <-client.Stopped():
		return
	case <-ctx.Done():
	}
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
}
