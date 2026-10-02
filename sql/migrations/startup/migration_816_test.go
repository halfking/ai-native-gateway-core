package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 816 契约：canonical 视图的 client_ip 从 NULL 补位改为 session 侧有源投影。
//
// 背景（审计 §9.45.6.1）：740 把它补成 NULL::inet，理由写在 Go 镜像体上的是
// 「session_turns.client_ip 为 text 且**未回填**，不能直映」。后半句已被 252
// 生产库复测证伪——本机近 7 天 session 侧非空 85.0%，252 近 7 天有值 17,586 行；
// 且两族同义（同 request_id 配对 826 行，session_turns.client_ip ==
// host(request_logs.client_ip) 826/826、差异 0）。
//
// 本文件守三件**没人会主动想起**的事：
//  1. 有源投影不能被悄悄改回 NULL 补位（那正是 §9.18「修好了但变全盲」的形状）；
//  2. **守卫不能被去掉**——session_turns.client_ip 是 text、无类型约束，
//     无守卫的 `::inet` 遇到一个畸形值会让**整条 canonical 视图的每个读方**报错。
//     这是本迁移里唯一一个「少写三个字符就可能打挂全站读面」的改动；
//  3. down 迁移必须真的回到 NULL 补位（否则回滚会静默留下一个半吊子形态）。
const migration816 = "816_request_logs_view_client_ip_projection.sql"

func readMigration816(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(b)
}

func TestMigration816ProjectsClientIPWithGuard(t *testing.T) {
	src := readMigration816(t, migration816)
	proj := between(t, src, "proj := $proj$", "$proj$;")

	// ① 有源投影：会话分支必须真的取 t.client_ip，而不是补 NULL。
	if strings.Contains(proj, "NULL::inet AS client_ip") {
		t.Errorf("816 的会话分支又把 client_ip 退回 NULL 补位了。\n" +
			"「session 侧未回填」这个理由已在 252 生产库证伪（本机近 7 天非空 85.0%%、" +
			"252 有值 17,586 行；两族配对 826/826 同义）。退回补位等于让 " +
			"internal/collector/gateway_adapters.go 的 client_ip 维度重新塌成 __unknown__。")
	}
	if !strings.Contains(proj, "t.client_ip::inet") {
		t.Errorf("816 投影里找不到 t.client_ip::inet —— 会话分支没有消费 session_turns.client_ip")
	}

	// ② 守卫：必须有，且必须包住那次转换。
	guard := regexp.MustCompile(`CASE\s+WHEN\s+t\.client_ip\s*~\s*'\^\[0-9a-fA-F:\.\]\+\$'\s+THEN\s+t\.client_ip::inet\s+END`)
	if !guard.MatchString(proj) {
		t.Errorf("816 的 client_ip 投影丢了 CASE 守卫（或守卫的正则被改动）。\n" +
			"session_turns.client_ip 是 **text**、无类型约束，无守卫的 `::inet` 遇到一个畸形值" +
			"会抛错并打挂整条 canonical 视图的每一个读方。真库实测守卫把 `garbage`、" +
			"`1.2.3.4, 5.6.7.8`（多跳 XFF 链）、`''` 全部落成 NULL 而不是报错。\n实际片段：%s",
			clientIPLine(proj))
	}

	// ③ 列序/列数不变：client_ip 仍在 740 的位置（第 115 位），没有被挪走。
	names := between(t, src, "names := $names$", "$names$;")
	if !strings.Contains(names, "credits_rate_multiplier, client_ip, origin_stage, token_band, client_forwarded_for") {
		t.Errorf("816 改了列序 —— client_ip 必须留在 740 的原位（" +
			"它是 CREATE OR REPLACE 合法性的一部分：列名/类型/序号都不能动）")
	}
	// 逐字确认 815 的三列仍在末尾（816 只换一行，别顺手改掉别人的三列）。
	if !strings.HasSuffix(strings.TrimSpace(names), "origin_stage, token_band, client_forwarded_for") {
		t.Errorf("names 末尾三列次序被改动 —— 816 的作用域只有 client_ip 一行")
	}
}

func TestMigration816DownRestoresNullPadding(t *testing.T) {
	raw := readMigration816(t, strings.TrimSuffix(migration816, ".sql")+".down.sql")
	proj := between(t, raw, "proj := $proj$", "$proj$;")

	if !strings.Contains(proj, "NULL::inet AS client_ip") {
		t.Errorf("816 down 没有把 client_ip 还原成 NULL 补位。\n" +
			"半吊子回滚（表达式改了但补位没回来）比不回滚更难排查。")
	}
	if strings.Contains(proj, "t.client_ip::inet") {
		t.Errorf("816 down 里仍有 t.client_ip::inet —— 回滚必须真的撤掉有源投影")
	}
	// down 也不该含 DROP VIEW：816 up 用的是 CREATE OR REPLACE，级联面为空。
	// 若 down 引入 DROP，815.down 头部记录的那条级联（带走
	// v_model_health_dashboard / v_probe_system_health）就会在回滚时被触发。
	//
	// **必须先剥 SQL 行注释再判**：down 文件的头部正写着「级联面：**无**。
	// 本迁移不含 DROP VIEW」——第一版门直接子串匹配，被这句**约束注释本身**
	// 喂饱，误报了一个从未存在的缺陷。一个会被自己的说明文字触发的门，
	// 第一次跑红就会训练人忽略它。
	if regexp.MustCompile(`(?i)DROP\s+VIEW`).MatchString(stripSQLLineComments816(raw)) {
		t.Errorf("816 down 含 DROP VIEW **语句**。\n" +
			"816 up 刻意用 CREATE OR REPLACE（列名/类型/序号都没动，合法）以保持**零级联**；" +
			"down 若改成 DROP+CREATE，会在回滚期间带走 v_model_health_dashboard 与 " +
			"v_probe_system_health（它们只在启动期自愈，在线回滚窗口内是不存在的）。")
	}
}

// stripSQLLineComments816 去掉 `--` 行注释。
//
// 为什么这道门非剥不可：约束类注释天然会**陈述它禁止的东西**
//（「本迁移不含 DROP VIEW」），子串匹配于是自我触发。本仓已有多份
// stripSQLLineComments（admin / bg 各自一份），本包没有；不复用它们是因为
// 它们在别的包内，而**同名包级声明只在同包内冲突**。名字带 816 后缀是
// 为了让「这个副本是为哪道门而存在」一眼可辨。
func stripSQLLineComments816(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// clientIPLine 取出投影里 client_ip 那一行，用于失败信息。
func clientIPLine(proj string) string {
	for _, l := range strings.Split(proj, "\n") {
		if strings.Contains(l, "client_ip") {
			return strings.TrimSpace(l)
		}
	}
	return "<未找到 client_ip 行>"
}
