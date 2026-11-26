package db

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

/** OpenRedis verifies Redis connectivity and returns a client owned by the caller. */
func OpenRedis(ctx context.Context, address, caFile string) (*redis.Client, error) {
	if address == "" {
		return nil, errors.New("REDIS_URL is not configured")
	}
	options, err := redis.ParseURL(address)
	if err != nil {
		return nil, errors.New("REDIS_URL is invalid")
	}
	if options.TLSConfig != nil && options.TLSConfig.InsecureSkipVerify {
		return nil, errors.New("REDIS_URL must verify the server certificate")
	}

	// A private CA is restricted to this Redis client.
	if caFile != "" {
		if options.TLSConfig == nil {
			return nil, errors.New("REDIS_CA_FILE requires a rediss:// URL")
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("cannot read REDIS_CA_FILE")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("REDIS_CA_FILE contains no PEM certificate")
		}
		options.TLSConfig.RootCAs = roots
	}

	// Keep authentication and PING within a single deadline.
	options.ContextTimeoutEnabled = true
	client := redis.NewClient(options)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("Redis PING failed (%T)", err)
	}
	return client, nil
}
