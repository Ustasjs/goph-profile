// GophProfile worker entrypoint: consumes avatar events and does
// the heavy lifting (thumbnails, S3 cleanup) off the request path.
package main

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/broker"
	"github.com/ustasjs/goph-profile/internal/config"
	"github.com/ustasjs/goph-profile/internal/logger"
	"github.com/ustasjs/goph-profile/internal/storage/postgres"
	"github.com/ustasjs/goph-profile/internal/storage/s3"
	"github.com/ustasjs/goph-profile/internal/worker/processor"
)

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
		log.Error("worker terminated with error", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
	_ = log.Sync()
}

func run(cfg config.Config, log *zap.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()
	
	if cfg.DatabaseDSN == "" {
		return errors.New("database DSN is required: set DATABASE_DSN or -d")
	}

	// Migrations are the server's job; the worker only connects.
	pool, err := pgxpool.New(ctx, cfg.DatabaseDSN)
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

	consumer, err := broker.NewConsumer(cfg.RabbitURL, cfg.Prefetch, log)
	if err != nil {
		return err
	}

	proc := processor.New(postgres.New(pool), files, log)

	log.Info("worker started", zap.Int("prefetch", cfg.Prefetch))
	// Run blocks until the context is canceled; it closes the
	// connection itself on the way out.
	return consumer.Run(ctx, broker.Handlers{
		OnUpload:     proc.HandleUpload,
		OnDelete:     proc.HandleDelete,
		OnUploadDead: proc.UploadFailed,
	})
}
