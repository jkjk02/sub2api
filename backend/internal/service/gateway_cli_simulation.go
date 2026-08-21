package service

import (
	"context"
	mathrand "math/rand"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ensureCLISimulationSettingsLoaded lazily applies the persisted centralized profile.
func (s *GatewayService) ensureCLISimulationSettingsLoaded() {
	if s == nil || s.settingService == nil {
		return
	}
	s.cliSimulationSettingsOnce.Do(func() {
		_, _ = s.settingService.GetClaudeGatewaySettings(context.Background())
	})
}

// legacyCLIProtocolEnabled centralizes the compatibility gate for fork-specific
// Claude Code synthesis. Unknown/empty protocol modes are normalized by config.
func (s *GatewayService) legacyCLIProtocolEnabled() bool {
	s.ensureCLISimulationSettingsLoaded()
	return s != nil && s.cfg != nil &&
		s.cfg.Gateway.CliSimulation.EffectiveProtocolMode() == config.CliSimulationProtocolModeLegacy
}

// legacyAPIKeyCLISimulationEnabled additionally honors the API-key feature switch.
func (s *GatewayService) legacyAPIKeyCLISimulationEnabled() bool {
	s.ensureCLISimulationSettingsLoaded()
	return s != nil && s.cfg != nil && s.cfg.Gateway.CliSimulation.LegacySynthesisEnabled()
}

// applyInterRequestDelay smooths short request bursts per account before the first upstream attempt.
// It is independent from legacy identity synthesis and returns promptly on cancellation.
func (s *GatewayService) applyInterRequestDelay(ctx context.Context, accountID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.ensureCLISimulationSettingsLoaded()
	if s == nil || s.cfg == nil {
		return nil
	}
	c := s.cfg.Gateway.CliSimulation
	if !c.StabilityProtectionEnabled || !c.TrafficSmoothingEnabled {
		return nil
	}
	minMs := c.MinInterRequestDelayMs
	maxMs := c.MaxInterRequestDelayMs
	if minMs <= 0 || maxMs <= 0 || maxMs < minMs {
		return nil
	}
	interval := time.Duration(minMs+mathrand.Intn(maxMs-minMs+1)) * time.Millisecond
	now := time.Now()

	s.claudePacingMu.Lock()
	if s.claudeNextRequestAt == nil {
		s.claudeNextRequestAt = make(map[int64]time.Time)
	}
	scheduledAt := now
	if next := s.claudeNextRequestAt[accountID]; next.After(scheduledAt) {
		scheduledAt = next
	}
	s.claudeNextRequestAt[accountID] = scheduledAt.Add(interval)
	s.claudePacingMu.Unlock()

	return sleepWithContext(ctx, time.Until(scheduledAt))
}
