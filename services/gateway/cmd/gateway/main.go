package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/api"
	"relaypine.com/gateway/internal/relay"
	"relaypine.com/gateway/internal/store"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	appURL := env("APP_URL", "http://localhost:4810")
	u, err := url.Parse(appURL)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		slog.Error("APP_URL must be an origin")
		os.Exit(1)
	}
	appURL = strings.TrimRight(appURL, "/")
	private := os.Getenv("ALLOW_PRIVATE_UPSTREAMS") == "true"
	production := os.Getenv("APP_ENV") == "production"
	if production && (private || u.Scheme != "https") {
		slog.Error("Production requires HTTPS and private upstreams disabled")
		os.Exit(1)
	}
	if os.Getenv("MYSQL_DSN") == "" || os.Getenv("MASTER_KEY") == "" {
		slog.Error("MYSQL_DSN and MASTER_KEY are required")
		os.Exit(1)
	}
	db, err := store.Open(ctx, os.Getenv("MYSQL_DSN"), os.Getenv("MASTER_KEY"))
	if err != nil {
		slog.Error("Database initialization failed")
		os.Exit(1)
	}
	defer db.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "127.0.0.1:4813"), Password: os.Getenv("REDIS_PASSWORD")})
	defer redisClient.Close()
	if redisClient.Ping(ctx).Err() != nil {
		slog.Error("Redis unavailable")
		os.Exit(1)
	}
	if email := os.Getenv("BOOTSTRAP_EMAIL"); email != "" && store.NormalizeEmail(email) != store.SuperAdminEmail {
		slog.Error("BOOTSTRAP_EMAIL must match the configured super administrator")
		os.Exit(1)
	}
	if _, err = db.EnsureSuperAdmin(ctx, env("BOOTSTRAP_NAME", "RelayPine Administrator"), os.Getenv("BOOTSTRAP_PASSWORD")); err != nil {
		slog.Error("Super administrator bootstrap failed; configure BOOTSTRAP_PASSWORD")
		os.Exit(1)
	}
	gateway := relay.New(db, redisClient, private)
	app := &api.Server{Store: db, Redis: redisClient, Relay: gateway, AppURL: appURL, AllowPrivate: private}
	server := &http.Server{Addr: env("GATEWAY_ADDR", "127.0.0.1:4811"), Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, cancel := context.WithTimeout(ctx, 40*time.Second)
				if e := db.Recover(work); e != nil {
					slog.Warn("Request recovery will retry")
				}
				cancel()
			}
		}
	}()
	go func() {
		slog.Info("RelayPine gateway ready", "address", server.Addr, "access", "private")
		if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("HTTP listener failed")
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if server.Shutdown(shutdown) != nil {
		_ = server.Close()
	}
}
