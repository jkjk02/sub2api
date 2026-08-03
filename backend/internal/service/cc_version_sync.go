package service

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// npmRegistryLatestURL is the npm registry URL for the latest version
const npmRegistryLatestURL = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"

// npmVersionTTL is how long a fetched version remains valid before a refresh.
const npmVersionTTL = 2 * time.Hour

// cliVersionSyncOnce guards the lazy start of the background npm sync loop so it
// only spins up once per process regardless of how many requests race in.
var cliVersionSyncOnce sync.Once

// cliOverridesOnce guards the one-time application of config-driven overrides
// (cc_version_override / cache_control_ttl / fingerprint_salt) to their
// package-level sinks so that every code path reads the overridden value.
var cliOverridesOnce sync.Once

// applyCliSimulationOverrides pushes config overrides into their package-level
// sinks exactly once. Without this the *_override config keys were dead knobs.
func (s *GatewayService) applyCliSimulationOverrides() {
	if s == nil || s.cfg == nil {
		return
	}
	cliOverridesOnce.Do(func() {
		sim := s.cfg.Gateway.CliSimulation
		if sim.CCVersionOverride != "" {
			claude.SetCLIVersionOverride(sim.CCVersionOverride)
			slog.Info("cli simulation: cc version override applied", "version", sim.CCVersionOverride)
		}
		if sim.CacheControlTTLOverride != "" {
			claude.SetCacheControlTTL(sim.CacheControlTTLOverride)
			slog.Info("cli simulation: cache_control ttl override applied", "ttl", sim.CacheControlTTLOverride)
		}
		if sim.FingerprintSaltOverride != "" {
			SetFingerprintSalt(sim.FingerprintSaltOverride)
			slog.Info("cli simulation: fingerprint salt override applied")
		}
		if sp := strings.TrimSpace(sim.SystemPromptStaticOverride); sp != "" {
			claudeCodeSystemPromptExpansion = sp
			slog.Info("cli simulation: system prompt static override applied", "len", len(sp))
		}
	})
}

// ensureCLIVersionSync lazily starts the background npm version sync loop and
// applies config overrides. Safe to call on every request (cheap after first).
func (s *GatewayService) ensureCLIVersionSync(ctx context.Context) {
	if s == nil || s.cfg == nil {
		return
	}
	s.applyCliSimulationOverrides()
	// An explicit override pins the version; no need to poll npm.
	if s.cfg.Gateway.CliSimulation.CCVersionOverride != "" {
		return
	}
	cliVersionSyncOnce.Do(func() {
		// Detach from the request context so the loop outlives the request.
		go s.startCLIVersionSyncLoop(context.WithoutCancel(ctx))
	})
}

// startCLIVersionSyncLoop fetches the latest CC version immediately and then
// refreshes it every npmVersionTTL. Failures are logged and retried next tick.
func (s *GatewayService) startCLIVersionSyncLoop(ctx context.Context) {
	if _, err := s.SyncCLIVersion(ctx); err != nil {
		slog.Warn("cli version initial sync failed", "err", err)
	}
	ticker := time.NewTicker(npmVersionTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.SyncCLIVersion(ctx); err != nil {
				slog.Warn("cli version refresh failed", "err", err)
			}
		}
	}
}

// SyncCLIVersion fetches the latest Claude Code version from the npm registry
// and updates the in-memory synced version. Returns the version string.
//
// Unlike the previous implementation this always performs the fetch (the caller
// controls cadence via the ticker), so a long-running process picks up upstream
// version bumps instead of freezing on the first value forever.
func (s *GatewayService) SyncCLIVersion(ctx context.Context) (string, error) {
	if s.cfg != nil && s.cfg.Gateway.CliSimulation.CCVersionOverride != "" {
		v := s.cfg.Gateway.CliSimulation.CCVersionOverride
		claude.SetCLIVersionOverride(v)
		return v, nil
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

// GetEffectiveCLIVersion returns the process-wide effective CC version
// (override > synced > compile-time constant). It does NOT apply per-account
// version-pool diversity; use resolveAccountCLIVersion for the per-request value.
func (s *GatewayService) GetEffectiveCLIVersion() string {
	return claude.EffectiveCLIVersion()
}

// resolveAccountCLIVersion returns the CC version for THIS account's requests.
// It is the single source of truth that MUST be used for both the User-Agent and
// the billing attribution block (cc_version=...); a mismatch between the two is a
// strong third-party tell.
//
// Priority:
//  1. config override (cc_version_override) — pins everyone to one version
//  2. per-account version pool — deterministic by account ID (stable across
//     requests, spread across the pool between accounts)
//  3. process-wide effective version (synced npm value or compile-time constant)
func (s *GatewayService) resolveAccountCLIVersion(account *Account) string {
	if v := claude.GetCLIVersionOverride(); v != "" {
		return v
	}
	if s.cfg != nil && account != nil {
		if pool := s.cfg.Gateway.CliSimulation.CCVersionPool; len(pool) > 0 {
			return pool[stableIndexForAccount(account.ID, len(pool))]
		}
	}
	return claude.EffectiveCLIVersion()
}

// resolveAccountOSArch returns the OS/Arch pair for THIS account. An explicit
// per-account cli_os/cli_arch wins; otherwise a deterministic pick from the
// configured OSArchPool (stable per account). Empty strings mean "leave the
// process default in place".
func (s *GatewayService) resolveAccountOSArch(account *Account) (osName, arch string) {
	if account == nil {
		return "", ""
	}
	osName, arch = account.GetCLIOS(), account.GetCLIArch()
	if osName != "" && arch != "" {
		return osName, arch
	}
	if s.cfg != nil {
		if pool := s.cfg.Gateway.CliSimulation.OSArchPool; len(pool) > 0 {
			entry := pool[stableIndexForAccount(account.ID, len(pool))]
			if osName == "" {
				osName = entry.OS
			}
			if arch == "" {
				arch = entry.Arch
			}
		}
	}
	return osName, arch
}

// stableIndexForAccount maps an account ID to a stable index in [0,n) so the same
// account always lands on the same pool entry (mimicking a single real user whose
// OS/version does not change every request) while different accounts spread out.
func stableIndexForAccount(accountID int64, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	var buf [8]byte
	v := uint64(accountID)
	for i := 0; i < 8; i++ {
		buf[i] = byte(v >> (8 * i))
	}
	_, _ = h.Write(buf[:])
	return int(h.Sum32() % uint32(n))
}

// GetEffectiveFingerprintSalt returns the effective billing fingerprint salt
// (config override or the built-in default).
func (s *GatewayService) GetEffectiveFingerprintSalt() string {
	if s.cfg != nil && s.cfg.Gateway.CliSimulation.FingerprintSaltOverride != "" {
		return s.cfg.Gateway.CliSimulation.FingerprintSaltOverride
	}
	return fingerprintSalt
}
