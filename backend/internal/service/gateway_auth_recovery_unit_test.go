//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type authRecoverySequenceUpstream struct {
	responses   []*http.Response
	authHeaders []string
}

func (u *authRecoverySequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.next(req)
}

func (u *authRecoverySequenceUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.next(req)
}

func (u *authRecoverySequenceUpstream) next(req *http.Request) (*http.Response, error) {
	u.authHeaders = append(u.authHeaders, getHeaderRaw(req.Header, "authorization"))
	if len(u.responses) == 0 {
		return nil, io.EOF
	}
	resp := u.responses[0]
	u.responses = u.responses[1:]
	return resp, nil
}

func TestGatewayServiceForwardRecoversAnthropicOAuthAuthenticationFailureOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := &Account{
		ID:          905,
		Name:        "anthropic-oauth-auth-recovery",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "rejected-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(time.Hour),
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	cache := newClaudeTokenCacheStub()
	cache.tokens[ClaudeTokenCacheKey(account)] = "rejected-token"
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "replacement-token",
			"refresh_token": "replacement-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour),
		},
	}
	provider := NewClaudeTokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	upstream := &authRecoverySequenceUpstream{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired"}}`,
			)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_recovered","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
			)),
		},
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
		claudeTokenProvider:  provider,
	}

	body := []byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, account, parsed)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
	require.Equal(t, []string{"Bearer rejected-token", "Bearer replacement-token"}, upstream.authHeaders)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, "replacement-token", cache.tokens[ClaudeTokenCacheKey(account)])
}
