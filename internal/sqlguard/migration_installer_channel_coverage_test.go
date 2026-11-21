package sqlguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStartupMigrationsStayInEveryInstallerChannel —— 近邻窗口内的 startup
// 迁移必须在 **installer 侧三条投递通道**里都在场。
//
// # 背景（2026-11-21 211 生产取证，本轮真实撞到）
//
//	迁移 997 / 998 / 999（会话总结与任务项目关联系统）在 `0f545826a` 落地时：
//
//	· 只加了 **canonical 文件**（sql/migrations/startup/997_*.sql 等）
//	· 只登记了 **TSV 镜像**（sql/schema/installed_startup_migrations.tsv）
//	· ★ **没进** installer 的 embeddata 副本 / main.go 的 go:embed+map /
//	  dbinit 的 StartupFiles 登记表
//
//	⇒ 两条路线都不会执行它们：
//	  · installer 路线（全新安装）⇒ 不在 StartupFiles ⇒ 跳过
//	  · 211 的 seamless 部署路线 ⇒ 走 scripts/apply-db-revision-sequence.sh
//	    的 `files=(...)` 显式清单 ⇒ 不在清单里 ⇒ 跳过
//	⇒ ★ 211 生产实测（2026-11-21）：`public.schema_migrations` 停在 **995**、
//	  `public.tasks` 与 `public.session_summary_layers` **根本不存在**
//	  ⇒ 那笔 feat交付在生产上是**残缺的**（功能底座表没建）。
//
// ★ 为什么**既有的**门没抓住：
//
//	TestStartupMigrationsStayInEveryDeliveryChannel（同包）只管**一条**通道
//	（211 部署通道清单 files=(...)），**不管** installer 侧；
//	而六点位的门是**逐个迁移**写的（migration_971/972/977/978/980/990_991/
//	994/995_delivery_test.go）⇒ **新增迁移**若漏登记 installer 通道，
//	**没有任何门会红** ⇒ 必须补一条**全量**的 installer 通道门。
//
// # 判据设计（与同包那道门同源，两处刻意避开本仓反复踩到的坑）
//
//	① **锚点取自磁盘侧**（canonical 目录里的最大迁移编号），**不取自清单侧**
//	   —— 否则「删掉清单里的那一行」会让锚点下移、把那个缺口自己移出窗口。
//	② **只对近邻窗口断言**（复用同包 windowSize）—— 对全部历史文件断言会
//	   误报数百条（老文件未登记是设计内，历史上靠脚本里的手写白名单逐个补）。
//	   ★ 实测窗口 [980, 999] 内**零缺口** ⇒ 本门不需要任何豁免表：
//	   历史缺口全部落在窗口之外，而**新迁移一定在窗口内**。
//
// # 本门覆盖的六点位
//
//	① canonical sql/migrations/startup/<name>
//	② embeddata 副本   installer/cmd/llm-gw-installer/embeddata/startup/<name>（**字节一致**）
//	③ main.go embed    //go:embed embeddata/startup/<name>
//	④ main.go map      embeddedSQLFiles 的 "startup/<name>" 条目
//	⑤ StartupFiles     installer/internal/dbinit/runner.go 的登记表
//	⑥ TSV 台账         sql/schema/installed_startup_migrations.tsv
//
// ⇒ ★ 通用式：**「新增迁移」是一条需要多点位同步的契约**（文件 + 副本 +
//
//	两条 embed/map + 登记表 + 台账镜像）；只做其中一两处 ⇒ **每条通道都会
//	各自静默跳过它**，而功能面看起来「代码都写了」⇒ 生产上却是残缺的。
//	⇒ 因此需要**全量门**（而不是逐迁移的六点位单测）来守住契约。
const (
	migChannelEmbedDir = "../../installer/cmd/llm-gw-installer/embeddata/startup"
)

func TestStartupMigrationsStayInEveryInstallerChannel(t *testing.T) {
	// ── ① 磁盘侧编号迁移（锚点取磁盘侧）───────────────────────────────────
	entries, err := os.ReadDir(startupMigrationsDir)
	if err != nil {
		t.Fatalf("读迁移目录 %s：%v", startupMigrationsDir, err)
	}
	type mig struct {
		name string
		num  int
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		if !migrationNumPrefix.MatchString(e.Name()) {
			continue
		}
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			continue
		}
		migs = append(migs, mig{e.Name(), n})
	}
	if len(migs) == 0 {
		t.Fatalf("迁移目录里没有编号迁移：%s", startupMigrationsDir)
	}
	maxNum := -1
	for _, m := range migs {
		if m.num > maxNum {
			maxNum = m.num
		}
	}

	// ── ② 读三份通道文件 ──────────────────────────────────────────────────
	installerMain, err := os.ReadFile("../../installer/cmd/llm-gw-installer/main.go")
	if err != nil {
		t.Fatalf("读 installer main.go：%v", err)
	}
	runnerText, err := os.ReadFile("../../installer/internal/dbinit/runner.go")
	if err != nil {
		t.Fatalf("读 dbinit runner.go：%v", err)
	}
	tsvRaw, err := os.ReadFile("../../sql/schema/installed_startup_migrations.tsv")
	if err != nil {
		t.Fatalf("读 TSV 台账：%v", err)
	}
	mainText := string(installerMain)
	runner := string(runnerText)
	tsv := string(tsvRaw)

	// ── ③ 近邻窗口内逐个查六点位 ───────────────────────────────────────────
	found := 0
	for _, m := range migs {
		if m.num <= maxNum-windowSize || m.num > maxNum {
			continue
		}
		found++
		name := m.name

		// ① canonical：文件在磁盘上（migs 就是从它扫出来的 ⇒ 天然在位）
		canonical := filepath.Join(startupMigrationsDir, name)

		// ② embeddata 副本：存在 **且字节一致**
		embedCopy := filepath.Join(migChannelEmbedDir, name)
		canonBytes, err := os.ReadFile(canonical)
		if err != nil {
			t.Errorf("canonical 文件读不出：%s：%v", canonical, err)
			continue
		}
		copyBytes, err := os.ReadFile(embedCopy)
		if err != nil {
			t.Errorf("★ embeddata 副本缺失：%s（编号 %d，在近邻窗口 [%d, %d] 内）\n"+
				"后果：installer 路线（全新安装/升级）**不会投递这条迁移** ⇒ 新环境上它的表/列根本不存在，\n"+
				"而 TSV 台账却写着「已登记」⇒ 声明与产物脱节（2026-11-21 211 实测：997/998/999 即此形态）。\n"+
				"修法：把 canonical 文件**逐字节**复制到 %s（不得手改副本）。",
				embedCopy, m.num, maxNum-windowSize+1, maxNum, migChannelEmbedDir)
			continue
		}
		if !bytes.Equal(canonBytes, copyBytes) {
			t.Errorf("★ embeddata 副本与 canonical **字节不一致**：%s（编号 %d）\n"+
				"后果：embed 通道会静默投递**另一份内容**的迁移 ⇒ 台账与产物对不上。\n"+
				"修法：用 canonical 文件覆盖副本（cp），不要在副本上单独编辑。",
				name, m.num)
			continue
		}

		// ③ main.go 的 go:embed 指令
		if !strings.Contains(mainText, "//go:embed embeddata/startup/"+name) {
			t.Errorf("★ installer main.go 缺 go:embed 指令：embeddata/startup/%s（编号 %d）\n"+
				"后果：编译能过但 installer **打包不到这条迁移** ⇒ 该路线静默跳过。",
				name, m.num)
			continue
		}

		// ④ main.go 的 embeddedSQLFiles map 条目
		if !strings.Contains(mainText, "\"startup/"+name+"\"") {
			t.Errorf("★ installer main.go 的 embeddedSQLFiles map 缺条目：%q（编号 %d）\n"+
				"后果：文件被 embed 进来但**没有注册进投递表** ⇒ installer 不会执行它。",
				"startup/"+name, m.num)
			continue
		}

		// ⑤ dbinit runner 的 StartupFiles 登记表
		if !strings.Contains(runner, "\""+name+"\"") {
			t.Errorf("★ dbinit runner 的 StartupFiles 缺 %q（编号 %d）\n"+
				"后果：安装器**不会应用这条迁移** ⇒ 全新安装/升级的库里它的表/列不存在。\n"+
				"★ 注意这不是「记账断层」而是**功能缺失**：产物根本没建。",
				name, m.num)
			continue
		}

		// ⑥ TSV 台账镜像
		if !strings.Contains(tsv, name) {
			t.Errorf("★ TSV 台账缺 %q（编号 %d）\n"+
				"后果：installed_startup_migrations.tsv 是 StartupFiles 的机器可读镜像 ⇒ 两者脱节 ⇒\n"+
				"     「按台账判断有没有做」的人会踩空（镜像说有、真相源说无）。",
				name, m.num)
			continue
		}
	}
	if found == 0 {
		t.Fatalf("近邻窗口内没有编号迁移（maxNum=%d）—— 判据可能失效", maxNum)
	}
}
