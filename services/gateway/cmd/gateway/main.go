package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"relaypine.com/gateway/internal/api"
	"relaypine.com/gateway/internal/objectstore"
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
	capture := env("REQUEST_BODY_CAPTURE", "true")
	if capture != "true" && capture != "false" {
		slog.Error("REQUEST_BODY_CAPTURE must be true or false")
		os.Exit(1)
	}
	// Unset keeps bodies indefinitely. Earlier releases read 0 as "stop
	// recording and delete stored bodies", so 0 is refused rather than
	// silently changing meaning.
	retentionDays := 0
	if value := os.Getenv("REQUEST_BODY_RETENTION_DAYS"); value != "" {
		if retentionDays, err = strconv.Atoi(value); err != nil || retentionDays < 1 || retentionDays > 3650 {
			slog.Error("REQUEST_BODY_RETENTION_DAYS must be unset to keep bodies indefinitely, or a whole number of days from 1 to 3650; set REQUEST_BODY_CAPTURE=false to stop recording")
			os.Exit(1)
		}
	}
	objects, err := payloadObjects()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	db, err := store.Open(ctx, os.Getenv("MYSQL_DSN"), os.Getenv("MASTER_KEY"), store.Config{SuperAdminEmail: adminEmail, PayloadCapture: capture == "true", PayloadRetention: time.Duration(retentionDays) * 24 * time.Hour, PayloadObjects: objects})
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
	gateway := relay.New(db, redisClient, private, capture == "true")
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
	// Body upkeep can take minutes while a backlog moves to object storage, so
	// it never delays request recovery.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, cancel := context.WithTimeout(ctx, 2*time.Minute)
				if e := db.MaintainPayloads(work); e != nil {
					slog.Warn("Request body upkeep will retry", "error", e)
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
	// Finish uploading bodies of the last requests; within the pod's grace period.
	drain, cancelDrain := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelDrain()
	db.DrainPayloads(drain)
}

// payloadObjects configures optional object storage for request bodies. All
// OBJECT_STORAGE_* settings are required together; none keeps bodies in MySQL.
func payloadObjects() (store.ObjectStore, error) {
	cfg := objectstore.Config{Endpoint: os.Getenv("OBJECT_STORAGE_ENDPOINT"), Region: os.Getenv("OBJECT_STORAGE_REGION"), Bucket: os.Getenv("OBJECT_STORAGE_BUCKET"), AccessKeyID: os.Getenv("OBJECT_STORAGE_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("OBJECT_STORAGE_SECRET_ACCESS_KEY")}
	style := env("OBJECT_STORAGE_PATH_STYLE", "false")
	if cfg.Endpoint == "" && cfg.Region == "" && cfg.Bucket == "" && cfg.AccessKeyID == "" && cfg.SecretAccessKey == "" {
		slog.Info("Request bodies are stored in MySQL")
		return nil, nil
	}
	if style != "true" && style != "false" {
		return nil, errors.New("OBJECT_STORAGE_PATH_STYLE must be true or false")
	}
	cfg.PathStyle = style == "true"
	client, err := objectstore.New(cfg)
	if err != nil {
		return nil, errors.New("OBJECT_STORAGE_* settings are incomplete or invalid: " + err.Error())
	}
	slog.Info("Request bodies are stored in object storage", "bucket", client.Bucket())
	return client, nil
}
