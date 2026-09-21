// Package config loads the server settings.
//
// Values come from three places. Defaults are the base,
// environment variables override them, and command line flags
// win over everything: the environment describes where the
// server runs (container, CI), while a flag is a one-time
// override made by hand.
package config

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
)

const (
	defaultRunAddress = ":8080"
	defaultLogLevel   = "info"
	defaultS3Bucket   = "avatars"
)

// Config holds all server settings.
type Config struct {
	// RunAddress is the HTTP listen address.
	RunAddress string
	// DatabaseDSN is the postgres connection string. Required.
	DatabaseDSN string
	// S3Endpoint is the S3 host:port without scheme. Required.
	S3Endpoint string
	// S3AccessKey and S3SecretKey authenticate against S3. Required.
	S3AccessKey string
	S3SecretKey string
	// S3Bucket is the bucket that stores originals and thumbnails.
	S3Bucket string
	// S3UseSSL selects https for the S3 endpoint. Off for local MinIO.
	S3UseSSL bool
	// LogLevel is a zap level name: debug, info, warn, error.
	LogLevel string
}

// Load reads the settings for the running program. It must be
// called once, because it parses the command line.
func Load(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	return loadFrom(flag.NewFlagSet("gophprofile", flag.ContinueOnError), args, lookupEnv)
}

// loadFrom does the work on a given flag set, so tests can call it
// many times with their own arguments and environment.
func loadFrom(fs *flag.FlagSet, args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Config{
		RunAddress: defaultRunAddress,
		S3Bucket:   defaultS3Bucket,
		LogLevel:   defaultLogLevel,
	}

	stringVars := map[string]*string{
		"RUN_ADDRESS":   &cfg.RunAddress,
		"DATABASE_DSN":  &cfg.DatabaseDSN,
		"S3_ENDPOINT":   &cfg.S3Endpoint,
		"S3_ACCESS_KEY": &cfg.S3AccessKey,
		"S3_SECRET_KEY": &cfg.S3SecretKey,
		"S3_BUCKET":     &cfg.S3Bucket,
		"LOG_LEVEL":     &cfg.LogLevel,
	}
	for name, dst := range stringVars {
		if v, ok := lookupEnv(name); ok {
			*dst = v
		}
	}
	if v, ok := lookupEnv("S3_USE_SSL"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse S3_USE_SSL: %w", err)
		}
		cfg.S3UseSSL = b
	}

	fs.StringVar(&cfg.RunAddress, "a", cfg.RunAddress, "HTTP listen address")
	fs.StringVar(&cfg.DatabaseDSN, "d", cfg.DatabaseDSN, "postgres connection string")
	fs.StringVar(&cfg.S3Endpoint, "s3-endpoint", cfg.S3Endpoint, "S3 endpoint host:port")
	fs.StringVar(&cfg.S3AccessKey, "s3-access-key", cfg.S3AccessKey, "S3 access key")
	fs.StringVar(&cfg.S3SecretKey, "s3-secret-key", cfg.S3SecretKey, "S3 secret key")
	fs.StringVar(&cfg.S3Bucket, "s3-bucket", cfg.S3Bucket, "S3 bucket name")
	fs.BoolVar(&cfg.S3UseSSL, "s3-ssl", cfg.S3UseSSL, "use https for the S3 endpoint")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.DatabaseDSN == "" {
		return errors.New("database DSN is required: set DATABASE_DSN or -d")
	}
	if c.S3Endpoint == "" {
		return errors.New("S3 endpoint is required: set S3_ENDPOINT or -s3-endpoint")
	}
	if c.S3AccessKey == "" || c.S3SecretKey == "" {
		return errors.New("S3 credentials are required: set S3_ACCESS_KEY and S3_SECRET_KEY")
	}
	return nil
}
