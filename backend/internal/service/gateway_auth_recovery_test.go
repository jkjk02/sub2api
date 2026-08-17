package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAnthropicOAuthAuthenticationFailure(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "unauthorized status",
			statusCode: http.StatusUnauthorized,
			body:       `{"type":"error","error":{"type":"permission_error","message":"unauthorized"}}`,
			want:       true,
		},
		{
			name:       "structured authentication error on bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired"}}`,
			want:       true,
		},
		{
			name:       "explicit invalid access token on forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"type":"permission_error","message":"Invalid access token"}}`,
			want:       true,
		},
		{
			name:       "ordinary permission error",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"type":"permission_error","message":"This account cannot access the requested model"}}`,
			want:       false,
		},
		{
			name:       "count tokens route compatibility error",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"Invalid URL: POST /v1/messages/count_tokens"}}`,
			want:       false,
		},
		{
			name:       "thinking signature error",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`,
			want:       false,
		},
		{
			name:       "thinking budget validation error",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"budget_tokens must be greater than max_tokens"}}`,
			want:       false,
		},
		{
			name:       "server error is never auth recovery",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"type":"authentication_error","message":"Invalid access token"}}`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isAnthropicOAuthAuthenticationFailure(tt.statusCode, []byte(tt.body)))
		})
	}
}
