package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCLIProtocolTestService(mode string) *GatewayService {
	return &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CliSimulation: config.CliSimulationConfig{
			StabilityProtectionEnabled:    true,
			TrafficSmoothingEnabled:       true,
			ProtocolMode:                  mode,
			Enabled:                       true,
			ForceCLIBetaForAPIKey:         true,
			EnableCCMimicHeadersForAPIKey: true,
			CCVersionOverride:             "9.9.9",
			ExtraBetaTokens:               []string{"custom-beta-2026-08-17"},
			CacheControlTTLOverride:       "1h",
			FingerprintSaltOverride:       "service-specific-salt",
			SystemPromptStaticOverride:    "service-specific-prompt",
			MinInterRequestDelayMs:        5_000,
			MaxInterRequestDelayMs:        5_000,
		},
	}}}
}

func TestGatewayServiceCLIProtocolModeGatesAPIKeyHeadersAndBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		mode           string
		wantCLIHeaders bool
		wantForcedBeta bool
	}{
		{name: "legacy", mode: config.CliSimulationProtocolModeLegacy, wantCLIHeaders: true, wantForcedBeta: true},
		{name: "passthrough", mode: config.CliSimulationProtocolModePassthrough, wantCLIHeaders: false, wantForcedBeta: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("User-Agent", "client-agent/1.0")

			account := &Account{
				ID:       17,
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
			}
			svc := newCLIProtocolTestService(tt.mode)

			req, _, err := svc.buildUpstreamRequest(
				context.Background(), c, account,
				[]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`),
				"test-key", "api_key", "claude-sonnet-4-5", false, true,
			)
			require.NoError(t, err)

			if tt.wantCLIHeaders {
				require.True(t, strings.HasPrefix(getHeaderRaw(req.Header, "User-Agent"), "claude-cli/9.9.9"))
				require.NotEmpty(t, getHeaderRaw(req.Header, "x-client-request-id"))
			} else {
				require.Equal(t, "client-agent/1.0", getHeaderRaw(req.Header, "User-Agent"))
				require.Empty(t, getHeaderRaw(req.Header, "x-client-request-id"))
			}

			beta := getHeaderRaw(req.Header, "anthropic-beta")
			if tt.wantForcedBeta {
				require.Contains(t, beta, claude.BetaClaudeCode)
				require.Contains(t, beta, "custom-beta-2026-08-17")
			} else {
				require.Empty(t, beta)
			}
		})
	}
}

func TestGatewayServiceInterRequestDelayHonorsCancellation(t *testing.T) {
	svc := newCLIProtocolTestService(config.CliSimulationProtocolModeLegacy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err := svc.applyInterRequestDelay(ctx, 17)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(started), 250*time.Millisecond)
}

func TestGatewayServiceInterRequestDelaySkippedInPassthrough(t *testing.T) {
	svc := newCLIProtocolTestService(config.CliSimulationProtocolModePassthrough)
	started := time.Now()
	require.NoError(t, svc.applyInterRequestDelay(context.Background(), 17))
	require.Less(t, time.Since(started), 250*time.Millisecond)
}

func TestGatewayServiceCLIRuntimeIsInstanceScoped(t *testing.T) {
	legacyA := newCLIProtocolTestService(config.CliSimulationProtocolModeLegacy)
	legacyB := newCLIProtocolTestService(config.CliSimulationProtocolModeLegacy)
	legacyA.cfg.Gateway.CliSimulation.CCVersionOverride = "2.1.301"
	legacyB.cfg.Gateway.CliSimulation.CCVersionOverride = "2.1.302"
	legacyA.cfg.Gateway.CliSimulation.FingerprintSaltOverride = "salt-a"
	legacyB.cfg.Gateway.CliSimulation.FingerprintSaltOverride = "salt-b"
	legacyA.cfg.Gateway.CliSimulation.SystemPromptStaticOverride = "prompt-a"
	legacyB.cfg.Gateway.CliSimulation.SystemPromptStaticOverride = "prompt-b"

	require.Equal(t, "2.1.301", legacyA.GetEffectiveCLIVersion())
	require.Equal(t, "2.1.302", legacyB.GetEffectiveCLIVersion())
	require.Equal(t, "salt-a", legacyA.GetEffectiveFingerprintSalt())
	require.Equal(t, "salt-b", legacyB.GetEffectiveFingerprintSalt())
	require.Equal(t, "prompt-a", legacyA.getEffectiveSystemPromptExpansion())
	require.Equal(t, "prompt-b", legacyB.getEffectiveSystemPromptExpansion())

	legacyA.StopCLIVersionSync()
	select {
	case <-legacyA.getCLIVersionRuntime().ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("service-owned CLI version context was not canceled")
	}
	require.NoError(t, legacyB.getCLIVersionRuntime().ctx.Err())
}

func TestGatewayServiceCacheControlTTLIsInstanceScoped(t *testing.T) {
	serviceA := newCLIProtocolTestService(config.CliSimulationProtocolModeLegacy)
	serviceB := newCLIProtocolTestService(config.CliSimulationProtocolModeLegacy)
	serviceA.cfg.Gateway.CliSimulation.CacheControlTTLOverride = "1h"
	serviceB.cfg.Gateway.CliSimulation.CacheControlTTLOverride = "5m"

	body := []byte(`{"system":"original","messages":[]}`)
	readTTL := func(svc *GatewayService) string {
		var payload struct {
			System []struct {
				CacheControl map[string]any `json:"cache_control"`
			} `json:"system"`
		}
		rewritten := svc.rewriteSystemForNonClaudeCodeWithPromptBlocks(body, "original", "", "", "9.9.9")
		require.NoError(t, json.Unmarshal(rewritten, &payload))
		require.Len(t, payload.System, 3)
		value, ok := payload.System[2].CacheControl["ttl"].(string)
		require.True(t, ok)
		return value
	}

	require.Equal(t, "1h", readTTL(serviceA))
	require.Equal(t, "5m", readTTL(serviceB))
}

func TestGatewayServicePassthroughIgnoresLegacyOverrides(t *testing.T) {
	svc := newCLIProtocolTestService(config.CliSimulationProtocolModePassthrough)
	require.Equal(t, claude.CLICurrentVersion, svc.GetEffectiveCLIVersion())
	require.Equal(t, fingerprintSalt, svc.GetEffectiveFingerprintSalt())
	require.Equal(t, claudeCodeSystemPromptExpansion, svc.getEffectiveSystemPromptExpansion())
	require.Equal(t, claude.DefaultCacheControlTTL, svc.getEffectiveCacheControlTTL())
	require.Equal(t, []string{"base"}, svc.mergeExtraBetaTokens([]string{"base"}))
}
