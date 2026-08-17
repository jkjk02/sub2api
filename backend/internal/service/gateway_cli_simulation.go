package service

import (
	"context"
	mathrand "math/rand"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// legacyCLIProtocolEnabled centralizes the compatibility gate for fork-specific
// Claude Code synthesis. Unknown/empty protocol modes are normalized by config.
func (s *GatewayService) legacyCLIProtocolEnabled() bool {
	return s != nil && s.cfg != nil &&
		s.cfg.Gateway.CliSimulation.EffectiveProtocolMode() == config.CliSimulationProtocolModeLegacy
}

// legacyAPIKeyCLISimulationEnabled additionally honors the API-key feature switch.
func (s *GatewayService) legacyAPIKeyCLISimulationEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.CliSimulation.LegacySynthesisEnabled()
}

// applyInterRequestDelay inserts configured jitter before the first upstream attempt.
// It returns promptly when the request is canceled instead of keeping a worker asleep.
func (s *GatewayService) applyInterRequestDelay(ctx context.Context) error {
	if !s.legacyCLIProtocolEnabled() {
		return nil
	}
	minMs := s.cfg.Gateway.CliSimulation.MinInterRequestDelayMs
	maxMs := s.cfg.Gateway.CliSimulation.MaxInterRequestDelayMs
	if minMs <= 0 || maxMs <= 0 || maxMs < minMs {
		return nil
	}
	delay := time.Duration(minMs+mathrand.Intn(maxMs-minMs+1)) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
