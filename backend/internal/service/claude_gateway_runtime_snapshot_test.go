package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type claudeGatewayRuntimeRepo struct {
	SettingRepository
	mu    sync.RWMutex
	value string
}

func (r *claudeGatewayRuntimeRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyClaudeGatewaySettings {
		return "", fmt.Errorf("unexpected setting key: %s", key)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.value, nil
}

func (r *claudeGatewayRuntimeRepo) Set(_ context.Context, key, value string) error {
	if key != SettingKeyClaudeGatewaySettings {
		return fmt.Errorf("unexpected setting key: %s", key)
	}
	r.mu.Lock()
	r.value = value
	r.mu.Unlock()
	return nil
}

func TestClaudeGatewayRuntimeSnapshotDeepCopiesSlices(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
		ProtocolMode:    config.CliSimulationProtocolModeLegacy,
		CCVersionPool:   []string{"1.0.0"},
		OSArchPool:      []config.OSArchEntry{{OS: "linux", Arch: "amd64"}},
		ExtraBetaTokens: []string{"beta-a"},
	}}}
	service := NewSettingService(&claudeGatewayRuntimeRepo{}, cfg)

	cfg.Gateway.CliSimulation.CCVersionPool[0] = "mutated-config"
	cfg.Gateway.CliSimulation.OSArchPool[0].OS = "mutated-config"
	cfg.Gateway.CliSimulation.ExtraBetaTokens[0] = "mutated-config"

	first := service.claudeGatewayRuntimeConfig()
	require.Equal(t, "1.0.0", first.CCVersionPool[0])
	require.Equal(t, "linux", first.OSArchPool[0].OS)
	require.Equal(t, "beta-a", first.ExtraBetaTokens[0])

	first.CCVersionPool[0] = "mutated-reader"
	first.OSArchPool[0].OS = "mutated-reader"
	first.ExtraBetaTokens[0] = "mutated-reader"

	second := service.claudeGatewayRuntimeConfig()
	require.Equal(t, "1.0.0", second.CCVersionPool[0])
	require.Equal(t, "linux", second.OSArchPool[0].OS)
	require.Equal(t, "beta-a", second.ExtraBetaTokens[0])
}

func TestClaudeGatewayRuntimeSnapshotConcurrentSetAndRead(t *testing.T) {
	repo := &claudeGatewayRuntimeRepo{}
	cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
		ProtocolMode: config.CliSimulationProtocolModeLegacy,
	}}}
	settingService := NewSettingService(repo, cfg)
	gatewayService := &GatewayService{cfg: cfg, settingService: settingService}
	rateLimitService := &RateLimitService{cfg: cfg, settingService: settingService}

	initial := DefaultClaudeGatewaySettings()
	initial.ProtocolMode = config.CliSimulationProtocolModeLegacy
	initial.Enabled = true
	require.NoError(t, settingService.SetClaudeGatewaySettings(context.Background(), initial))

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(5)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			marker := fmt.Sprintf("version-%d", i%2)
			settings := DefaultClaudeGatewaySettings()
			settings.ProtocolMode = config.CliSimulationProtocolModeLegacy
			settings.Enabled = true
			settings.CCVersionOverride = marker
			settings.CCVersionPool = []string{marker}
			settings.OSArchPool = []config.OSArchEntry{{OS: marker, Arch: marker}}
			settings.ExtraBetaTokens = []string{marker}
			settings.CacheControlTTLOverride = marker
			settings.FingerprintSaltOverride = marker
			settings.SystemPromptStaticOverride = marker
			require.NoError(t, settingService.SetClaudeGatewaySettings(context.Background(), settings))
		}
	}()

	reader := func() {
		defer wg.Done()
		for i := 0; i < iterations*2; i++ {
			runtime := gatewayService.claudeGatewayRuntimeConfig()
			if runtime.CCVersionOverride != "" {
				require.Len(t, runtime.CCVersionPool, 1)
				require.Equal(t, runtime.CCVersionOverride, runtime.CCVersionPool[0])
				require.Len(t, runtime.OSArchPool, 1)
				require.Equal(t, runtime.CCVersionOverride, runtime.OSArchPool[0].OS)
				require.Len(t, runtime.ExtraBetaTokens, 1)
				require.Equal(t, runtime.CCVersionOverride, runtime.ExtraBetaTokens[0])
			}
			_ = gatewayService.legacyCLIProtocolEnabled()
			_ = gatewayService.mergeExtraBetaTokens([]string{"base"})
			_ = gatewayService.getEffectiveCacheControlTTL()
			_ = gatewayService.GetEffectiveCLIVersion()
			_ = gatewayService.GetEffectiveFingerprintSalt()
			_ = gatewayService.getEffectiveSystemPromptExpansion()
			_ = rateLimitService.claudeStabilityConfig(context.Background())
		}
	}
	go reader()
	go reader()
	go reader()
	go reader()
	wg.Wait()
}
