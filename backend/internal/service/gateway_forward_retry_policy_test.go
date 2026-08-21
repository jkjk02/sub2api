package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"strings"
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

func TestRetryDelayWithinBudgetRejectsWaitThatExhaustsDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	deadline := now.Add(time.Second)

	for _, delay := range []time.Duration{time.Second, 2 * time.Second} {
		if got, ok := retryDelayWithinBudget(now, deadline, delay); ok || got != 0 {
			t.Fatalf("delay %s should not permit another attempt: got=%s ok=%v", delay, got, ok)
		}
	}
	if got, ok := retryDelayWithinBudget(now, deadline, 900*time.Millisecond); !ok || got != 900*time.Millisecond {
		t.Fatalf("short delay should remain eligible: got=%s ok=%v", got, ok)
	}
}

func TestGatewayRetryAttemptContextCancelsWhenResponseHeadersExceedBudget(t *testing.T) {
	attemptCtx, guard := gatewayRetryAttemptContext(context.Background(), false, time.Now().Add(30*time.Millisecond))
	t.Cleanup(guard.close)

	select {
	case <-attemptCtx.Done():
		if attemptCtx.Err() != context.Canceled {
			t.Fatalf("attempt error = %v, want budget cancellation", attemptCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("attempt context was not canceled after response-header budget expired")
	}
}

func TestGatewayRetryAttemptContextKeepsStreamAliveUntilResponseBodyClose(t *testing.T) {
	attemptCtx, guard := gatewayRetryAttemptContext(context.Background(), true, time.Now().Add(30*time.Millisecond))
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("stream-data"))}
	guard.responseReceived(resp)

	time.Sleep(75 * time.Millisecond)
	select {
	case <-attemptCtx.Done():
		t.Fatalf("successful streaming attempt was canceled after retry budget: %v", attemptCtx.Err())
	default:
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read streaming body after retry budget: %v", err)
	}
	if got := string(body); got != "stream-data" {
		t.Fatalf("streaming body = %q, want %q", got, "stream-data")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close streaming body: %v", err)
	}

	select {
	case <-attemptCtx.Done():
		if attemptCtx.Err() != context.Canceled {
			t.Fatalf("attempt error after body close = %v, want cancellation", attemptCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("response Body.Close did not release the attempt context")
	}
}

func TestGatewayRetryAttemptContextPreservesCallerCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	attemptCtx, guard := gatewayRetryAttemptContext(parent, false, time.Now().Add(2*time.Second))
	t.Cleanup(guard.close)

	cancelParent()
	select {
	case <-attemptCtx.Done():
		if attemptCtx.Err() != context.Canceled {
			t.Fatalf("attempt error = %v, want caller cancellation", attemptCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("attempt context did not preserve caller cancellation")
	}
}

func TestGatewayForwardContainsNoDeferInsideLoop(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gateway_forward.go", nil, 0)
	if err != nil {
		t.Fatalf("parse gateway_forward.go: %v", err)
	}

	var forward *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Forward" {
			forward = fn
			break
		}
	}
	if forward == nil {
		t.Fatal("Forward function not found")
	}

	var deferPosition token.Position
	ast.Inspect(forward.Body, func(node ast.Node) bool {
		var loopBody *ast.BlockStmt
		switch loop := node.(type) {
		case *ast.ForStmt:
			loopBody = loop.Body
		case *ast.RangeStmt:
			loopBody = loop.Body
		default:
			return true
		}
		ast.Inspect(loopBody, func(n ast.Node) bool {
			if stmt, ok := n.(*ast.DeferStmt); ok && !deferPosition.IsValid() {
				deferPosition = fset.Position(stmt.Defer)
			}
			return true
		})
		return false
	})
	if deferPosition.IsValid() {
		t.Fatalf("Forward contains a defer inside a loop at %s", deferPosition)
	}
}
