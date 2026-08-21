package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type claudeGatewaySettingsRead struct {
	value string
	err   error
}

type claudeGatewaySettingsSequenceRepo struct {
	SettingRepository

	mu        sync.Mutex
	reads     []claudeGatewaySettingsRead
	readCalls int
}

func (r *claudeGatewaySettingsSequenceRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyClaudeGatewaySettings {
		return "", errors.New("unexpected setting key: " + key)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	call := r.readCalls
	r.readCalls++
	if call >= len(r.reads) {
		return "", errors.New("unexpected extra claude gateway settings read")
	}
	return r.reads[call].value, r.reads[call].err
}

func (r *claudeGatewaySettingsSequenceRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readCalls
}

func TestGatewayServiceCLISimulationSettingsRetryAfterTransientReadError(t *testing.T) {
	persisted := DefaultClaudeGatewaySettings()
	persisted.ProtocolMode = config.CliSimulationProtocolModeLegacy
	data, err := json.Marshal(persisted)
	require.NoError(t, err)

	repo := &claudeGatewaySettingsSequenceRepo{reads: []claudeGatewaySettingsRead{
		{err: errors.New("temporary database error")},
		{value: string(data)},
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
		ProtocolMode: config.CliSimulationProtocolModePassthrough,
	}}}
	settingService := NewSettingService(repo, cfg)
	gatewayService := &GatewayService{cfg: cfg, settingService: settingService}

	require.False(t, gatewayService.legacyCLIProtocolEnabled())
	require.Equal(t, 1, repo.calls())

	require.True(t, gatewayService.legacyCLIProtocolEnabled())
	require.Equal(t, 2, repo.calls())

	// A successful load remains cached on the hot path.
	require.True(t, gatewayService.legacyCLIProtocolEnabled())
	require.Equal(t, 2, repo.calls())
}

func TestGetClaudeGatewaySettingsMalformedJSONAppliesFallbackToRuntime(t *testing.T) {
	repo := &claudeGatewaySettingsSequenceRepo{reads: []claudeGatewaySettingsRead{
		{value: `{malformed`},
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
		ProtocolMode:                     "stale-invalid-mode",
		MaxRetryAttempts:                 0,
		RetryBaseDelayMs:                 0,
		RetryMaxDelayMs:                  0,
		RetryMaxElapsedSeconds:           0,
		RateLimitFallbackCooldownSeconds: 0,
		OAuthAuthCooldownMinutes:         0,
		MinInterRequestDelayMs:           -1,
		MaxInterRequestDelayMs:           -2,
	}}}
	settingService := NewSettingService(repo, cfg)

	settings, err := settingService.GetClaudeGatewaySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, config.CliSimulationProtocolModePassthrough, settings.ProtocolMode)
	require.Equal(t, 1, settings.MaxRetryAttempts)
	require.Equal(t, 50, settings.RetryBaseDelayMs)
	require.Equal(t, 50, settings.RetryMaxDelayMs)
	require.Equal(t, 0, settings.MinInterRequestDelayMs)
	require.Equal(t, 0, settings.MaxInterRequestDelayMs)

	runtime := settingService.claudeGatewayRuntimeConfig()
	require.Equal(t, settings.ProtocolMode, runtime.ProtocolMode)
	require.Equal(t, settings.MaxRetryAttempts, runtime.MaxRetryAttempts)
	require.Equal(t, settings.RetryBaseDelayMs, runtime.RetryBaseDelayMs)
	require.Equal(t, settings.RetryMaxDelayMs, runtime.RetryMaxDelayMs)
	require.Equal(t, settings.MinInterRequestDelayMs, runtime.MinInterRequestDelayMs)
	require.Equal(t, settings.MaxInterRequestDelayMs, runtime.MaxInterRequestDelayMs)
}

type claudeGatewaySettingsBlockingRepo struct {
	SettingRepository

	mu          sync.Mutex
	value       string
	getStarted  chan struct{}
	releaseGet  chan struct{}
	setCalled   chan struct{}
	getStartOne sync.Once
	setCallOne  sync.Once
}

func (r *claudeGatewaySettingsBlockingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyClaudeGatewaySettings {
		return "", errors.New("unexpected setting key: " + key)
	}
	r.mu.Lock()
	value := r.value
	r.mu.Unlock()
	r.getStartOne.Do(func() { close(r.getStarted) })
	<-r.releaseGet
	return value, nil
}

func (r *claudeGatewaySettingsBlockingRepo) Set(_ context.Context, key, value string) error {
	if key != SettingKeyClaudeGatewaySettings {
		return errors.New("unexpected setting key: " + key)
	}
	r.setCallOne.Do(func() { close(r.setCalled) })
	r.mu.Lock()
	r.value = value
	r.mu.Unlock()
	return nil
}

func TestClaudeGatewaySettingsConcurrentGetCannotOverwriteNewerSetSnapshot(t *testing.T) {
	oldSettings := DefaultClaudeGatewaySettings()
	oldSettings.CCVersionOverride = "old"
	oldJSON, err := json.Marshal(oldSettings)
	require.NoError(t, err)

	repo := &claudeGatewaySettingsBlockingRepo{
		value:      string(oldJSON),
		getStarted: make(chan struct{}),
		releaseGet: make(chan struct{}),
		setCalled:  make(chan struct{}),
	}
	settingService := NewSettingService(repo, &config.Config{})

	getDone := make(chan error, 1)
	go func() {
		_, getErr := settingService.GetClaudeGatewaySettings(context.Background())
		getDone <- getErr
	}()
	<-repo.getStarted

	newSettings := DefaultClaudeGatewaySettings()
	newSettings.CCVersionOverride = "new"
	setDone := make(chan error, 1)
	go func() {
		setDone <- settingService.SetClaudeGatewaySettings(context.Background(), newSettings)
	}()

	select {
	case <-repo.setCalled:
		t.Fatal("SetClaudeGatewaySettings reached persistence before the in-flight GET completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(repo.releaseGet)
	require.NoError(t, <-getDone)
	require.NoError(t, <-setDone)
	require.Equal(t, "new", settingService.claudeGatewayRuntimeConfig().CCVersionOverride)
}
