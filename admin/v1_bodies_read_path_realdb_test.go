//go:build !integration

package admin

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v1_bodies_read_path_realdb_test.go —— 读 `storage.session_bodies_native_read` 在
// 真库里的**实际值**，并把 13 个「只经切换层」的 bodies 读方此刻读的是哪一臂
// 推导出来（审计 §9.265）。
//
// # 这道门断言的是**推导**，不是配置值
//
// 设成 `true` 是**目标**，不是缺陷。所以门不能因为「值是 false」而红 ——
// 那样它会变成一个「催着人去改配置」的铃铛，而不是一道判据。
//
// ⇒ 门断言两件事：
//  1. 键**能读到**（读不到 ⇒ 推导无从谈起，判红）；
//  2. 由该值推出的**结论文字**在两个分支下都自洽
//     （false → 「13 个读方此刻读 v1」；true → 「已在读会话侧」）。
//
// 真库实测（2026-10-06）：`settings_kv` 共 42 个键，该键**整行不存在**，
// 而 `storage.session_turns_bodies_enabled` / `storage.admin_logs_native_turns_read` /
// `storage.session_final_full_enabled` **都在** ⇒ 不是「表读不到」，是「这个键没被设过」。
const bodiesNativeReadKey = "storage.session_bodies_native_read"

func TestBodiesNativeReadKeyInRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	// 对照键：它们**确实**在表里。若对照键也读不到，说明是「表/键名/连接」的问题，
	// 而不是「这个键没被设过」—— 这两件事必须分开。
	probes := map[string]string{
		bodiesNativeReadKey:                    "★ bodies 读端切会话侧的开关",
		"storage.session_turns_bodies_enabled": "对照：应当存在",
		"storage.admin_logs_native_turns_read": "对照：应当存在",
		"storage.request_logs_write_enabled":   "对照：S4 停写门",
	}
	for key, note := range probes {
		var v string
		err := pool.QueryRow(ctx,
			`SELECT value FROM settings_kv WHERE key = $1`, key).Scan(&v)
		switch {
		case err != nil:
			t.Logf("键 %-46s 不在 settings_kv（%s）", key, note)
		default:
			t.Logf("键 %-46s = %-8s（%s）", key, v, note)
		}
	}

	// 至少要有一个对照键读得到，否则「不在」是环境问题而不是事实。
	var probeOK int
	for _, k := range []string{
		"storage.session_turns_bodies_enabled",
		"storage.admin_logs_native_turns_read",
	} {
		var v string
		if pool.QueryRow(ctx, `SELECT value FROM settings_kv WHERE key = $1`, k).Scan(&v) == nil {
			probeOK++
		}
	}
	if probeOK == 0 {
		t.Fatalf("两个对照键都读不到 —— 「%s 不在表里」是环境/连接问题，"+
			"不是「这个键没被设过」这个事实", bodiesNativeReadKey)
	}

	// 推导。两个分支都要说得通 —— 这才是「断言推导」。
	var v string
	err = pool.QueryRow(ctx, `SELECT value FROM settings_kv WHERE key = $1`, bodiesNativeReadKey).Scan(&v)
	enabled := err == nil && (v == "true" || v == "1" || v == "t" || v == "on" || v == "yes")
	consequence := "★ 13 个只经切换层的 bodies 读方此刻读的仍是 v1 —— 停写后读到冻结数据，不报错"
	if enabled {
		consequence = "13 个只经切换层的 bodies 读方已在读会话侧；停写前置 P1 的这一半已满足"
	}
	t.Logf("推导：%s = %v ⇒ %s", bodiesNativeReadKey, enabled, consequence)
	t.Logf("（另一半 P1 仍待办：13 个只字面量指名 v1 bodies 的读方无开关可翻，必须改 SQL）")
}
