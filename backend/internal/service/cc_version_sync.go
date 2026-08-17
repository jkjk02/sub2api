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
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// npmRegistryLatestURL is the npm registry URL for the latest version.
const npmRegistryLatestURL = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"

// npmVersionTTL is how long a fetched version remains valid before a refresh.
const npmVersionTTL = 2 * time.Hour

type cliVersionRuntime struct {
	startOnce     sync.Once
	overridesOnce sync.Once
	client        *http.Client
	ctx           context.Context
	cancel        context.CancelFunc
	syncedVersion atomic.Value // string
}

func newCLIVersionRuntime() *cliVersionRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &cliVersionRuntime{
		client: &http.Client{Timeout: 10 * time.Second},
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *GatewayService) getCLIVersionRuntime() *cliVersionRuntime {
	if s == nil {
		return nil
	}
	s.cliVersionRuntimeOnce.Do(func() {
		s.cliVersionRuntime = newCLIVersionRuntime()
	})
	return s.cliVersionRuntime
}

// applyCliSimulationOverrides records service-scoped override activation once.
// Request-critical values are read from this GatewayService's config instead of
// mutating package globals, so multiple service instances cannot contaminate each other.
func (s *GatewayService) applyCliSimulationOverrides() {
	if !s.legacyCLIProtocolEnabled() {
		return
	}
	runtime := s.getCLIVersionRuntime()
	if runtime == nil {
		return
	}
	runtime.overridesOnce.Do(func() {
		sim := s.cfg.Gateway.CliSimulation
		if sim.CCVersionOverride != "" {
			slog.Info("cli simulation: cc version override active", "version", sim.CCVersionOverride)
		}
		if sim.CacheControlTTLOverride != "" {
			slog.Info("cli simulation: cache_control ttl override active", "ttl", sim.CacheControlTTLOverride)
		}
		if sim.FingerprintSaltOverride != "" {
			slog.Info("cli simulation: fingerprint salt override active")
		}
		if sp := strings.TrimSpace(sim.SystemPromptStaticOverride); sp != "" {
			slog.Info("cli simulation: system prompt static override active", "len", len(sp))
		}
	})
}

// ensureCLIVersionSync lazily starts one background npm version sync loop per
// GatewayService. The worker is owned by the service rather than a request context.
func (s *GatewayService) ensureCLIVersionSync(_ context.Context) {
	if !s.legacyCLIProtocolEnabled() {
		return
	}
	s.applyCliSimulationOverrides()
	if strings.TrimSpace(s.cfg.Gateway.CliSimulation.CCVersionOverride) != "" {
		return
	}
	runtime := s.getCLIVersionRuntime()
	if runtime == nil {
		return
	}
	runtime.startOnce.Do(func() {
		go s.startCLIVersionSyncLoop(runtime.ctx)
	})
}

// StopCLIVersionSync cancels the service-owned npm synchronization worker.
// It is idempotent and safe even when synchronization was never started.
func (s *GatewayService) StopCLIVersionSync() {
	if runtime := s.getCLIVersionRuntime(); runtime != nil {
		runtime.cancel()
	}
}

// startCLIVersionSyncLoop fetches the latest CC version immediately and then
// refreshes it every npmVersionTTL. Failures are logged and retried next tick.
func (s *GatewayService) startCLIVersionSyncLoop(ctx context.Context) {
	if _, err := s.SyncCLIVersion(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("cli version initial sync failed", "err", err)
	}
	ticker := time.NewTicker(npmVersionTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.SyncCLIVersion(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("cli version refresh failed", "err", err)
			}
		}
	}
}

// SyncCLIVersion fetches the latest Claude Code version from the npm registry
// and updates this service's in-memory synced version.
func (s *GatewayService) SyncCLIVersion(ctx context.Context) (string, error) {
	if s == nil {
		return "", fmt.Errorf("gateway service required")
	}
	if s.legacyCLIProtocolEnabled() {
		if v := strings.TrimSpace(s.cfg.Gateway.CliSimulation.CCVersionOverride); v != "" {
			return v, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, npmRegistryLatestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create npm request: %w", err)
	}

	runtime := s.getCLIVersionRuntime()
	resp, err := runtime.client.Do(req)
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
	pkg.Version = strings.TrimSpace(pkg.Version)
	if pkg.Version == "" {
		return "", fmt.Errorf("npm response missing version field")
	}

	runtime.syncedVersion.Store(pkg.Version)
	slog.Info("cli version synced from npm registry", "version", pkg.Version)
	return pkg.Version, nil
}

// GetEffectiveCLIVersion returns this service's effective CC version
// (service config override > service-synced value > compile-time constant).
func (s *GatewayService) GetEffectiveCLIVersion() string {
	if s != nil && s.legacyCLIProtocolEnabled() {
		if v := strings.TrimSpace(s.cfg.Gateway.CliSimulation.CCVersionOverride); v != "" {
			return v
		}
		if runtime := s.getCLIVersionRuntime(); runtime != nil {
			if v, ok := runtime.syncedVersion.Load().(string); ok && v != "" {
				return v
			}
		}
	}
	return claude.CLICurrentVersion
}

// resolveAccountCLIVersion returns the CC version for this account's requests.
// Priority: service override, stable account pool, service-synced value, constant.
func (s *GatewayService) resolveAccountCLIVersion(account *Account) string {
	if s != nil && s.legacyCLIProtocolEnabled() {
		if v := strings.TrimSpace(s.cfg.Gateway.CliSimulation.CCVersionOverride); v != "" {
			return v
		}
		if account != nil {
			if pool := s.cfg.Gateway.CliSimulation.CCVersionPool; len(pool) > 0 {
				if v := strings.TrimSpace(pool[stableIndexForAccount(account.ID, len(pool))]); v != "" {
					return v
				}
			}
		}
	}
	return s.GetEffectiveCLIVersion()
}

// resolveAccountOSArch returns the OS/Arch pair for this account. Explicit
// account values win; the configured diversity pool is legacy-protocol only.
func (s *GatewayService) resolveAccountOSArch(account *Account) (osName, arch string) {
	if account == nil {
		return "", ""
	}
	osName, arch = account.GetCLIOS(), account.GetCLIArch()
	if osName != "" && arch != "" {
		return osName, arch
	}
	if s != nil && s.legacyCLIProtocolEnabled() {
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

// stableIndexForAccount maps an account ID to a stable index in [0,n).
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

// GetEffectiveFingerprintSalt returns this service's effective billing salt.
func (s *GatewayService) GetEffectiveFingerprintSalt() string {
	if s != nil && s.legacyCLIProtocolEnabled() {
		if v := strings.TrimSpace(s.cfg.Gateway.CliSimulation.FingerprintSaltOverride); v != "" {
			return v
		}
	}
	return fingerprintSalt
}

// getEffectiveSystemPromptExpansion returns the service-scoped static prompt
// override when legacy synthesis is active, otherwise the built-in default.
func (s *GatewayService) getEffectiveSystemPromptExpansion() string {
	if s != nil && s.legacyCLIProtocolEnabled() {
		if v := strings.TrimSpace(s.cfg.Gateway.CliSimulation.SystemPromptStaticOverride); v != "" {
			return v
		}
	}
	return claudeCodeSystemPromptExpansion
}
