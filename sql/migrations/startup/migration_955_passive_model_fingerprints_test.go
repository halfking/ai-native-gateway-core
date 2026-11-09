package startup

// migration_955_passive_model_fingerprints_test.go —— 迁移 955 的契约门。
//
// 955 建 passive_model_fingerprints（被动式模型指纹基线表）：从真实用户
// 流量聚合的 (凭据×模型) 行为基线，供 domains/modelcredibility.EvaluatePassive
// 做零成本、难规避的持续渗水检测（模型指纹验证增强轮 2026-11-09）。
//
// 五件（951 契约门同款）：
//
//	① 形态门：建表幂等 + 关键列（itt_distribution jsonb / sample_count /
//	  credential_id 默认 0）+ 唯一键 (tenant_id, credential_id, model_name)
//	  + lock guard。
//	② down 门：drop 同名表 + 删 schema_migrations 记账行。
//	③ 桶宽 cross-pin 门：迁移注释里钉的 ITT 桶宽口径必须与 Go 侧
//	  bucketizeITTs 的实现一致（10ms 桶宽 / "100+" 顶桶）—— 分布对比
//	  直接用这张表与 Go 侧产出的分布算 JSD，桶宽漂移 ⇒ 对比全部失真。
//	④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致。
//	⑤ 登记面门：runner.go / installer main.go / TSV 台账 / 修订序列脚本
//	  四处都提到 955（漏登 ⇒ 全新安装静默缺表）。
//
// 判据纪律：Go 侧读 SQL 用字符串字面量（同 951 门），判「提到关系」按
// 标识符精确匹配不做子串猜。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig955Name     = "955_passive_model_fingerprints.sql"
	mig955DownName = "955_passive_model_fingerprints.down.sql"
)

func mig955Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig955Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig955Code 去注释压平后的 SQL 正文（避免头注里的示例文本顶成断言命中）。
func mig955Code(t *testing.T, root, rel string) string {
	t.Helper()
	var codeLines []string
	for _, ln := range strings.Split(mig955Read(t, root, rel), "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		codeLines = append(codeLines, ln)
	}
	return regexp.MustCompile(`\s+`).ReplaceAllString(strings.Join(codeLines, "\n"), " ")
}

// ① 形态门：关键列 + 唯一键 + 幂等 + lock guard。
func TestMigration955PassiveFingerprintsShape(t *testing.T) {
	root := mig955Tree(t)
	code := mig955Code(t, root, filepath.Join("sql", "migrations", "startup", mig955Name))

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.passive_model_fingerprints (",
		"credential_id bigint NOT NULL DEFAULT 0,",
		"itt_distribution jsonb,",
		"sample_count integer NOT NULL DEFAULT 0,",
		"format_flags integer NOT NULL DEFAULT 0,",
		"CONSTRAINT passive_model_fingerprints_key UNIQUE (tenant_id, credential_id, model_name),",
		"CONSTRAINT passive_model_fingerprints_credential_check CHECK (credential_id >= 0),",
		"INSERT INTO public.schema_migrations (version, description) VALUES ('955',",
		"SET LOCAL lock_timeout = '5s';",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("955 形态漂移：缺 %q", want)
		}
	}
}

// ② down 门：drop 同名表 + 删记账行。
func TestMigration955DownDropsSameTable(t *testing.T) {
	root := mig955Tree(t)
	down := mig955Code(t, root, filepath.Join("sql", "migrations", "startup", mig955DownName))
	for _, want := range []string{
		"DROP TABLE IF EXISTS public.passive_model_fingerprints;",
		"DELETE FROM public.schema_migrations WHERE version = '955';",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("955.down 缺 %q，实际：%s", want, down)
		}
	}
}

// ③ 桶宽 cross-pin 门：迁移钉的 ITT 桶口径必须与 Go 侧 bucketizeITTs 一致。
func TestMigration955ITTBucketWidthCrossPin(t *testing.T) {
	root := mig955Tree(t)
	sql := mig955Read(t, root, filepath.Join("sql", "migrations", "startup", mig955Name))

	// 迁移侧：注释钉死「10ms 桶宽 + 桶标签 "0"/"10"/.../"90"/"100+"」。
	for _, want := range []string{
		"10ms 桶宽",
		`"0"/"10"/.../"90"/"100+"`,
		"bucketizeITTs",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("955 注释缺 ITT 桶口径 %q —— 桶宽是 Go/SQL 两侧的共享契约", want)
		}
	}

	// Go 侧：bucketizeITTs 仍是 10ms 桶宽（ms/10）+ 100+ 顶桶。
	goSrc := mig955Read(t, root, filepath.Join("domains", "modelcredibility", "passive.go"))
	if !strings.Contains(goSrc, "bucket := ms / 10") {
		t.Fatalf("bucketizeITTs 桶宽不再是 10ms（ms/10）—— 与 955 迁移钉的口径漂移，" +
			"JSD 对比会整体失真；改桶宽必须迁移注释与 Go 两侧同步")
	}
	if !strings.Contains(goSrc, `"100+"`) {
		t.Fatalf("bucketizeITTs 顶桶标签不再是 \"100+\" —— 与 955 迁移口径漂移")
	}
}

// ④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致。
func TestMigration955EmbeddataByteMatch(t *testing.T) {
	root := mig955Tree(t)
	for _, name := range []string{mig955Name, mig955DownName} {
		canon := mig955Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig955Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer",
			"embeddata", "startup", name))
		hc := sha256.Sum256([]byte(canon))
		he := sha256.Sum256([]byte(embed))
		if hex.EncodeToString(hc[:]) != hex.EncodeToString(he[:]) {
			t.Fatalf("%s canonical 与 embeddata 两份字节不一致（sha %s vs %s）——"+
				"embed 通道会静默投递另一份迁移", name,
				hex.EncodeToString(hc[:])[:12], hex.EncodeToString(he[:])[:12])
		}
	}
}

// ⑤ 登记面门：四处登记载体都提到 955。
func TestMigration955RegistrationCoverage(t *testing.T) {
	root := mig955Tree(t)
	for _, path := range []string{
		filepath.Join("installer", "internal", "dbinit", "runner.go"),
		filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"),
		filepath.Join("sql", "schema", "installed_startup_migrations.tsv"),
		filepath.Join("scripts", "apply-db-revision-sequence.sh"),
	} {
		if !strings.Contains(mig955Read(t, root, path), mig955Name) {
			t.Fatalf("955 未登记到 %s（漏登 ⇒ 全新安装静默缺表）", path)
		}
	}
}
