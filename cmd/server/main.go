// GophProfile server entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/ustasjs/goph-profile/internal/broker"
	"github.com/ustasjs/goph-profile/internal/config"
	"github.com/ustasjs/goph-profile/internal/logger"
	"github.com/ustasjs/goph-profile/internal/metrics"
	"github.com/ustasjs/goph-profile/internal/server/httpserver"
	"github.com/ustasjs/goph-profile/internal/server/service"
	"github.com/ustasjs/goph-profile/internal/storage/postgres"
	"github.com/ustasjs/goph-profile/internal/storage/s3"
	"github.com/ustasjs/goph-profile/internal/telemetry"
	"github.com/ustasjs/goph-profile/migrations"
)

const shutdownTimeout = 10 * time.Second

// Build information injected at link time via
// -ldflags "-X main.buildVersion=... -X main.buildDate=...".
var (
	buildVersion = "N/A"
	buildDate    = "N/A"
)

func main() {
	fmt.Printf("Build version: %s\nBuild date: %s\n", buildVersion, buildDate)

	cfg, err := config.Load(os.Args[1:], os.LookupEnv)
	if err != nil {
		stdlog.Println(err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.LogLevel)
	if err != nil {
		stdlog.Println(err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("server terminated with error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, "gophprofile-server", cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	// The flush gets its own context: by this point ctx is already
	// canceled by the shutdown signal.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			log.Warn("flush traces", "error", err)
		}
	}()

	if cfg.DatabaseDSN == "" {
		return errors.New("database DSN is required: set DATABASE_DSN or -d")
	}
	if err := migrations.Run(cfg.DatabaseDSN); err != nil {
		return err
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("parse database dsn: %w", err)
	}
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create pgx pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	files, err := s3.New(s3.Config{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		UseSSL:    cfg.S3UseSSL,
	})
	if err != nil {
		return err
	}
	if err := files.EnsureBucket(ctx); err != nil {
		return err
	}

	pub, err := broker.NewPublisher(cfg.RabbitURL)
	if err != nil {
		return err
	}
	defer func() { _ = pub.Close() }()

	repo := postgres.New(pool)
	svc := service.New(repo, files, pub, log)

	m := metrics.NewServer()
	m.RegisterPool(pool.Stat)
	m.RegisterStorage(repo.StorageByUser)

	checks := []httpserver.HealthCheck{
		{Name: "db", Check: pool.Ping},
		{Name: "s3", Check: files.Ping},
		{Name: "broker", Check: pub.Ping},
	}
	server := httpserver.New(cfg.RunAddress, svc, checks, m, log)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("starting HTTP server", "address", cfg.RunAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	// Wait for a shutdown signal (or a server failure), then stop
	// the server gracefully.
	g.Go(func() error {
		<-gCtx.Done()
		log.Info("shutting down")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})

	return g.Wait()
}
