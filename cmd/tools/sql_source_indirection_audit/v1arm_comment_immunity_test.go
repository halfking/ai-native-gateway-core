package main

// v1arm_comment_immunity_test.go —— **剥注释器自己的门**
//
// # 这道门补的是什么
//
// 另一个会话在 2026-10-21 通报了一条通用陷阱：
// **凡对 SQL 文本做子串断言 / 解析，先剥注释** ——
// 注释里出现 `FROM public._request_logs` 足以让「这个视图有 v1 臂」的判定变成**假的**。
//
// 本工具原来只剥**行注释**（`--`），**不剥块注释**（`/* */`）⇒ 那是一个**潜在**缺口：
// 实测（2026-10-21 R65 轮）加上块注释剥离后，工具输出与剥离前**逐字相同**
// ⇒ 今天没有一条判定受影响；仓库现状也印证：`sql/objects/`（1767 个 .sql）、
// composer、883/884 里**一个块注释都没有**。
//
// ★★ 但「今天没影响」**不等于**「不需要判据」：
// 一条只对「仓库现状」成立的判据，等于没有判据 —— 下一个人写一个带块注释的视图 DDL，
// 就会把「部署形态有 v1 臂」的判定弄假（要么假红，要么更糟：**假豁免**）。
//
// ⇒ 这道门**不依赖仓库现状**，用**合成夹具**造出那几种形态，
//   并且**带对照臂**（真读 v1 ⇒ 必须被认出来）——
//   否则「所有用例都判不出 v1」也是绿的（工具瞎了也是绿的）。
//
// ★ 判据分三层：
//	① 端到端（走 `AuditV1Arms` 的**真实**代码路径）：注释里的 v1 提及**不得**
//	   被算成「部署时点 v1 臂」；
//	② **不剥过头**：注释之后的真 SQL 关系名**必须**仍被认出来；
//	③ 剥注释器单元：嵌套块注释**必须**剥干净（PostgreSQL 的块注释**可嵌套**）。

import (
	"strings"
	"testing"
)

func TestV1ArmDetectionIsCommentImmune(t *testing.T) {
	cases := []struct {
		name string
		view string
		ddl  string
		// wantV1Arm = 该视图**是否应当**被算成「部署时点含 v1 臂」。
		wantV1Arm bool
	}{
		{
			name:      "对照臂：真读 v1 ⇒ 必须被算成 v1 臂（否则下面的『不算』是工具瞎了）",
			view:      "v_real_v1_read",
			ddl:       "CREATE VIEW public.v_real_v1_read AS\n SELECT 1 FROM public._request_logs;\n",
			wantV1Arm: true,
		},
		{
			name: "行注释里提到 v1 ⇒ 不是真读",
			view: "v_line_comment",
			ddl: "CREATE VIEW public.v_line_comment AS\n" +
				"-- 历史实现读 FROM public._request_logs（已改会话族）\n" +
				" SELECT 1 FROM public.session_turns;\n",
			wantV1Arm: false,
		},
		{
			name: "块注释里提到 v1 ⇒ 不是真读",
			view: "v_block_comment",
			ddl: "CREATE VIEW public.v_block_comment AS\n" +
				"/* 历史实现读 FROM public._request_logs（已改会话族） */\n" +
				" SELECT 1 FROM public.session_turns;\n",
			wantV1Arm: false,
		},
		{
			name: "嵌套块注释里提到 v1 ⇒ 不是真读（PG 的块注释可嵌套）",
			view: "v_nested_comment",
			ddl: "CREATE VIEW public.v_nested_comment AS\n" +
				"/* 外层 /* 内层 FROM public._request_logs */ 仍在注释里 */\n" +
				" SELECT 1 FROM public.session_turns;\n",
			wantV1Arm: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRepoRoot(t)
			// 未声明的视图 ⇒ 「未声明却含 v1 臂 ⇒ 算部署形态」这条规则生效，
			// 于是「是否被算成 v1 臂」完全由**解析**决定 ⇒ 正好测的是剥注释器。
			r.writeView("v_probe.sql", tc.ddl)
			r.writeChain("901_drop_v1.sql")
			r.writeMigration("901_drop_v1.sql", v1DropMigrationDDL())

			a := AuditV1Arms(r.dir)
			got := false
			for _, v := range a.DeploymentArms {
				if v == tc.view {
					got = true
				}
			}
			if got != tc.wantV1Arm {
				t.Errorf("视图 %s 的「部署时点 v1 臂」判定 = %v，期望 %v\n"+
					"  部署时点 v1 臂集合 = %v\n"+
					"  ⇒ 若判定为 true 而期望 false：注释里的 v1 提及被当成了真读\n"+
					"  ⇒ 若判定为 false 而期望 true：解析器（剥注释器）瞎了",
					tc.view, got, tc.wantV1Arm, a.DeploymentArms)
			}
		})
	}
}

// TestSQLCommentStripperDoesNotEatRealSQL 是**不剥过头**那一腿：
// 剥注释只该减少「可扫的关系名」，绝不该把真 SQL 也吃掉
// （真被吃掉 ⇒ 视图会被判成「无引用」⇒ 它的引用关系凭空消失）。
//
// ★ 必须**三种注释形态各来一遍**：只测块注释时，
//
//	「行注释被剥到文末」这个真实会犯的错会**溜过去**（实测：那样改完门照样绿）。
func TestSQLCommentStripperDoesNotEatRealSQL(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
	}{
		{
			name: "块注释在前",
			ddl: "CREATE VIEW public.v_after_comment AS\n" +
				"/* 一段块注释，里面提到 FROM public._request_logs */\n" +
				" SELECT request_id FROM public.session_turns;\n",
		},
		{
			name: "行注释在前",
			ddl: "CREATE VIEW public.v_after_comment AS\n" +
				"-- 一行行注释，里面提到 FROM public._request_logs\n" +
				" SELECT request_id FROM public.session_turns;\n",
		},
		{
			name: "嵌套块注释在前",
			ddl: "CREATE VIEW public.v_after_comment AS\n" +
				"/* 外层 /* 内层 FROM public._request_logs */ 仍在注释里 */\n" +
				" SELECT request_id FROM public.session_turns;\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edges := sqlFileViewEdges(tc.ddl)
			refs, ok := edges["v_after_comment"]
			if !ok {
				t.Fatalf("剥注释把视图体整个吃掉了（视图没进 edges）：edges=%v", edges)
			}
			if !refs["session_turns"] {
				t.Errorf("剥过头：注释之后的真关系名 public.session_turns 没被认出来 ⇒ refs=%v", refs)
			}
			if refs["_request_logs"] {
				t.Errorf("注释里的 v1 提及被当成了真读 ⇒ refs=%v", refs)
			}
		})
	}
}

// TestStripSQLCommentsHandlesNestedBlockComments 是剥注释器的单元检查。
//
// ★ PostgreSQL 的块注释**可以嵌套**（`/* /* … */ … */`）⇒ 正则配不了对
//
//	⇒ 实现必须是扫描器；这一条把那个要求钉住。
func TestStripSQLCommentsHandlesNestedBlockComments(t *testing.T) {
	in := "a /* 外层 /* 内层 FROM public._request_logs */ 仍在注释里 */ b FROM public.session_turns"
	got := stripSQLComments(in)
	if strings.Contains(got, "_request_logs") {
		t.Errorf("嵌套块注释没剥干净 ⇒ got=%q", got)
	}
	if !strings.Contains(got, "FROM public.session_turns") {
		t.Errorf("剥过头：把注释之后的真 SQL 也吃了 ⇒ got=%q", got)
	}
	if strings.Contains(got, "外层") || strings.Contains(got, "内层") {
		t.Errorf("注释正文残留 ⇒ got=%q", got)
	}
}
