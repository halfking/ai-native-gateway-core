package db

// modality_source_writers_guard_test.go —— 825 守卫闭环的静态钉子（R42）。
//
// 迁移 825 定义了 modality 的分层出处（inferred/structural/semantic/manual），
// 其中 semantic/manual 是权威值：规则侧写方不得覆写。825 的配套 Go 改动只
// 给 discovery 的三处 upsert 加了豁免，规则侧还有两个漏网写点 + 两个不盖章
// 的 manual 写点（R42 审计定案）：
//
//   - bg/taxonomy_sync.go upsertCanonical：6h tick 无条件覆写 modality
//   - db/db.go ensureAudioModalityBackfill：每次网关启动把 text 翻回 audio
//   - admin/model_modality.go 与 admin/credential_models_dto.go：Layer 3
//     手工覆盖从不写 modality_source='manual'，manual 臂空转
//
// 本门用文本钉子（非行为测试）钉住四个文件的 SQL 字面量形态。文本钉子
// 的红不能证明语义正确，但足以抓住「有人把豁免/盖章当冗余删掉」这一最
// 可能的回退形态——与 repair_two_surface_test 同一取舍。
//
// 若红在这里：先确认删改是有意为之（例如 825 分层被重新设计），同步更新
// 本文件与迁移 825 的注释，再动生产写点。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGuardedFile(t *testing.T, rel string) string {
	t.Helper()
	// 测试进程的 cwd 是包目录（db/），rel 以包目录为基准。
	body, err := os.ReadFile(filepath.Join(".", rel))
	if err != nil {
		// go test 允许在任意子目录运行，回退到相对包目录再试一次。
		body, err = os.ReadFile(filepath.Join("db", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
	}
	return string(body)
}

func TestRuleWritersExemptSemanticManualModality(t *testing.T) {
	t.Run("taxonomy_upsert_conflict_arm", func(t *testing.T) {
		src := readGuardedFile(t, filepath.Join("..", "bg", "taxonomy_sync.go"))
		i := strings.Index(src, "func (t *TaxonomySync) upsertCanonical")
		if i < 0 {
			t.Fatal("upsertCanonical 不在 bg/taxonomy_sync.go —— 门守的代码被移动/改名，需同步本门")
		}
		body := src[i:]
		if !strings.Contains(body, "IN ('semantic', 'manual')") {
			t.Error("upsertCanonical 冲突臂缺少 semantic/manual 豁免 —— " +
				"taxonomy YAML 是规则侧写方（Layer 1），无豁免时语义降级活不过 6h tick（825 互斥写）")
		}
		if !strings.Contains(body, "'inferred'") {
			t.Error("upsertCanonical 插臂未盖 'inferred' —— 规则播种不盖章会让 825 的存量口径与新增行不可区分")
		}
	})
	t.Run("ensure_audio_backfill_both_updates", func(t *testing.T) {
		src := readGuardedFile(t, "db.go")
		i := strings.Index(src, "func (db *DB) ensureAudioModalityBackfill")
		if i < 0 {
			t.Fatal("ensureAudioModalityBackfill 不在 db/db.go —— 门守的代码被移动/改名，需同步本门")
		}
		body := src[i:]
		// 两条 UPDATE（text 臂与 vision 白名单臂）各自都要有豁免。
		if got := strings.Count(body, "NOT IN ('semantic', 'manual')"); got < 2 {
			t.Errorf("ensureAudioModalityBackfill 期望两条 UPDATE 各带一次 semantic/manual 豁免，实际 %d 次"+
				"—— 少于 2 意味着某条 UPDATE 会把语义降级在下次重启翻回（825 互斥写）", got)
		}
	})
}

func TestManualOverridesStampModalitySource(t *testing.T) {
	for _, tc := range []struct {
		rel  string
		mark string
	}{
		{filepath.Join("..", "admin", "model_modality.go"), "modality_source = 'manual'"},
		{filepath.Join("..", "admin", "credential_models_dto.go"), "modality_source = 'manual'"},
	} {
		src := readGuardedFile(t, tc.rel)
		if !strings.Contains(src, tc.mark) {
			t.Errorf("%s 缺少 %s —— manual 是 825 定义的权威层，不盖章则守卫的 manual 臂空转、"+
				"手工覆盖可被规则推断翻回", tc.rel, tc.mark)
		}
	}
}
