package service

import (
	mathrand "math/rand"
	"time"
)

// applyInterRequestDelay inserts configured jitter before the first upstream attempt.
func (s *GatewayService) applyInterRequestDelay() {
	if s.cfg == nil {
		return
	}
	minMs := s.cfg.Gateway.CliSimulation.MinInterRequestDelayMs
	maxMs := s.cfg.Gateway.CliSimulation.MaxInterRequestDelayMs
	if minMs <= 0 || maxMs <= 0 || maxMs < minMs {
		return
	}
	delay := time.Duration(minMs+mathrand.Intn(maxMs-minMs+1)) * time.Millisecond
	time.Sleep(delay)
}
