package sqlguard

// 997/998/999（会话总结与任务项目关联系统，0f545826a）投递门。
//
// ★ 为什么需要这道门：三迁移落地时投递链九面全缺（12h 修订审计轮 F9 补齐），
// 但**台账自登记段**仍缺（62 增量审计轮 G-15 补齐）——up 不写
// public.schema_migrations ⇒ 任何通道应用后运行时台账无行 ⇒ 连续性门/
// 漂移报告假红、幂等重放无据可依。本文件把三迁移的 12 面钉死：
// canonical（守卫+自登记）/ down（回退+台账 DELETE）/ embeddata 副本×2 /
// installer go:embed×2 + map×2 / dbinit StartupFiles / tsv / docker 通道
// files=() + ledger_artifact_probe / 离线升级副本×2 —— 与 994/995/996 同构，
// 防止「补一次漏三面」的循环。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type migrationDeliverySpec struct {
	version      string // '997' / '998' / '999'
	upFile       string
	downFile     string
	mapGoVar     string // installer main.go 的 go:embed 变量名
	mapGoVarDown string
	tsvIndex     string // installed_startup_migrations.tsv 期望索引
}

var sessionSummaryDeliverySpecs = []migrationDeliverySpec{
	{
		version:      "997",
		upFile:       "997_tasks_dimension_table.sql",
		downFile:     "997_tasks_dimension_table.down.sql",
		mapGoVar:     "tasksDimensionTable997",
		mapGoVarDown: "tasksDimensionTable997Down",
		tsvIndex:     "388",
	},
	{
		version:      "998",
		upFile:       "998_session_summaries_task_association.sql",
		downFile:     "998_session_summaries_task_association.down.sql",
		mapGoVar:     "sessionSummariesTaskAssociation998",
		mapGoVarDown: "sessionSummariesTaskAssociation998Down",
		tsvIndex:     "389",
	},
	{
		version:      "999",
		upFile:       "999_session_summary_layers.sql",
		downFile:     "999_session_summary_layers.down.sql",
		mapGoVar:     "sessionSummaryLayers999",
		mapGoVarDown: "sessionSummaryLayers999Down",
		tsvIndex:     "390",
	},
}

func TestMigration997to999SessionSummaryDelivery(t *testing.T) {
	for _, spec := range sessionSummaryDeliverySpecs {
		t.Run(spec.version, func(t *testing.T) {
			up := filepath.Join("..", "..", "sql", "migrations", "startup", spec.upFile)
			body, err := os.ReadFile(up)
			if err != nil {
				t.Fatalf("读 %s 正本失败（投递面断裂）: %v", spec.version, err)
			}
			// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
			exec := sgStripSQLComments(string(body))
			rawUp := string(body)

			// ① 超时守卫（995/996 同口径：会话级 SET，docker 流式通道 SET LOCAL 静默失效）。
			for _, guard := range []string{
				"SET lock_timeout = '5s';",
				"SET statement_timeout = '10min';",
			} {
				if !strings.Contains(exec, guard) {
					t.Fatalf("%s 缺超时守卫: %s", spec.version, guard)
				}
			}
			// ② 台账自登记（≥952 双账本纪律 —— G-15 补面后钉死）。
			if !strings.Contains(exec, "INSERT INTO public.schema_migrations (version, description)") ||
				!strings.Contains(exec, "VALUES ('"+spec.version+"',") {
				t.Fatalf("%s 缺 schema_migrations 自登记段（应用后运行时台账无行）", spec.version)
			}
			if !strings.Contains(exec, "ON CONFLICT (version) DO UPDATE") {
				t.Fatalf("%s 自登记缺幂等 ON CONFLICT 守卫", spec.version)
			}

			// ③ down：只退自己 + 台账 DELETE 对偶。
			down := filepath.Join("..", "..", "sql", "migrations", "startup", spec.downFile)
			downBody, err := os.ReadFile(down)
			if err != nil {
				t.Fatalf("%s 缺 down 迁移: %v", spec.version, err)
			}
			downExec := sgStripSQLComments(string(downBody))
			rawDown := string(downBody)
			if !strings.Contains(downExec, "DELETE FROM public.schema_migrations WHERE version = '"+spec.version+"'") {
				t.Fatalf("%s down 缺台账 DELETE（与 up 自登记对偶）", spec.version)
			}

			// ④ embeddata 副本逐字一致（installer 会应用另一份内容 = 投递分裂）。
			for pair, want := range map[string]string{
				filepath.Join("embeddata", "startup", spec.upFile):   rawUp,
				filepath.Join("embeddata", "startup", spec.downFile): rawDown,
			} {
				cop, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", pair))
				if err != nil {
					t.Fatalf("读 %s embeddata 副本失败: %v", spec.version, err)
				}
				if string(cop) != want {
					t.Fatalf("%s 正本与 embeddata 副本不一致（%s）", spec.version, pair)
				}
			}

			// ⑤ installer go:embed + embeddedSQLFiles map 双登记。
			mainSrc, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
			if err != nil {
				t.Fatalf("读 installer main.go 失败: %v", err)
			}
			// ★ 剥整行 Go 注释（★ 保留 `//go:embed` 指令本身）。
			main := collapseSpaces(sgStripGoCommentsKeepEmbed(string(mainSrc)))
			for _, frag := range []string{
				"//go:embed embeddata/startup/" + spec.upFile,
				"//go:embed embeddata/startup/" + spec.downFile,
				`"startup/` + spec.upFile + `": ` + spec.mapGoVar + ",",
				`"startup/` + spec.downFile + `": ` + spec.mapGoVarDown + ",",
			} {
				if !strings.Contains(main, collapseSpaces(frag)) {
					t.Fatalf("installer main.go 缺 %s 登记: %s", spec.version, frag)
				}
			}

			// ⑥ dbinit StartupFiles（新装库跑该迁移）。
			runnerSrc, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
			if err != nil {
				t.Fatalf("读 dbinit runner.go 失败: %v", err)
			}
			if !strings.Contains(sgStripGoCommentsKeepEmbed(string(runnerSrc)), `"`+spec.upFile+`",`) {
				t.Fatalf("dbinit runner.go StartupFiles 缺 %s", spec.version)
			}

			// ⑦ tsv 台账（期望索引；由 startup_manifest_test 从 StartupFiles 派生，禁止手改）。
			tsvBody, err := os.ReadFile(filepath.Join("..", "..", "sql", "schema", "installed_startup_migrations.tsv"))
			if err != nil {
				t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
			}
			if !strings.Contains(sgStripShellComments(string(tsvBody)), spec.tsvIndex+"\t"+spec.upFile) {
				t.Fatalf("tsv 缺 %s 登记行（%s\\t%s）", spec.version, spec.tsvIndex, spec.upFile)
			}

			// ⑧ docker 通道：files 列表 + 产物探针。
			deploySrc, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
			if err != nil {
				t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
			}
			// ★ 剥整行 shell 注释（★ 被 `#` 注掉的登记不算登记）。
			deployText := sgStripShellComments(string(deploySrc))
			for _, frag := range []string{
				`"$ROOT_DIR/sql/migrations/startup/` + spec.upFile + `"`,
				`ledger_artifact_probe "` + spec.upFile + `"`,
			} {
				if !strings.Contains(deployText, frag) {
					t.Fatalf("apply-db-revision-sequence.sh 缺 %s 登记: %s（docker 通道会静默跳过该迁移）", spec.version, frag)
				}
			}

			// ⑨ 离线升级通道副本（installer/migrations/<name>/{up,down}，offline_apply.go 消费）。
			offlineUp, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations", strings.TrimSuffix(spec.upFile, ".sql"), "up.sql"))
			if err != nil {
				t.Fatalf("读 %s installer/migrations 副本失败: %v", spec.version, err)
			}
			if string(offlineUp) != rawUp {
				t.Fatalf("%s 正本与 installer/migrations 副本不一致", spec.version)
			}
			offlineDown, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations", strings.TrimSuffix(spec.upFile, ".sql"), "down.sql"))
			if err != nil {
				t.Fatalf("读 %s installer/migrations down 副本失败: %v", spec.version, err)
			}
			if string(offlineDown) != rawDown {
				t.Fatalf("%s down 正本与 installer/migrations 副本不一致", spec.version)
			}
		})
	}
}
