package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	claudeTokenRefreshSkew         = 3 * time.Minute
	claudeTokenCacheSkew           = 5 * time.Minute
	claudeLockWaitTime             = 200 * time.Millisecond
	claudeAuthRecoveryWaitAttempts = 5
)

// ClaudeTokenCache token cache interface.
type ClaudeTokenCache = GeminiTokenCache

// ClaudeTokenProvider manages access_token for Claude OAuth and Vertex service account accounts.
type ClaudeTokenProvider struct {
	accountRepo   AccountRepository
	tokenCache    ClaudeTokenCache
	oauthService  *OAuthService
	refreshAPI    *OAuthRefreshAPI
	executor      OAuthRefreshExecutor
	refreshPolicy ProviderRefreshPolicy
}

func NewClaudeTokenProvider(
	accountRepo AccountRepository,
	tokenCache ClaudeTokenCache,
	oauthService *OAuthService,
) *ClaudeTokenProvider {
	return &ClaudeTokenProvider{
		accountRepo:   accountRepo,
		tokenCache:    tokenCache,
		oauthService:  oauthService,
		refreshPolicy: ClaudeProviderRefreshPolicy(),
	}
}

// SetRefreshAPI injects unified OAuth refresh API and executor.
func (p *ClaudeTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

// SetRefreshPolicy injects caller-side refresh policy.
func (p *ClaudeTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

// GetAccessToken returns a valid access_token.
func (p *ClaudeTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformAnthropic || (account.Type != AccountTypeOAuth && account.Type != AccountTypeServiceAccount) {
		return "", errors.New("not an anthropic oauth or service account")
	}
	if account.Type == AccountTypeServiceAccount {
		return p.getServiceAccountAccessToken(ctx, account)
	}

	cacheKey := ClaudeTokenCacheKey(account)

	// 1) Try cache first.
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			slog.Debug("claude_token_cache_hit", "account_id", account.ID)
			return token, nil
		} else if err != nil {
			slog.Warn("claude_token_cache_get_failed", "account_id", account.ID, "error", err)
		}
	}

	slog.Debug("claude_token_cache_miss", "account_id", account.ID)

	// 2) Refresh if needed (pre-expiry skew).
	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= claudeTokenRefreshSkew
	refreshFailed := false

	if needsRefresh && p.refreshAPI != nil && p.executor != nil {
		result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, claudeTokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
			slog.Warn("claude_token_refresh_failed", "account_id", account.ID, "error", logredact.RedactText(err.Error()))
			refreshFailed = true
		} else if result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache && p.tokenCache != nil {
				time.Sleep(claudeLockWaitTime)
				if token, cacheErr := p.tokenCache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					slog.Debug("claude_token_cache_hit_after_wait", "account_id", account.ID)
					return token, nil
				}
			}
		} else {
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && p.tokenCache != nil {
		// Backward-compatible test path when refreshAPI is not injected.
		locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			slog.Warn("claude_token_lock_failed", "account_id", account.ID, "error", lockErr)
		} else {
			time.Sleep(claudeLockWaitTime)
			if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				slog.Debug("claude_token_cache_hit_after_wait", "account_id", account.ID)
				return token, nil
			}
		}
	}

	accessToken := account.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// 3) Populate cache with TTL.
	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			slog.Debug("claude_token_version_stale_use_latest", "account_id", account.ID)
			accessToken = latestAccount.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if refreshFailed {
				if p.refreshPolicy.FailureTTL > 0 {
					ttl = p.refreshPolicy.FailureTTL
				} else {
					ttl = time.Minute
				}
				slog.Debug("claude_token_cache_short_ttl", "account_id", account.ID, "reason", "refresh_failed")
			} else if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > claudeTokenCacheSkew:
					ttl = until - claudeTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			if err := p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
				slog.Warn("claude_token_cache_set_failed", "account_id", account.ID, "error", err)
			}
		}
	}

	return accessToken, nil
}

// RefreshAfterAuthFailure refreshes an Anthropic OAuth token after upstream
// explicitly rejects the access token, even when the local expires_at is still
// in the future. The rejected token is used as a concurrency guard so a token
// already rotated by another request is reused instead of refreshed again.
func (p *ClaudeTokenProvider) RefreshAfterAuthFailure(ctx context.Context, account *Account, rejectedAccessToken string) (string, error) {
	if p == nil || p.refreshAPI == nil || p.executor == nil {
		return "", classifyClaudeOAuthRefreshFailure(errors.New("claude oauth refresh is not configured"))
	}
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformAnthropic || account.Type != AccountTypeOAuth {
		return "", errors.New("not an anthropic oauth account")
	}
	rejectedAccessToken = strings.TrimSpace(rejectedAccessToken)
	if rejectedAccessToken == "" {
		return "", errors.New("rejected access token is empty")
	}

	expectedCredentials, snapshotErr := cloneAccountJSONMap(account.Credentials)
	if snapshotErr != nil {
		return "", &ClaudeOAuthAuthRecoveryError{
			Reason: ClaudeOAuthReasonStateUpdateFailed,
			Scope:  GatewayFailureScopeAccount,
			cause:  snapshotErr,
		}
	}
	result, err := p.refreshAPI.RefreshAfterAccessTokenRejected(
		withOAuthRefreshRequestPath(ctx),
		account,
		p.executor,
		rejectedAccessToken,
	)
	if err != nil {
		return p.handleAuthRecoveryRefreshFailure(ctx, account, expectedCredentials, rejectedAccessToken, err)
	}
	if result == nil {
		return "", classifyClaudeOAuthRefreshFailure(errors.New("claude oauth refresh returned no result"))
	}
	if result.LockHeld {
		return p.waitForReplacementAccessToken(ctx, account, rejectedAccessToken)
	}
	if result.Account == nil {
		return "", classifyClaudeOAuthRefreshFailure(errors.New("claude oauth refresh returned no account"))
	}

	return p.publishReplacementAccessToken(ctx, result.Account, rejectedAccessToken)
}

func (p *ClaudeTokenProvider) handleAuthRecoveryRefreshFailure(
	ctx context.Context,
	account *Account,
	expectedCredentials map[string]any,
	rejectedAccessToken string,
	refreshErr error,
) (string, error) {
	recoveryErr := classifyClaudeOAuthRefreshFailure(refreshErr)
	if recoveryErr == nil || !recoveryErr.ReauthRequired {
		return "", recoveryErr
	}

	conditionalRepo, ok := p.accountRepo.(claudeOAuthAuthFailureRepository)
	if !ok {
		return "", &ClaudeOAuthAuthRecoveryError{
			Reason:         ClaudeOAuthReasonStateUpdateFailed,
			Scope:          GatewayFailureScopeAccount,
			ReauthRequired: true,
			cause:          errors.New("account repository does not support conditional claude oauth quarantine"),
		}
	}
	applied, setErr := conditionalRepo.SetClaudeOAuthErrorIfCredentialsUnchanged(
		ctx,
		account.ID,
		expectedCredentials,
		string(recoveryErr.Reason),
		claudeOAuthReauthRequiredMessage,
	)
	if setErr != nil {
		return "", &ClaudeOAuthAuthRecoveryError{
			Reason:         ClaudeOAuthReasonStateUpdateFailed,
			Scope:          GatewayFailureScopeAccount,
			ReauthRequired: true,
			cause:          setErr,
		}
	}
	if applied {
		invalidateClaudeAccessTokenDetached(p.tokenCache, account)
		recoveryErr.Quarantined = true
		return "", recoveryErr
	}

	// A concurrent reauthorization may have replaced the complete credential
	// document after this request observed the rejected token. Reuse the newer
	// access token rather than overwriting or quarantining the account.
	if p.accountRepo != nil {
		freshAccount, readErr := p.accountRepo.GetByID(ctx, account.ID)
		if readErr == nil && freshAccount != nil {
			freshToken := strings.TrimSpace(freshAccount.GetCredential("access_token"))
			if freshToken != "" && freshToken != rejectedAccessToken && freshAccount.Status == StatusActive && freshAccount.Schedulable {
				return p.publishReplacementAccessToken(ctx, freshAccount, rejectedAccessToken)
			}
		}
	}
	return "", &ClaudeOAuthAuthRecoveryError{
		Reason:         ClaudeOAuthReasonAccountStateChanged,
		Scope:          GatewayFailureScopeAccount,
		ReauthRequired: true,
		cause:          refreshErr,
	}
}

func (p *ClaudeTokenProvider) waitForReplacementAccessToken(ctx context.Context, account *Account, rejectedAccessToken string) (string, error) {
	cacheKey := ClaudeTokenCacheKey(account)
	for attempt := 0; attempt < claudeAuthRecoveryWaitAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(claudeLockWaitTime):
		}

		if p.tokenCache != nil {
			token, cacheErr := p.tokenCache.GetAccessToken(ctx, cacheKey)
			token = strings.TrimSpace(token)
			if cacheErr == nil && token != "" && token != rejectedAccessToken {
				return token, nil
			}
		}
		if p.accountRepo != nil {
			freshAccount, readErr := p.accountRepo.GetByID(ctx, account.ID)
			if readErr == nil && freshAccount != nil {
				token := strings.TrimSpace(freshAccount.GetCredential("access_token"))
				if token != "" && token != rejectedAccessToken {
					return p.publishReplacementAccessToken(ctx, freshAccount, rejectedAccessToken)
				}
			}
		}
	}
	return "", errors.New("timed out waiting for replacement claude access token")
}

func (p *ClaudeTokenProvider) publishReplacementAccessToken(ctx context.Context, account *Account, rejectedAccessToken string) (string, error) {
	accessToken := strings.TrimSpace(account.GetCredential("access_token"))
	if accessToken == "" {
		return "", errors.New("access_token not found after claude oauth refresh")
	}
	if accessToken == rejectedAccessToken {
		return "", errors.New("claude oauth refresh did not replace rejected access token")
	}

	if p.tokenCache != nil {
		ttl := 30 * time.Minute
		if expiresAt := account.GetCredentialAsTime("expires_at"); expiresAt != nil {
			until := time.Until(*expiresAt)
			switch {
			case until > claudeTokenCacheSkew:
				ttl = until - claudeTokenCacheSkew
			case until > 0:
				ttl = until
			default:
				ttl = time.Minute
			}
		}
		if err := p.tokenCache.SetAccessToken(ctx, ClaudeTokenCacheKey(account), accessToken, ttl); err != nil {
			slog.Warn("claude_token_cache_set_failed_after_auth_recovery", "account_id", account.ID, "error", err)
		}
	}
	return accessToken, nil
}

func (p *ClaudeTokenProvider) getServiceAccountAccessToken(ctx context.Context, account *Account) (string, error) {
	return getVertexServiceAccountAccessToken(ctx, p.tokenCache, account)
}
