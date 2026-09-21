// Package logger builds the zap logger used by the binaries.
package logger

import (
	"fmt"

	"go.uber.org/zap"
)

// New builds a production zap logger with the given level name:
// debug, info, warn, error.
func New(level string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return nil, fmt.Errorf("parse log level: %w", err)
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = lvl
	log, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	return log, nil
}
