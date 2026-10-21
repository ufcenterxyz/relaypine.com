package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	private := os.Getenv("ALLOW_PRIVATE_UPSTREAMS") == "true"
	production := os.Getenv("APP_ENV") == "production"
	appURL, adminAppURL, err := api.ValidateAppURLs(env("APP_URL", "http://localhost:4810"), env("ADMIN_APP_URL", "http://admin.localhost:4810"), production)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if production && private {
		slog.Error("Production requires private upstreams disabled")
		os.Exit(1)
	}
	trustedProxies, err := api.ParseProxyCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		slog.Error("TRUSTED_PROXY_CIDRS must contain explicit proxy networks")
		os.Exit(1)
	}
	cloudflareProxies, err := api.ParseProxyCIDRs(os.Getenv("CLOUDFLARE_PROXY_CIDRS"))
	if err != nil {
		slog.Error("CLOUDFLARE_PROXY_CIDRS must contain explicit Cloudflare networks")
		os.Exit(1)
	}
	adminEmail := store.NormalizeEmail(os.Getenv("SUPERADMIN_EMAIL"))
	if !store.ValidEmail(adminEmail) {
		slog.Error("SUPERADMIN_EMAIL must be a valid email address")
		os.Exit(1)
	}
	if os.Getenv("MYSQL_DSN") == "" || os.Getenv("MASTER_KEY") == "" {
		slog.Error("MYSQL_DSN and MASTER_KEY are required")
		os.Exit(1)
	}
	db, err := store.Open(ctx, os.Getenv("MYSQL_DSN"), os.Getenv("MASTER_KEY"), store.Config{SuperAdminEmail: adminEmail})
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
	if _, err = db.EnsureSuperAdmin(ctx, env("BOOTSTRAP_NAME", "RelayPine Administrator")); err != nil {
		slog.Error("Super administrator initialization failed")
		os.Exit(1)
	}
	var sender api.CodeSender
	if os.Getenv("MAILGUN_API_KEY") != "" || os.Getenv("MAILGUN_DOMAIN") != "" || os.Getenv("MAILGUN_FROM") != "" {
		mailer, mailErr := api.NewMailgunSender(os.Getenv("MAILGUN_API_KEY"), os.Getenv("MAILGUN_DOMAIN"), os.Getenv("MAILGUN_FROM"), os.Getenv("MAILGUN_API_BASE"))
		if mailErr != nil {
			slog.Warn("Email delivery configuration is incomplete or invalid")
		} else {
			sender = mailer
		}
	} else {
		slog.Warn("Email delivery is not configured; code requests will be unavailable")
	}
	masterKey, _ := base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	emailAuth, err := api.NewEmailAuth(redisClient, sender, masterKey)
	if err != nil {
		slog.Error("Email authentication initialization failed")
		os.Exit(1)
	}
	gateway := relay.New(db, redisClient, private)
	app := &api.Server{EmailAuth: emailAuth, Store: db, Redis: redisClient, Relay: gateway, AppURL: appURL, AdminAppURL: adminAppURL, AllowPrivate: private, TrustedProxyCIDRs: trustedProxies, CloudflareProxyCIDRs: cloudflareProxies}
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
		slog.Info("RelayPine gateway ready", "address", server.Addr, "access", "email-otp")
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
