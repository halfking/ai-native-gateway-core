//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// ★ 它在部署路径上**不是每次都跑**（2026-11-26 核对过 scripts/deploy-test-gate.sh）：部署闸门按
// 「diff 触及的包」选包 ⇒ 本门只在这批改动碰到 scripts/ursmcheck/ 时才会在部署前跑一次。
// 对生产代码的改动，部署路径上靠的是 admin 与 domains/authentication 的两道绊线；别的包里新增的
// 引用要等下一次全量慢门才红——这是本门已登记的覆盖缺口（见证据档 §8）。
// 另：本包是重包，触及它的 diff 会把整包拖进部署闸门（仓内登记的实测是 14 分钟以上）。
//
// schema_delivery_gate_test.go —— 「生产 SQL 引用的表与列，必须由部署与新装真的会执行的来源创建」
// （2026-11-26 本会话系列第 30 轮）。判据本体在 internal/schemadelivery。
//
// # 这道门治什么病
//
//	同一类缺口已经出过多次，每次都是「代码先到、建对象的迁移没到」：
//
//	  · 2026-11-03：全新安装的 outbox_events 缺 last_attempt_at（932 漏建）
//	    ⇒ 状态机 6 条 UPDATE 全部 42703 ⇒ 派发彻底死掉
//	    ⇒ 事后立了 outbox_freshinstall_column_closure_test.go（只盯 outbox 一个特性）
//	  · 2026-11-24：973 的 raw_model_name 列在生产上不存在 ⇒ 统计管线静默丢数；
//	    055–058 团队族四表从来没被任何通道建过
//	    ⇒ 文件在 sql/migrations/startup 里 ⇒ 由
//	      TestStartupMigrationsStayInEveryDeliveryChannel 盯着
//	  · 2026-11-26：820fcc4f0 让 API Key 鉴权路径（keystore_sync／verifier）与
//	    admin/keys 直接读写 api_keys.capabilities，而创建这一列的文件只在仓根
//	    散目录 migrations/ 里 ⇒ 文件不在上面两道门的搜索面里 ⇒ **没有任何门会红**
//
//	每次都没被测试拦住，是同一个原因：相关包的测试全绿——它们不跑在
//	「由投递迁移建出来的 schema」上。前两道门只盯各自的目录；本门按对象判，
//	不看定义件放在哪个目录。
//
// # 判据（细节与边界见包注释）
//
//	认可的创建来源五处：部署通道 files=()、安装器清单、认可目录（sql/schema、
//	installer/migrations）、生产 Go 字面量自带的 DDL、生产 Go //go:embed 的 SQL。
//	未投递对象 ＝ 其余任何 .sql 定义了、而上面五处都没创建的表与列。
//	引用 ＝ 生产 Go（非 _test.go，不含 tests/、scripts/）SQL 字面量对它的**绑定引用**
//	（限定名／INSERT 列清单／UPDATE 赋值目标／单表语句里的裸列）。
//
//	两头核账：
//	  · 没登记的引用 ⇒ 红（Result.References）；
//	  · 已知欠账台账（internal/schemadelivery/known_undelivered.go）里不再被命中的条目 ⇒ 红
//	    （Result.Stale）——问题修好了就要删登记，防免死金牌。
//
// # 恒绿护栏
//
//	通道文件 < 300、已投递对象 < 1000、候选 SQL < 500、或含 SQL 的生产 Go 文件 < 100
//	⇒ 判据面可疑地小 ⇒ 直接 Fatal（判据可能已失效 ⇒ 别按本门结论改代码）。
//	生产 Go 文件解析失败 ⇒ 逐个报出（不在判据面里 ⇒ 可能漏判）。
//
// # 门有牙的证据
//
//	internal/schemadelivery 的测试拿**真实文件**（keystore_sync.go／verifier.go／admin/keys.go
//	与上游的修法 db/api_key_capabilities.go）跑过：修法不在 ⇒ 恰好点到这三个文件；
//	只删掉那行 //go:embed ⇒ 重新变红。
//	admin 与 domains/authentication 另各有一道只报本包的绊线（包测试直接红；部署闸门按 diff 选包，
//	改了这两个包就会跑）。与仓里既有的「悬空引用门」（internal/relrefs）互补：那道只管表／视图，
//	且仓里有 DDL 就算数；本门管到列，且要求 DDL 在会被执行的来源里。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/schemadelivery"
)

// schemaDeliveryRepoRoot 定位仓根（scripts/ursmcheck → ../../ = 仓根）。
func schemaDeliveryRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("★ 定位仓根失败：%v", err)
	}
	return root
}

// TestProductionSQLDoesNotReferenceUndeliveredSchema —— 全仓：生产 SQL 不许引用
// 没有任何投递来源会创建的表与列；已知欠账逐条登记在案，两头核账。
func TestProductionSQLDoesNotReferenceUndeliveredSchema(t *testing.T) {
	root := schemaDeliveryRepoRoot(t)
	res, err := schemadelivery.Check(root)
	if err != nil {
		t.Fatalf("★ 判定失败（量具面，不是结论）：%v", err)
	}
	if res.ChannelFiles < 300 || len(res.Delivered) < 1000 || res.CandidateFiles < 500 || res.GoFiles < 100 {
		t.Fatalf("★ 判据面可疑地小（通道文件 %d／已投递对象 %d／候选 SQL %d／含 SQL 的生产 Go 文件 %d）\n"+
			"★ ⇒ 判据可能已失效（通道清单换了路径？扫描面被缩小？）⇒ ★ 别按本门结论改代码",
			res.ChannelFiles, len(res.Delivered), res.CandidateFiles, res.GoFiles)
	}
	for _, e := range res.ParseErrors {
		t.Errorf("★ 生产 Go 文件解析失败 ⇒ 不在判据面里（可能漏判）：%s", e)
	}
	for _, r := range res.References {
		t.Errorf("★ %s:%d 的 SQL 引用了未投递对象 %s（仓里只有这些没被投递的文件定义它：%s）\n"+
			"★ ⇒ 部署与新装都不会创建它 ⇒ 运行时 42703／42P01\n"+
			"★ ⇒ 出路二选一：① 把定义它的迁移搬进投递通道并登记；② 别在 SQL 里引用它\n"+
			"★ ⇒ 确属已知情况（死代码／有意的双形状／已登记的存量缺口）⇒ 在\n"+
			"★    internal/schemadelivery/known_undelivered.go 逐条登记并写明理由",
			r.File, r.Line, r.Object, strings.Join(res.Undelivered[r.Object], "、"))
	}
	for _, s := range res.Stale {
		t.Errorf("★ 已知欠账台账（internal/schemadelivery/known_undelivered.go）里这一条不再被命中：%s\n"+
			"★ ⇒ 问题修好了就要删登记，防免死金牌", s)
	}
	t.Logf("★ 判据面：通道文件 %d（点名但读不到 %d）／认可目录 %d／嵌入 %d／候选 SQL %d 件／已投递对象 %d／"+
		"未投递对象 %d／含 SQL 的生产 Go 文件 %d ⇒ 未登记的引用 %d 处、已知欠账 %d 处、失效登记 %d 条",
		res.ChannelFiles, res.MissingFiles, res.RecognizedFiles, res.EmbedFiles, res.CandidateFiles,
		len(res.Delivered), len(res.Undelivered), res.GoFiles, len(res.References), len(res.Known), len(res.Stale))
}
