package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNew(t *testing.T) {
	log, err := New("debug")
	require.NoError(t, err)

	assert.True(t, log.Core().Enabled(zap.DebugLevel))
}

func TestNewLevelFilter(t *testing.T) {
	log, err := New("error")
	require.NoError(t, err)

	assert.False(t, log.Core().Enabled(zap.InfoLevel))
	assert.True(t, log.Core().Enabled(zap.ErrorLevel))
}

func TestNewBadLevel(t *testing.T) {
	_, err := New("loud")
	assert.Error(t, err)
}
