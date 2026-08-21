package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestGatewayRetryPolicyDoesNotRepeatOAuthAuthFailures(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if svc.shouldRetryUpstreamError(account, statusCode) {
			t.Fatalf("OAuth status %d must not enter generic same-credential retry", statusCode)
		}
	}
}

func TestGatewayRetryPolicyUsesConservativeStatusesWhenProtectionEnabled(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CliSimulation: config.CliSimulationConfig{StabilityProtectionEnabled: true},
	}}}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	tests := []struct {
		statusCode int
		want       bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusRequestTimeout, true},
		{http.StatusConflict, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
	}
	for _, tt := range tests {
		if got := svc.shouldRetryUpstreamError(account, tt.statusCode); got != tt.want {
			t.Fatalf("status %d retry = %v, want %v", tt.statusCode, got, tt.want)
		}
	}
}

func TestGatewayRetryPolicyHonorsConfiguredLimits(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CliSimulation: config.CliSimulationConfig{
			StabilityProtectionEnabled: true,
			RespectRetryAfter:          true,
			RetryJitterEnabled:         true,
			MaxRetryAttempts:           4,
			RetryBaseDelayMs:           250,
			RetryMaxDelayMs:            2_000,
			RetryMaxElapsedSeconds:     12,
		},
	}}}

	policy := svc.gatewayRetryPolicy()
	if policy.maxAttempts != 4 || policy.baseDelay != 250*time.Millisecond || policy.maxDelay != 2*time.Second || policy.maxElapsed != 12*time.Second {
		t.Fatalf("unexpected retry policy: %+v", policy)
	}
	if !policy.jitter || !policy.retryAfter {
		t.Fatalf("expected jitter and Retry-After support: %+v", policy)
	}
}

func TestGatewayRetryPolicyHonorsRetryAfter(t *testing.T) {
	svc := &GatewayService{}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "2")
	policy := gatewayRetryPolicy{
		baseDelay:  100 * time.Millisecond,
		maxDelay:   time.Second,
		retryAfter: true,
	}

	delay := svc.retryBackoffDelay(1, resp, policy)
	if delay < 1500*time.Millisecond || delay > 2500*time.Millisecond {
		t.Fatalf("Retry-After delay = %s, want approximately 2s", delay)
	}
}

func TestGatewayFailoverPolicyDoesNotRetryAuthFailuresOnSameAccount(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if shouldRetryFailoverOnSameAccount(account, statusCode) {
			t.Fatalf("auth status %d must switch or quarantine instead of retrying the same account", statusCode)
		}
	}
}
