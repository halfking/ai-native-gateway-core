package db

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPoolCapForInstance 覆盖「池子上限随实例容量自适应」的判据。
//
// ★ 判据（★ 2026-10-30 · 211 生产读数）：
//
//	★ cap = clamp(instanceMax/8, 4, 32)
//	★ 100 槽 ⇒ 12（★ 211 共享实例：★ 网关一家 idle 就占 19~30 槽，★ 并发实测 0~1）
//	★ 1000 槽 ⇒ 32（★ 大集群**行为完全不变** ⇒ 零回归面）
//	★ 未知（探针失败）⇒ 32（★ fail-open：★ 保持既有行为）
func TestPoolCapForInstance(t *testing.T) {
	cases := []struct {
		name        string
		instanceMax int32
		want        int32
	}{
		{name: "211 shared instance: 100 slots", instanceMax: 100, want: 12},
		{name: "184 cluster: 1000 slots keeps 32", instanceMax: 1000, want: 32},
		{name: "between: 200 slots", instanceMax: 200, want: 25},
		{name: "between: 400 slots", instanceMax: 400, want: 32},
		{name: "floor: 8 slots", instanceMax: 8, want: 4},
		{name: "floor: 32 slots", instanceMax: 32, want: 4},
		{name: "unknown (probe failed) keeps default", instanceMax: 0, want: 32},
		{name: "negative (probe failed) keeps default", instanceMax: -1, want: 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolCapForInstance(tc.instanceMax); got != tc.want {
				t.Fatalf("poolCapForInstance(%d) = %d, want %d", tc.instanceMax, got, tc.want)
			}
		})
	}
}

// TestPoolCapForInstanceNeverExceedsInstanceShare 钉住「不许超过实例的 1/8」
// 与「不许超过 32」两条上限（★ 防止有人把分母改小）。
//
// ★ 注：★ 下限 4 会让**极小实例**（instanceMax < 32）的 cap 合法地大于 instanceMax/8
// ⇒ ★ 不变式写成 cap ≤ max(4, instanceMax/8)（★ 上下限 clamp 的定义式）。
func TestPoolCapForInstanceNeverExceedsInstanceShare(t *testing.T) {
	for instanceMax := int32(1); instanceMax <= 2000; instanceMax++ {
		got := poolCapForInstance(instanceMax)
		if got > 32 {
			t.Fatalf("instanceMax=%d ⇒ cap=%d exceeds the absolute cap 32", instanceMax, got)
		}
		share := instanceMax / 8
		if share < 4 {
			share = 4
		}
		if got > share {
			t.Fatalf("instanceMax=%d ⇒ cap=%d exceeds clamp(instanceMax/8, 4, 32) = %d", instanceMax, got, share)
		}
		if got < 4 {
			t.Fatalf("instanceMax=%d ⇒ cap=%d below the floor 4", instanceMax, got)
		}
	}
}

// TestPoolMaxConnsEnvExplicit 钉住「运维显式值优先于实例自适应」。
func TestPoolMaxConnsEnvExplicit(t *testing.T) {
	cases := []struct {
		name string
		env  string
		set  bool
		want bool
	}{
		{name: "unset", env: "", set: false, want: false},
		{name: "valid positive", env: "12", set: true, want: true},
		{name: "spaced valid", env: " 24 ", set: true, want: true},
		{name: "zero is not an explicit choice", env: "0", set: true, want: false},
		{name: "negative is not an explicit choice", env: "-5", set: true, want: false},
		{name: "garbage is not an explicit choice", env: "abc", set: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", tc.env)
			}
			if got := poolMaxConnsEnvExplicit(); got != tc.want {
				t.Fatalf("poolMaxConnsEnvExplicit() with env %q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestPoolCapRespectsExplicitEnvOverride 端到端口径：显式设了 env ⇒
// 池上限**不被**实例自适应改写（★ 运维意图优先于自动推导）。
func TestPoolCapRespectsExplicitEnvOverride(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", "20")
	if !poolMaxConnsEnvExplicit() {
		t.Fatal("explicit env must be recognized as explicit")
	}
	if got := poolMaxConnsFromEnv(32); got != 20 {
		t.Fatalf("poolMaxConnsFromEnv(32) with explicit env = %d, want 20", got)
	}
	// ★ 未显式设时，实例自适应才生效（★ 32 → 12 for a 100-slot instance）。
	t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", "")
	if poolMaxConnsEnvExplicit() {
		t.Fatal("unset env must not be treated as explicit")
	}
	if got := poolCapForInstance(100); got != 12 {
		t.Fatalf("poolCapForInstance(100) = %d, want 12", got)
	}
}

// TestProbeInstanceMaxConnectionsRealDB 探针的**真库腿**。
//
// ★★★ 为什么必须有这条腿（★ 2026-10-30 生产实测的教训）：
//
//	★ 第一版门只测了**纯函数** poolCapForInstance（★ clamp 边界 / 全域不变式 / env 显式性）
//	★ ⇒ ★ **门全绿** ★ 但 211 生产上探针**恒失败**（★ 日志「probe failed, keeping pool cap=32」）
//	★ ⇒ ★ **修复根本没生效** ★ 而 fail-open 让行为保持原样 ⇒ ★ **假绿**。
//	★ 真因：★ `SHOW max_connections` 返回 **text（OID 25）** ⇒ ★ 扫进 *int32 必然失败
//	★ ⇒ ★ 纯函数门**结构上不可能**发现它（★ 它压根不碰数据库）。
//	★ ⇒ ⇒ 本腿**真的连库跑探针** ⇒ ★ 扫错类型 ⇒ ★ 探针返回 false ⇒ ★ 本门红。
//
// ★ 门控（★ 与仓内既有真库门同形态）：★ 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
func TestProbeInstanceMaxConnectionsRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 真库腿跳过" +
			"（★ 注意：★ 本门是探针**唯一**能抓到扫描类型错误的腿；★ 纯函数门抓不到）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	got, ok := probeInstanceMaxConnections(ctx, dsn)
	if !ok {
		t.Fatal("probeInstanceMaxConnections returned ok=false against a reachable database — " +
			"the probe cannot read max_connections (★ check the scan target type: " +
			"SHOW max_connections returns text/OID 25, NOT int4)")
	}
	if got <= 0 {
		t.Fatalf("probeInstanceMaxConnections returned %d, want a positive instance capacity", got)
	}
	// ★ 交叉核对：★ 同一个库上用独立连接再查一次（★ 不同实现 ⇒ 不同失败模式）。
	want := queryMaxConnectionsViaPool(t, ctx, dsn)
	if got != want {
		t.Fatalf("probe says max_connections=%d, but the pool-based cross-check says %d", got, want)
	}
	// ★ 不变式：★ 探针读到的值必须能推出一个合法 cap。
	if cap := poolCapForInstance(got); cap < 4 || cap > 32 {
		t.Fatalf("poolCapForInstance(%d) = %d, outside the legal clamp range [4,32]", got, cap)
	}
}

// queryMaxConnectionsViaPool 用**池**（而非独立连接）再读一次 max_connections。
//
// ★ 存在的理由：★ 探针走独立连接、★ 交叉核对走池 ⇒ ★ 两条**不同**的实现路径
// ⇒ ★ 任一条坏掉都会被另一条抓到（★ 而不是一个恒绿摆设）。
func queryMaxConnectionsViaPool(t *testing.T, ctx context.Context, dsn string) int32 {
	t.Helper()
	cfg, err := newPoolConfig(dsn)
	if err != nil {
		t.Fatalf("newPoolConfig: %v", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	defer pool.Close()
	var raw string
	if err := pool.QueryRow(ctx, "SHOW max_connections").Scan(&raw); err != nil {
		t.Fatalf("pool cross-check query: %v", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		t.Fatalf("pool cross-check parse %q: %v", raw, err)
	}
	return int32(n)
}
