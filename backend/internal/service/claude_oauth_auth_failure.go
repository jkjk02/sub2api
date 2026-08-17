package service

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	ClaudeOAuthReasonRefreshTokenInvalid GatewayFailureReason = "claude_refresh_token_invalid"
	ClaudeOAuthReasonRefreshTokenMissing GatewayFailureReason = "claude_refresh_token_missing"
	ClaudeOAuthReasonRefreshTransient    GatewayFailureReason = "claude_refresh_transient"
	ClaudeOAuthReasonProviderConfig      GatewayFailureReason = "claude_provider_config"
	ClaudeOAuthReasonAccountStateChanged GatewayFailureReason = "claude_account_state_changed"
	ClaudeOAuthReasonStateUpdateFailed   GatewayFailureReason = "claude_state_update_failed"
)

const claudeOAuthReauthRequiredMessage = "Claude OAuth authorization expired; re-authorize this account"

type claudeOAuthAuthFailureRepository interface {
	SetClaudeOAuthErrorIfCredentialsUnchanged(
		ctx context.Context,
		id int64,
		expectedCredentials map[string]any,
		reason string,
		errorMessage string,
	) (bool, error)
}

// ClaudeOAuthAuthRecoveryError is a safe, typed result for gateway failover.
// The wrapped provider error is intentionally omitted from Error() because it
// may contain credentials or provider response bodies.
type ClaudeOAuthAuthRecoveryError struct {
	Reason         GatewayFailureReason
	Scope          GatewayFailureScope
	ReauthRequired bool
	Quarantined    bool
	cause          error
}

func (e *ClaudeOAuthAuthRecoveryError) Error() string {
	if e == nil {
		return "claude oauth auth recovery failed"
	}
	return "claude oauth auth recovery failed: " + string(e.Reason)
}

func (e *ClaudeOAuthAuthRecoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newClaudeOAuthAuthRecoveryError(reason GatewayFailureReason, scope GatewayFailureScope, cause error) *ClaudeOAuthAuthRecoveryError {
	return &ClaudeOAuthAuthRecoveryError{
		Reason: reason,
		Scope:  scope,
		cause:  cause,
	}
}

func classifyClaudeOAuthRefreshFailure(err error) *ClaudeOAuthAuthRecoveryError {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())

	if containsAnyClaudeOAuthFailure(message,
		"invalid_client",
		"unauthorized_client",
		"invalid_scope",
		"unknown scope",
		"oauth refresh is not configured",
	) {
		return newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonProviderConfig, GatewayFailureScopeProvider, err)
	}
	if containsAnyClaudeOAuthFailure(message,
		"no refresh token available",
		"missing refresh token",
		"refresh_token not found",
		"refresh token not found",
	) {
		recoveryErr := newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonRefreshTokenMissing, GatewayFailureScopeAccount, err)
		recoveryErr.ReauthRequired = true
		return recoveryErr
	}
	if containsAnyClaudeOAuthFailure(message,
		"invalid_grant",
		"invalid_refresh_token",
		"invalid refresh token",
		"token_expired",
		"refresh token expired",
		"app_session_terminated",
		"session terminated",
		"refresh_token_reused",
		"refresh_token_invalidated",
		"access_denied",
	) {
		recoveryErr := newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonRefreshTokenInvalid, GatewayFailureScopeAccount, err)
		recoveryErr.ReauthRequired = true
		return recoveryErr
	}
	return newClaudeOAuthAuthRecoveryError(ClaudeOAuthReasonRefreshTransient, GatewayFailureScopeAccount, err)
}

func containsAnyClaudeOAuthFailure(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func invalidateClaudeAccessTokenDetached(cache ClaudeTokenCache, account *Account) {
	if cache == nil || account == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = cache.DeleteAccessToken(ctx, ClaudeTokenCacheKey(account))
}

func claudeOAuthRecoveryFailoverError(recoveryErr *ClaudeOAuthAuthRecoveryError) *UpstreamFailoverError {
	if recoveryErr == nil {
		return &UpstreamFailoverError{
			StatusCode:        http.StatusServiceUnavailable,
			Stage:             GatewayFailureStageAccountAuth,
			Scope:             GatewayFailureScopeAccount,
			Reason:            ClaudeOAuthReasonRefreshTransient,
			NextAccountAction: NextAccountRetry,
			ClientStatusCode:  http.StatusServiceUnavailable,
			ClientMessage:     "Claude OAuth account is temporarily unavailable",
		}
	}
	nextAction := NextAccountRetry
	clientMessage := "Claude OAuth account requires re-authorization"
	if recoveryErr.Scope == GatewayFailureScopeProvider {
		nextAction = NextAccountStop
		clientMessage = "Claude OAuth provider configuration is invalid"
	}
	return &UpstreamFailoverError{
		StatusCode:        http.StatusServiceUnavailable,
		Stage:             GatewayFailureStageAccountAuth,
		Scope:             recoveryErr.Scope,
		Reason:            recoveryErr.Reason,
		NextAccountAction: nextAction,
		ClientStatusCode:  http.StatusServiceUnavailable,
		ClientMessage:     clientMessage,
	}
}
