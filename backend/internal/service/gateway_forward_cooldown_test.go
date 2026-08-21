//go:build unit

package service

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayOAuthCooldownRepoStub struct {
	rateLimitAccountRepoStub
	lastTempUntil time.Time
}

func (r *gatewayOAuthCooldownRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	r.lastTempID = id
	r.lastTempReason = reason
	r.lastTempUntil = until
	return nil
}

func TestGatewayRetryExhaustedAnthropicAPIKey429PersistsCooldown(t *testing.T) {
	tests := []struct {
		name        string
		header      http.Header
		fallback    int
		wantAtLeast time.Duration
	}{
		{name: "retry_after", header: http.Header{"Retry-After": []string{"2"}}, fallback: 7, wantAtLeast: 1500 * time.Millisecond},
		{name: "centralized_fallback", header: http.Header{}, fallback: 7, wantAtLeast: 6500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimit429AccountRepoStub{}
			cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
				StabilityProtectionEnabled:       true,
				RateLimitFallbackCooldownSeconds: tt.fallback,
			}}}
			rateLimitSvc := NewRateLimitService(repo, nil, cfg, nil, nil)
			gateway := &GatewayService{rateLimitService: rateLimitSvc}
			account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			resp := &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     tt.header,
				Body:       ioNopCloserBytes(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`),
			}

			before := time.Now()
			gateway.handleRetryExhaustedSideEffects(context.Background(), resp, account)

			require.Equal(t, 1, repo.rateLimitCalls)
			require.Equal(t, account.ID, repo.lastRateLimitID)
			require.True(t, repo.lastRateLimitReset.After(before.Add(tt.wantAtLeast)), "reset=%v before=%v", repo.lastRateLimitReset, before)
		})
	}
}

func TestGatewayTransientClaudeOAuthRecoveryFailureAppliesCentralizedCooldown(t *testing.T) {
	repo := &gatewayOAuthCooldownRepoStub{}
	cfg := &config.Config{Gateway: config.GatewayConfig{CliSimulation: config.CliSimulationConfig{
		StabilityProtectionEnabled: true,
		OAuthAuthCooldownMinutes:   3,
	}}}
	rateLimitSvc := NewRateLimitService(repo, nil, cfg, nil, nil)
	gateway := &GatewayService{rateLimitService: rateLimitSvc}
	account := &Account{
		ID:       702,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "refresh-token",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Body:       ioNopCloserBytes(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`),
	}
	transient := newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonRefreshTransient, GatewayFailureScopeAccount, context.DeadlineExceeded)

	before := time.Now()
	gateway.handleClaudeOAuthRecoveryFailure(context.Background(), resp, account, transient)

	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, account.ID, repo.lastTempID)
	require.Contains(t, repo.lastTempReason, "OAuth 401")
	require.True(t, repo.lastTempUntil.After(before.Add(2*time.Minute+50*time.Second)), "until=%v before=%v", repo.lastTempUntil, before)
}

func TestGatewayPermanentClaudeOAuthRecoveryFailureKeepsReauthQuarantineSemantics(t *testing.T) {
	repo := &gatewayOAuthCooldownRepoStub{}
	rateLimitSvc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &GatewayService{rateLimitService: rateLimitSvc}
	account := &Account{ID: 703, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: ioNopCloserBytes(`{"error":{"message":"invalid token"}}`)}
	permanent := newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonRefreshTokenInvalid, GatewayFailureScopeAccount, context.Canceled)
	permanent.ReauthRequired = true
	permanent.Quarantined = true

	gateway.handleClaudeOAuthRecoveryFailure(context.Background(), resp, account, permanent)

	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
}

func ioNopCloserBytes(value string) *nopReadCloser {
	return &nopReadCloser{Reader: bytes.NewReader([]byte(value))}
}

type nopReadCloser struct {
	*bytes.Reader
}

func (r *nopReadCloser) Close() error { return nil }
