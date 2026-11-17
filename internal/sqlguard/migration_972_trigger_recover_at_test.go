package sqlguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是「迁移 972（model_offers_update_trigger 补 unavailable_recover_at）」
// 的投递六点齐备性 + **触发器不变式**门。
//
// # 背景（2026-11-17 F3 残留地平线根修轮）
//
//	owner 报障形态：节点状态恢复地平线（F3 残留）在 37~231 行之间**反复反弹**，
//	19 轮观测从未归零（详见 sql/migrations/startup/972_*.sql 的文件头）。
//
//	★ 根因：`model_offers` 是 INSTEAD OF UPDATE 触发器视图，触发器的 SET 清单是
//	  **手写的一串列名**。211 实测（pg_get_functiondef）清单里有 available /
//	  unavailable_reason / unavailable_at，**唯独没有 unavailable_recover_at**
//	  ⇒ 经视图的「置可用」写入只清两列、把恢复标记留下 ⇒ 行落进矛盾态
//	    available=TRUE ∧ recover_at≠NULL ⇒ 残留地平线反复反弹。
//	★ 仓内早已自述此事（credentialhealth/checker.go:469-473：「writing it here
//	  would be silently dropped」），但只绕了它没修它。
//
// # 本门钉住什么
//
//	① 972 的**六点位**逐点在位（canonical / embeddata 副本 / go:embed + var /
//	   embeddedSQLFiles map / dbinit StartupFiles / deploy 通道 files 数组 +
//	   ledger_artifact_probe / tsv 台账）；
//	② ★ **触发器不变式**（本门最核心）：触发器体里 unavailable_recover_at 的
//	   CASE 必须**以 available=TRUE 分支开头**——
//	   ★★ 这是隔离 PG 真跑腿（scripts/audit/f3-trigger-recover-at-isolated-leg.sh）
//	     抓到的真错：**「显式给值」在前**的写法（与两个兄弟列同款）在 INSTEAD OF
//	     UPDATE 触发器里**是错的**——NEW 行里未被 SET 触碰的列携带旧值 ⇒
//	     调用方不写 recover_at 时 NEW 仍是旧的未来时间戳 ⇒ 第一个分支命中 ⇒
//	     旧标记被保留 ⇒ 缺陷照旧（真跑实测 RESIDUE）。
//	   ⇒ 「contains unavailable_recover_at」这种弱判据**放走**过那个坏版本；
//	     本门必须钉**分支顺序**。
//	③ 触发器体**没有丢列**：两个兄弟列与既有列仍在（防止重写时误删）。
//	④ 正典快照 sql/objects/functions/model_offers_update_trigger.sql 与迁移同款
//	   （否则下一个照抄正典文件的人会把 bug 抄回去）。
//	⑤ deploy 通道的 clobber 守卫不会因为本件而中止部署（多文件重定义 ⇒ 必须
//	   登记进 intentional_function_chains）。
const (
	m972UpFile   = "972_model_offers_trigger_recover_at.sql"
	m972DownFile = "972_model_offers_trigger_recover_at.down.sql"

	m972CanonicalDir = "../../sql/migrations/startup"
	m972EmbedDir     = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	m972InstallerGo  = "../../installer/cmd/llm-gw-installer/main.go"
	m972DbinitRunner = "../../installer/internal/dbinit/runner.go"
	m972Tsv          = "../../sql/schema/installed_startup_migrations.tsv"

	// m972CanonicalFunc 是「正典函数快照」的路径（pg_dump 风格副本）。
	// ★ 它不是执行器输入，但它是**人**会照抄的那一份 ⇒ 必须与迁移同款。
	m972CanonicalFunc = "../../sql/objects/functions/model_offers_update_trigger.sql"

	// m972TriggerFunc 是 972 要修的触发器函数名。
	m972TriggerFunc = "model_offers_update_trigger"
)

// m972RecoverAtClauseOK 是**唯一被认可的** unavailable_recover_at 分支顺序：
// available=TRUE 分支必须在最前。
const m972RecoverAtClauseOK = `unavailable_recover_at = CASE
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            ELSE credential_model_bindings.unavailable_recover_at
        END`

// m972RecoverAtClauseBroken 是隔离 PG 真跑腿实测**无效**的那一版
// （「显式给值」在前 ⇒ 旧标记被保留 ⇒ 缺陷照旧）。
// ★ 本门显式断言它**不在**迁移里 ⇒ 防止有人「为了和兄弟列同款」把它改回去。
const m972RecoverAtClauseBroken = `unavailable_recover_at = CASE
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_recover_at
        END`

// TestM972DeliverySixPointsAreInPlace —— ① 六点位逐点在位。
func TestM972DeliverySixPointsAreInPlace(t *testing.T) {
	// ── canonical 正典件在位 ───────────────────────────────────────────────
	upPath := filepath.Join(m972CanonicalDir, m972UpFile)
	upRaw, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("读 canonical %s：%v", m972UpFile, err)
	}
	if _, err := os.Stat(filepath.Join(m972CanonicalDir, m972DownFile)); err != nil {
		t.Errorf("canonical down 件 %s 不存在：%v", m972DownFile, err)
	}

	// ── embeddata 副本逐字节相同 ───────────────────────────────────────────
	for _, name := range []string{m972UpFile, m972DownFile} {
		embed, err := os.ReadFile(filepath.Join(m972EmbedDir, name))
		if err != nil {
			t.Errorf("embeddata 副本 %s 不存在：%v", name, err)
			continue
		}
		src, err := os.ReadFile(filepath.Join(m972CanonicalDir, name))
		if err != nil {
			t.Errorf("读 canonical %s：%v", name, err)
			continue
		}
		if string(embed) != string(src) {
			t.Errorf("embeddata 漂移 %s：embeddata 与 sql/migrations/startup 逐字节不一致"+
				"（installer 会带着非正典 SQL 上门）", name)
		}
	}

	// ── installer main.go：go:embed ×2 + embeddedSQLFiles map ×2 ─────────
	installerRaw, err := os.ReadFile(m972InstallerGo)
	if err != nil {
		t.Fatalf("读 %s：%v", m972InstallerGo, err)
	}
	installerText := string(installerRaw)
	for _, name := range []string{m972UpFile, m972DownFile} {
		if !strings.Contains(installerText, "//go:embed embeddata/startup/"+name) {
			t.Errorf("installer main.go 缺 go:embed 指令：embeddata/startup/%s", name)
		}
		if !strings.Contains(installerText, `"startup/`+name+`"`) {
			t.Errorf("installer main.go 的 embeddedSQLFiles map 缺条目：%q", "startup/"+name)
		}
	}

	// ── dbinit runner 的 StartupFiles ─────────────────────────────────────
	runnerRaw, err := os.ReadFile(m972DbinitRunner)
	if err != nil {
		t.Fatalf("读 %s：%v", m972DbinitRunner, err)
	}
	if !strings.Contains(string(runnerRaw), `"`+m972UpFile+`"`) {
		t.Errorf("dbinit runner 的 StartupFiles 缺 %q ⇒ 全新安装/升级不会应用这条迁移", m972UpFile)
	}

	// ── deploy 通道 files=(...) 数组 + ledger_artifact_probe ───────────────
	seqRaw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	seqText := string(seqRaw)
	arrBlock := revisionSequenceArrayBlock(t, seqText)
	if !revisionSequenceEntryRe.MatchString(arrBlock) ||
		!strings.Contains(arrBlock, m972UpFile) {
		t.Errorf("deploy 通道 files=(...) 数组缺 %q ⇒ 211 的 seamless 部署通道不会应用这条迁移",
			m972UpFile)
	}
	if !strings.Contains(seqText, `ledger_artifact_probe "`+m972UpFile+`"`) {
		t.Errorf("deploy 通道缺 ledger_artifact_probe %q ⇒ 「台账说已应用但产物不在场」"+
			"这一形态无人对账", m972UpFile)
	}

	// ── tsv 台账行 ─────────────────────────────────────────────────────────
	tsvRaw, err := os.ReadFile(m972Tsv)
	if err != nil {
		t.Fatalf("读 %s：%v", m972Tsv, err)
	}
	if !strings.Contains(string(tsvRaw), m972UpFile) {
		t.Errorf("台账 %s 缺 %q 的行 ⇒ 「账本说没做」会让后续按账本判断的人踩空",
			m972Tsv, m972UpFile)
	}

	// ── 迁移体本身必须真的改了触发器 ─────────────────────────────────────
	upText := string(upRaw)
	if !strings.Contains(upText, "CREATE OR REPLACE FUNCTION public."+m972TriggerFunc+"()") {
		t.Errorf("迁移体里没有 CREATE OR REPLACE FUNCTION public.%s() ⇒ 它没修触发器",
			m972TriggerFunc)
	}
}

// TestM972TriggerRecoversAtClauseOrderIsPinned —— ②③ 触发器不变式门（本门核心）。
//
// ★ 本门是本轮**最贵的一条**：隔离 PG 真跑腿证明「显式给值在前」的写法无效，
//
//	而弱判据（只查 contains unavailable_recover_at）放走过那个坏版本。
func TestM972TriggerRecoversAtClauseOrderIsPinned(t *testing.T) {
	upRaw, err := os.ReadFile(filepath.Join(m972CanonicalDir, m972UpFile))
	if err != nil {
		t.Fatalf("读迁移体：%v", err)
	}
	upText := string(upRaw)

	if !strings.Contains(upText, m972RecoverAtClauseOK) {
		t.Errorf("迁移体里 unavailable_recover_at 的 CASE 分支顺序不对。\n"+
			"必须是「available=TRUE 分支在前」——INSTEAD OF UPDATE 的 NEW 行里未被 SET\n"+
			"触碰的列携带旧值 ⇒ 「显式给值」在前会把旧标记原样保留（隔离 PG 真跑腿\n"+
			"实测 RESIDUE，缺陷照旧）。\n期望片段：\n%s", m972RecoverAtClauseOK)
	}
	if strings.Contains(upText, m972RecoverAtClauseBroken) {
		t.Errorf("迁移体里出现了**已被真跑证伪**的分支顺序（显式给值在前）——\n"+
			"那一版会让经视图的「置可用」保留旧的恢复标记 ⇒ F3 残留地平线照旧反弹。\n"+
			"禁止片段：\n%s", m972RecoverAtClauseBroken)
	}

	// ★ 触发器体没有丢列：两个兄弟列与既有列必须在。
	for _, want := range []string{
		"unavailable_reason = CASE",
		"unavailable_at = CASE",
		"admin_protected = CASE",
		"priority = COALESCE(NEW.priority, credential_model_bindings.priority)",
		"context_window_override = COALESCE(NEW.context_window_override, credential_model_bindings.context_window_override)",
	} {
		if !strings.Contains(upText, want) {
			t.Errorf("迁移体里的触发器体缺 %q —— 重写函数体时误删了既有列（静默丢列）", want)
		}
	}
}

// TestM972CanonicalFunctionSnapshotMatchesMigration —— ④ 正典快照与迁移同款。
func TestM972CanonicalFunctionSnapshotMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile(m972CanonicalFunc)
	if err != nil {
		t.Fatalf("读正典函数快照 %s：%v", m972CanonicalFunc, err)
	}
	text := string(raw)
	if !strings.Contains(text, m972RecoverAtClauseOK) {
		t.Errorf("正典函数快照 %s 里的 unavailable_recover_at 分支顺序不对（或缺该列）——\n"+
			"它是**人**会照抄的那一份 ⇒ 抄回去就是复发。\n期望片段：\n%s",
			m972CanonicalFunc, m972RecoverAtClauseOK)
	}
	if strings.Contains(text, m972RecoverAtClauseBroken) {
		t.Errorf("正典函数快照 %s 里出现了已被真跑证伪的分支顺序（显式给值在前）", m972CanonicalFunc)
	}
}

// TestM972DeployChannelClobberGuardWontTrip —— ⑤ clobber 守卫不会因本件中止部署。
//
// ★ deploy 通道的脚本守卫：若 files=(...) 数组里有**多个**文件重定义同一函数，
//
//	必须在 intentional_function_chains 里登记链序，否则部署在应用任何文件之前
//	abort（exit 5）。本件是唯一重定义者 ⇒ 不需要登记；一旦将来有第二个重定义者
//	（例如有人再改一次触发器），本门会要求补登记。
func TestM972DeployChannelClobberGuardWontTrip(t *testing.T) {
	seqRaw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	seqText := string(seqRaw)

	// 只取 files=(...) 数组里的条目（守卫也只扫这个数组）。
	arrBlock := revisionSequenceArrayBlock(t, seqText)
	definers := 0
	for _, m := range revisionSequenceEntryRe.FindAllStringSubmatch(arrBlock, -1) {
		// ★ revisionSequenceEntryRe 的捕获组 1 是**文件名**（不含目录）⇒
		//   读盘时要把 startup/ 目录补回去。
		body, err := os.ReadFile(filepath.Join(m972CanonicalDir, m[1]))
		if err != nil {
			// 数组里有些条目的正典件不在 sql/migrations/startup/（通道自己会在
			// 运行时报 missing migration）⇒ 与本门无关，跳过。
			continue
		}
		if strings.Contains(string(body),
			"CREATE OR REPLACE FUNCTION public."+m972TriggerFunc+"()") {
			definers++
		}
	}
	if definers == 0 {
		t.Fatalf("deploy 通道数组里没有文件定义 %s —— 972 漏登记了？", m972TriggerFunc)
	}
	if definers > 1 {
		if !strings.Contains(seqText, "'"+m972TriggerFunc+"|") {
			t.Errorf("deploy 通道数组里有 %d 个文件重定义 %s，但 intentional_function_chains "+
				"里没有登记链序 ⇒ 部署会在应用任何文件之前 abort（exit 5）。",
				definers, m972TriggerFunc)
		}
	}
}
