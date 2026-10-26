package testguard

import (
	"testing"
)

// TestIsIsolated pins the accept/reject table.
//
// ★ 这张表是本包的全部业务逻辑 ⇒ 必须逐条钉住，尤其是两条「看起来像放行、
//   实际必须拒绝」的：
//   · `llm_gateway`（产品库）—— 库名不含一次性词；
//   · `llm_gateway_test`（长期共享库）—— 它**含 "test"**，
//     但 R86 轮定下的口径是「独立词」+「_it 后缀」，`_test` 不在其中
//     ⇒ ⇒ 仓内另外几份守卫（`bg/feature_stats_worker_test.go` 的
//     `itgate|scratch|test|tmp|bench|pg_`）是**更宽**的口径，会放行它
//     ⇒ ⇒ 这里刻意用**更严**的那一份。
func TestIsIsolated(t *testing.T) {
	cases := []struct {
		name string
		tgt  Target
		want bool
	}{
		{"一次性库（官方门禁的 itgate_ 前缀）", Target{Host: "127.0.0.1", Port: 5432, Database: "itgate_1234_5678"}, true},
		{"一次性库（_it 后缀）", Target{Host: "127.0.0.1", Port: 5432, Database: "llm_routeincident_it"}, true},
		{"一次性库（独立词 scratch）", Target{Host: "127.0.0.1", Port: 5432, Database: "my_scratch_db"}, true},
		{"一次性库（独立词 e2e）", Target{Host: "127.0.0.1", Port: 5432, Database: "foo_e2e"}, true},
		{"一次性库（独立词 tmp）", Target{Host: "127.0.0.1", Port: 5432, Database: "tmp_x"}, true},
		{"一次性库（独立词 bench）", Target{Host: "127.0.0.1", Port: 5432, Database: "bench_x"}, true},
		{"本仓审计容器 15433（库名与产品库同名）", Target{Host: "127.0.0.1", Port: 15433, Database: "llm_gateway"}, true},
		{"本仓审计容器 55432（库名 postgres）", Target{Host: "localhost", Port: 55432, Database: "postgres"}, true},
		{"★ 产品库（本地 5432）", Target{Host: "127.0.0.1", Port: 5432, Database: "llm_gateway"}, false},
		{"★ 长期共享库 llm_gateway_test", Target{Host: "127.0.0.1", Port: 5432, Database: "llm_gateway_test"}, false},
		{"★ 远端产品库（即使库名像一次性库，host 也不是回环也不是容器端口）", Target{Host: "10.0.0.9", Port: 15433, Database: "llm_gateway"}, false},
		{"★ 没指名数据库", Target{Host: "127.0.0.1", Port: 5432, Database: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := IsIsolated(c.tgt)
			if got != c.want {
				t.Fatalf("IsIsolated(%s) = %v（理由 %q），want %v", c.tgt, got, why, c.want)
			}
			if !got && why == "" {
				t.Fatalf("拒绝了却没给理由 ⇒ 拒绝信息必须能指导人怎么配 DSN")
			}
		})
	}
}

// TestParseTargetUsesParsedConfig 确认解析走的是 pgconn 而不是字符串手术。
func TestParseTargetUsesParsedConfig(t *testing.T) {
	tgt, err := ParseTarget("postgres://u:p@127.0.0.1:15433/llm_gateway?sslmode=disable")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tgt.Host != "127.0.0.1" || tgt.Port != 15433 || tgt.Database != "llm_gateway" {
		t.Fatalf("解析结果不对：%+v", tgt)
	}
	ok, why := IsIsolated(tgt)
	if !ok {
		t.Fatalf("审计容器目标应放行，却拒绝了（%q）", why)
	}
}

// TestRequireIsolatedTargetAccepts 确认放行路径返回目标而不是跳过。
func TestRequireIsolatedTargetAccepts(t *testing.T) {
	tgt := RequireIsolatedTarget(t, "postgres://u:p@127.0.0.1:15433/llm_gateway?sslmode=disable")
	if tgt.Port != 15433 || tgt.Database != "llm_gateway" {
		t.Fatalf("返回的目标不对：%+v", tgt)
	}
}
