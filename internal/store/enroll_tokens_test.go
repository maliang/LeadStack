package store

import (
	"context"
	"errors"
	"testing"
)

func TestUsedEnrollTokenSurvivesExpiryForReconnect(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := NowMS()
	used := now - 5000
	tok := &EnrollToken{
		ID:        "tok-reconnect",
		TokenHash: "hash-reconnect",
		Note:      "agent",
		ExpiresAt: now - 1000,
		UsedAt:    &used,
	}
	if err := st.CreateEnrollToken(ctx, tok); err != nil {
		t.Fatalf("创建令牌失败: %v", err)
	}
	// 未使用过的过期令牌仍拒绝。
	unused := &EnrollToken{
		ID:        "tok-unused",
		TokenHash: "hash-unused",
		ExpiresAt: now - 1000,
	}
	if err := st.CreateEnrollToken(ctx, unused); err != nil {
		t.Fatalf("创建未使用令牌失败: %v", err)
	}
	if _, err := st.FindEnrollTokenByHash(ctx, unused.TokenHash, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("未使用的过期令牌应返回 ErrExpired，实际 %v", err)
	}
	// 已使用过的令牌即使 TTL 过期，也必须允许 agent 重连。
	got, err := st.FindEnrollTokenByHash(ctx, tok.TokenHash, now)
	if err != nil {
		t.Fatalf("已使用的过期令牌应允许重连，实际 %v", err)
	}
	if got.ID != tok.ID {
		t.Fatalf("返回令牌不符: %+v", got)
	}
}
