package config

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func load(t *testing.T, args []string, env map[string]string) (Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	return loadFrom(fs, args, func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	})
}

// required is the minimal environment that passes validation.
func required() map[string]string {
	return map[string]string{
		"DATABASE_DSN":  "postgres://localhost/db",
		"S3_ENDPOINT":   "localhost:9000",
		"S3_ACCESS_KEY": "key",
		"S3_SECRET_KEY": "secret",
		"RABBITMQ_URL":  "amqp://guest:guest@localhost:5672/",
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := load(t, nil, required())
	require.NoError(t, err)

	assert.Equal(t, ":8080", cfg.RunAddress)
	assert.Equal(t, "avatars", cfg.S3Bucket)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.False(t, cfg.S3UseSSL)
	assert.Equal(t, 8, cfg.Prefetch)
}

func TestEnvOverridesDefaults(t *testing.T) {
	env := required()
	env["RUN_ADDRESS"] = ":9090"
	env["S3_BUCKET"] = "pics"
	env["S3_USE_SSL"] = "true"
	env["LOG_LEVEL"] = "debug"

	cfg, err := load(t, nil, env)
	require.NoError(t, err)

	assert.Equal(t, ":9090", cfg.RunAddress)
	assert.Equal(t, "pics", cfg.S3Bucket)
	assert.True(t, cfg.S3UseSSL)
	assert.Equal(t, "debug", cfg.LogLevel)
}

func TestFlagsOverrideEnv(t *testing.T) {
	env := required()
	env["RUN_ADDRESS"] = ":9090"

	cfg, err := load(t, []string{"-a", ":7070", "-s3-bucket", "flagged"}, env)
	require.NoError(t, err)

	assert.Equal(t, ":7070", cfg.RunAddress)
	assert.Equal(t, "flagged", cfg.S3Bucket)
}

func TestRequiredValues(t *testing.T) {
	for _, missing := range []string{"DATABASE_DSN", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY", "RABBITMQ_URL"} {
		t.Run(missing, func(t *testing.T) {
			env := required()
			delete(env, missing)
			_, err := load(t, nil, env)
			assert.Error(t, err)
		})
	}
}

func TestBadSSLValue(t *testing.T) {
	env := required()
	env["S3_USE_SSL"] = "nope"
	_, err := load(t, nil, env)
	assert.Error(t, err)
}

func TestBadPrefetch(t *testing.T) {
	for _, v := range []string{"zero", "0"} {
		t.Run(v, func(t *testing.T) {
			env := required()
			env["WORKER_PREFETCH"] = v
			_, err := load(t, nil, env)
			assert.Error(t, err)
		})
	}
}
