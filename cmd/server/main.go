// GophProfile server entrypoint.
package main

import (
	"context"
	"fmt"
	stdlog "log"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/logger"
)

// Build information injected at link time via
// -ldflags "-X main.buildVersion=... -X main.buildDate=...".
var (
	buildVersion = "N/A"
	buildDate    = "N/A"
)

func main() {
	fmt.Printf("Build version: %s\nBuild date: %s\n", buildVersion, buildDate)

	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}

	log, err := logger.New(logLevel)
	if err != nil {
		stdlog.Println(err)
		os.Exit(1)
	}

	if err := run(log); err != nil {
		log.Error("server terminated with error", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
	_ = log.Sync()
}

func run(log *zap.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	log.Info("server started")

	// Wait for a shutdown signal.
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
