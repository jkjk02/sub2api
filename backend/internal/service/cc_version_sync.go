package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// npmRegistryLatestURL is the npm registry URL for the latest version
const npmRegistryLatestURL = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"

// npmVersionTTL is how long a fetched version remains valid
const npmVersionTTL = 2 * time.Hour

// ccVersionState holds the synced version with expiry
type ccVersionState struct {
	version   string
	fetchedAt time.Time
}

// SyncCLIVersion fetches the latest Claude Code version from npm registry
// and updates the in-memory synced version. Returns the version string.
func (s *GatewayService) SyncCLIVersion(ctx context.Context) (string, error) {
	// Check config override first
	if s.cfg != nil && s.cfg.Gateway.CliSimulation.CCVersionOverride != "" {
		v := s.cfg.Gateway.CliSimulation.CCVersionOverride
		claude.SetSyncedCLIVersion(v)
		slog.Info("cli version from config override", "version", v)
		return v, nil
	}

	// Try dynamic sync from npm
	synced := claude.GetSyncedCLIVersion()
	if synced != "" {
		return synced, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, npmRegistryLatestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create npm request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch npm registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return "", fmt.Errorf("read npm response: %w", err)
	}

	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return "", fmt.Errorf("parse npm response: %w", err)
	}
	if pkg.Version == "" {
		return "", fmt.Errorf("npm response missing version field")
	}

	claude.SetSyncedCLIVersion(pkg.Version)
	slog.Info("cli version synced from npm registry", "version", pkg.Version)
	return pkg.Version, nil
}

// GetEffectiveCLIVersion returns the best available CC version:
// 1. Config override (gateway.cli_simulation.cc_version_override)
// 2. Dynamically synced version (from npm registry)
// 3. Compile-time constant (claude.CLICurrentVersion)
func (s *GatewayService) GetEffectiveCLIVersion() string {
	if s.cfg != nil && s.cfg.Gateway.CliSimulation.CCVersionOverride != "" {
		return s.cfg.Gateway.CliSimulation.CCVersionOverride
	}
	if v := claude.GetSyncedCLIVersion(); v != "" {
		return v
	}
	return claude.CLICurrentVersion
}

// GetEffectiveFingerprintSalt returns the effective billing fingerprint salt
func (s *GatewayService) GetEffectiveFingerprintSalt() string {
	if s.cfg != nil && s.cfg.Gateway.CliSimulation.FingerprintSaltOverride != "" {
		return s.cfg.Gateway.CliSimulation.FingerprintSaltOverride
	}
	return fingerprintSalt
}
