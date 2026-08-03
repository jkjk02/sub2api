package service

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildOAuthMetadataUserID_FallbackWithoutAccountUUID(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	account := &Account{
		ID:    123,
		Type:  AccountTypeOAuth,
		Extra: map[string]any{}, // intentionally missing account_uuid / claude_user_id
	}

	fp := &Fingerprint{ClientID: "deadbeef"} // should be used as user id in legacy format

	got := svc.buildOAuthMetadataUserID(parsed, account, fp)
	require.NotEmpty(t, got)

	// Legacy format: user_{client}_account__session_{uuid}
	re := regexp.MustCompile(`^user_[a-zA-Z0-9]+_account__session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

func TestBuildOAuthMetadataUserID_UsesAccountUUIDWhenPresent(t *testing.T) {
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	account := &Account{
		ID:   123,
		Type: AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "acc-uuid",
			"claude_user_id":    "clientid123",
			"anthropic_user_id": "",
		},
	}

	got := svc.buildOAuthMetadataUserID(parsed, account, nil)
	require.NotEmpty(t, got)

	// New format: user_{client}_account_{account_uuid}_session_{uuid}
	re := regexp.MustCompile(`^user_clientid123_account_acc-uuid_session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

// TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns 验证
// buildOAuthMetadataUserID 生成的 session_id 在同一会话内跨轮保持稳定
//（真实 Claude Code 的 session_id 是进程级稳定的，随对话追加 messages 不变）。
// 每请求随机 UUID 反而是自动化特征，故这里断言"稳定"而非"随机"。
func TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{ID: 777, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "acc-uuid"}}
	fp := &Fingerprint{ClientID: "clientid777", UserAgent: "claude-cli/2.1.161 (external, cli)"}

	mustParse := func(body string) *ParsedRequest {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
		require.NoError(t, err)
		return parsed
	}

	round1 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"}]}`)

	id1 := svc.buildOAuthMetadataUserID(round1, account, fp)
	id2 := svc.buildOAuthMetadataUserID(round1, account, fp)

	require.NotEmpty(t, id1)
	require.NotEmpty(t, id2)

	// 相同请求应产生完全相同的 metadata.user_id（含稳定 session_id）
	require.Equal(t, id1, id2, "session_id should be deterministic for the same conversation")

	// 验证生成的 ID 格式正确（包含 account_uuid 和 session_id 字段）
	parsed1 := ParseMetadataUserID(id1)
	require.NotNil(t, parsed1)
	require.Equal(t, "acc-uuid", parsed1.AccountUUID)
	require.NotEmpty(t, parsed1.SessionID)

	// 第二轮：在尾部追加 assistant/user 消息，但首条 user 文本不变 → session_id 保持稳定
	round2 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"an answer"},` +
		`{"role":"user","content":"second question"}]}`)
	id3 := svc.buildOAuthMetadataUserID(round2, account, fp)
	parsed3 := ParseMetadataUserID(id3)
	require.NotNil(t, parsed3)
	require.Equal(t, parsed1.SessionID, parsed3.SessionID, "session_id should stay stable as the conversation grows")

	// 不同首条 user 文本（不同会话）应得到不同 session_id
	roundOther := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"a completely different opening"}]}`)
	idOther := svc.buildOAuthMetadataUserID(roundOther, account, fp)
	parsedOther := ParseMetadataUserID(idOther)
	require.NotNil(t, parsedOther)
	require.NotEqual(t, parsed1.SessionID, parsedOther.SessionID, "different conversations should yield different session IDs")
}
