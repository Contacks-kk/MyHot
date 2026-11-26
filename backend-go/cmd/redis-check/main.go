package main

import (
	"context"
	"fmt"
	"log"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
)

/** main verifies the configured Redis connection without opening other services. */
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	client, err := db.OpenRedis(context.Background(), cfg.RedisURL, cfg.RedisCAFile)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	fmt.Println("Redis connection OK (PING)")
}
