package admin

// auto_route_tuning_test.go — R65 修复的单测（纯函数、无 DB）。
//
// 覆盖：
//   - keyword_add / weight_adjust 提案 key 白名单拒绝（R65 P3）：白名单外
//     的 key 在任何 DB 访问之前即被 conflictErrf 拒绝（errors.Is 可识别 →
//     approveProposal 映射 409）。
//
// 未覆盖（待真库契约）：rejectProposal 的 404/409 分支需要真库事务
//（SELECT ... FOR UPDATE，db 为具体 *pgxpool.Pool，无 sqlmock/接口桩基建）；
// applyKeywordAddInTx 的 fail-closed unmarshal 分支同样需要可 FOR UPDATE 的
// 参数行。均标注待真库契约回归。
//
// 实现说明：nil pgx.Tx 在此是安全的——白名单校验位于两个 apply 函数的最前
// （先于任何 tx.QueryRow），校验失败即返回，nil tx 永不被触碰。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestApplyKeywordAddProposalRejectsUnknownKey(t *testing.T) {
	// R65：提案 key 不在 tuning_params 已知键集内 → conflictErrf（409 语义），
	// 且必须在触库前拒绝（nil tx 不被触碰即为证）。
	err := applyKeywordAddInTx(context.Background(), nil, map[string]any{
		"key": "keywords.bogus",
		"add": []any{"token"},
	})
	if !errors.Is(err, errProposalConflict) {
		t.Fatalf("err = %v, want errProposalConflict (409 mapping)", err)
	}
	if !strings.Contains(err.Error(), "not appliable") {
		t.Fatalf("err text = %q, want not-appliable reason", err.Error())
	}
	if !strings.Contains(err.Error(), "keywords.bogus") {
		t.Fatalf("err text = %q, want offending key echoed", err.Error())
	}
}

func TestApplyWeightAdjustProposalRejectsUnknownKey(t *testing.T) {
	// R65：weight key 白名单——dimension 白名单已存在（R64 前），这里钉住
	// key 侧的拒绝语义。
	err := applyWeightAdjustInTx(context.Background(), nil, map[string]any{
		"key":       "weights.bogus",
		"dimension": "match",
		"new":       1.0,
	})
	if !errors.Is(err, errProposalConflict) {
		t.Fatalf("err = %v, want errProposalConflict (409 mapping)", err)
	}
	if !strings.Contains(err.Error(), "not appliable") {
		t.Fatalf("err text = %q, want not-appliable reason", err.Error())
	}
	if !strings.Contains(err.Error(), "weights.bogus") {
		t.Fatalf("err text = %q, want offending key echoed", err.Error())
	}
}

func TestProposalKeyWhitelistsCoverKnownKeySets(t *testing.T) {
	// 白名单键集与 taskprofile 生成器/autoroute 已知键对齐：keyword 通道与
	// taskprofile.keywordChannels 一致，weight 键与 02-seed 的三张 profile
	// 权重行一致。防止未来扩键时只改一处漏掉另一处。
	for _, k := range []string{"keywords.reasoning", "keywords.code", "keywords.creative"} {
		if !allowedKeywordKeys[k] {
			t.Errorf("allowedKeywordKeys missing %q", k)
		}
	}
	for _, k := range []string{"weights.smart", "weights.speed_first", "weights.cost_first"} {
		if !allowedWeightKeys[k] {
			t.Errorf("allowedWeightKeys missing %q", k)
		}
	}
	if len(allowedKeywordKeys) != 3 || len(allowedWeightKeys) != 3 {
		t.Fatalf("whitelist sizes = %d/%d, want 3/3 (no stray keys)",
			len(allowedKeywordKeys), len(allowedWeightKeys))
	}
}
