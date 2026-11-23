package store

import (
	"context"
	"errors"
	"testing"
)

func TestServerAccountsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedServer(t, st, "srv-a", "a", ModeAgent, "", nil)
	seedServer(t, st, "srv-b", "b", ModeAgent, "", nil)

	now := NowMS()
	first := &ServerAccount{
		ID: "acc-1", ServerID: "srv-a", Name: "宝塔", Address: "https://1.2.3.4:8888",
		Username: "admin", PasswordEnc: []byte("cipher-1"), Note: "面板账号",
		CustomFieldsJSON: `{"环境":"生产"}`, LoginScript: "printf '{\"output\":\"ok\"}'",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateServerAccount(ctx, first); err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	second := &ServerAccount{
		ID: "acc-2", ServerID: "srv-a", Name: "SSH", CreatedAt: now + 1, UpdatedAt: now + 1,
	}
	if err := st.CreateServerAccount(ctx, second); err != nil {
		t.Fatalf("创建第二个账号失败: %v", err)
	}

	list, err := st.ListServerAccounts(ctx, "srv-a")
	if err != nil {
		t.Fatalf("列账号失败: %v", err)
	}
	if len(list) != 2 || list[0].ID != "acc-1" {
		t.Fatalf("账号列表不符: %+v", list)
	}

	got, err := st.GetServerAccount(ctx, "srv-a", "acc-1")
	if err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if got.Note != "面板账号" || string(got.PasswordEnc) != "cipher-1" {
		t.Fatalf("账号字段不符: %+v", got)
	}
	if _, err := st.GetServerAccount(ctx, "srv-b", "acc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨服务器读取期望 ErrNotFound，实际 %v", err)
	}

	name := "宝塔面板"
	note := "更新后的备注"
	if _, err := st.UpdateServerAccount(ctx, "srv-a", "acc-1", ServerAccountUpdate{
		Name: &name, Note: &note, PasswordSet: true, PasswordEnc: nil, UpdatedAt: now + 2,
	}); err != nil {
		t.Fatalf("更新账号失败: %v", err)
	}
	got, err = st.GetServerAccount(ctx, "srv-a", "acc-1")
	if err != nil {
		t.Fatalf("更新后读取账号失败: %v", err)
	}
	if got.Name != name || got.Note != note || len(got.PasswordEnc) != 0 {
		t.Fatalf("更新未生效: %+v", got)
	}

	if err := st.DeleteServerAccount(ctx, "srv-a", "acc-1"); err != nil {
		t.Fatalf("删除账号失败: %v", err)
	}
	if _, err := st.GetServerAccount(ctx, "srv-a", "acc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后期望 ErrNotFound，实际 %v", err)
	}

	// 删除服务器必须连带清掉账号。
	if err := st.DeleteServer(ctx, "srv-a"); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}
	list, err = st.ListServerAccounts(ctx, "srv-a")
	if err != nil {
		t.Fatalf("删除服务器后列账号失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("删除服务器后账号应清空，实际 %+v", list)
	}
}
