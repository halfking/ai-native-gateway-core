package admin

import (
	"context"
	"testing"
	"time"
)

// r1039_revoked_by_password_change_test.go — 2026-10-28 批判性审计轮补的判据。
//
// ## 为什么要补这条（★ 本轮审出的一处「声名」）
//
// r1039 后续轮把「改密即吊销」接到 Echo 侧时，**新增的 6 条判据全部 stub 了探针**
// （`cmd/gateway/r1039_jwt_middleware_revocation_test.go` 换的是 package main 的
// `jwtRevocationProbe`）⇒ ⇒ **生产路径上的 `RevokedByPasswordChange`
// 在任何测试里都没有被执行过一次**。
//
// ⇒ 若有人把这个导出外壳写坏（例如让它恒返回 false，或绕过探针直接读表），
//   **上一轮的 6 条判据照样全绿** ⇒ 那就是「声名一下就算完成」。
//
// ⇒ 本条用**真探针**（`probeAuthRevocation`，不 stub）钉住导出外壳的语义：
//   - 签发时间早于纪元 ⇒ 吊销；
//   - 签发时间晚于纪元 ⇒ 存活；
//   - 没有纪元的用户 ⇒ 存活；
//   - 没有数据库 ⇒ **fail-open**（放行）。
//
// ★ 四个断言是**双向**的：恒假 ⇒ 第 1 条红；恒真 ⇒ 第 2/3 条红；
//   恒真恒假都过不掉 ⇒ 「外壳只是转发」这件事被钉死。

func TestR1039RevokedByPasswordChangeUsesTheRealProbe(t *testing.T) {
	now := time.Now()
	// fillRevocationCache 直接把纪元装进进程内缓存 ⇒ 真探针读得到它
	// （真库腿由 cmd/gateway 侧的真库判据覆盖，见 r1039_jwt_middleware_revocation_realdb_test.go）。
	//
	// ★ 纪元取**过去**一小时（不是未来）—— 纪元是「改密那一刻」，
	//   语义上必然早于「改密之后签发的会话」⇒ 断言「纪元之后签发 ⇒ 存活」成立。
	pool := fillRevocationCache(t, map[string]string{
		"7": now.Add(-time.Hour).UTC().Format(time.RFC3339Nano),
	})

	t.Run("issued before epoch is revoked", func(t *testing.T) {
		if !RevokedByPasswordChange(context.Background(), pool, 7, now.Add(-2*time.Hour)) {
			t.Fatal("token issued before the revocation epoch must be revoked")
		}
	})
	t.Run("issued after epoch survives", func(t *testing.T) {
		if RevokedByPasswordChange(context.Background(), pool, 7, now) {
			t.Fatal("token issued after the revocation epoch must survive")
		}
	})
	t.Run("user without an epoch survives", func(t *testing.T) {
		if RevokedByPasswordChange(context.Background(), pool, 99, now.Add(-time.Hour)) {
			t.Fatal("user without a recorded epoch must survive")
		}
	})
	t.Run("no database fails open", func(t *testing.T) {
		if RevokedByPasswordChange(context.Background(), nil, 7, now.Add(-time.Hour)) {
			t.Fatal("nil database must fail open (settings_kv outage must not lock the admin plane out)")
		}
	})
}
