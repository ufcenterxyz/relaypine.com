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
	"relaypine.com/gateway/internal/payments"
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
	if email, password := os.Getenv("BOOTSTRAP_EMAIL"), os.Getenv("BOOTSTRAP_PASSWORD"); email != "" && password != "" {
		_, err = db.Register(ctx, email, env("BOOTSTRAP_NAME", "RelayPine Operator"), password, true)
		if err != nil && !errors.Is(err, store.ErrConflict) {
			slog.Error("Operator bootstrap failed")
			os.Exit(1)
		}
	}
	stripe := payments.New(db, os.Getenv("STRIPE_SECRET_KEY"), os.Getenv("STRIPE_WEBHOOK_SECRET"), appURL, env("STRIPE_API_VERSION", "2026-04-22.dahlia"))
	if err = stripe.Migrate(ctx); err != nil {
		slog.Error("Payment migration failed")
		os.Exit(1)
	}
	gateway := relay.New(db, redisClient, private)
	app := &api.Server{Store: db, Redis: redisClient, Relay: gateway, Payments: stripe, AppURL: appURL, AllowPrivate: private}
	server := &http.Server{Addr: env("GATEWAY_ADDR", "127.0.0.1:4811"), Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		ticks := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, cancel := context.WithTimeout(ctx, 40*time.Second)
				if e := db.Recover(work); e != nil {
					slog.Warn("Request recovery will retry")
				}
				if e := stripe.ProcessPending(work); e != nil {
					slog.Warn("Payment inbox will retry")
				}
				ticks++
				if ticks%4 == 0 {
					if e := stripe.Reconcile(work); e != nil {
						slog.Warn("Payment reconciliation will retry")
					}
				}
				cancel()
			}
		}
	}()
	go func() {
		slog.Info("RelayPine gateway ready", "address", server.Addr, "payments_configured", stripe.Enabled())
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
