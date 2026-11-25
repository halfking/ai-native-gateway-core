package schemadelivery

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------- 小工具 ----------

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readRepo 读本仓的真实文件，给「真实文件咬合」臂用；仓不在时跳过那一臂，不伪装成通过。
func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Skipf("仓内文件读不到（%v）：本臂要拿真实文件咬合，单独跑本包时跳过", err)
	}
	return string(b)
}

func check(t *testing.T, root string) *Result {
	t.Helper()
	res, err := Check(root)
	if err != nil {
		t.Fatalf("Check 失败：%v", err)
	}
	return res
}

func refKeys(rs []Reference) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.File+":"+strconv.Itoa(r.Line)+" "+r.Object.String())
	}
	return out
}

func assertKeys(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s：\n  实得 %v\n  期望 %v", label, got, want)
	}
}

func lineOf(src, needle string) int {
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	panic("lineOf：源码里没有 " + needle)
}

func objKeys(d *sqlDefs) []string {
	out := make([]string, 0, len(d.objects))
	for o := range d.objects {
		out = append(out, o.String())
	}
	sort.Strings(out)
	return out
}

// ---------- 定义侧：一条 SQL 里读出哪些对象 ----------

func TestParseSQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"单列", "ALTER TABLE public.api_keys ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;",
			[]string{"api_keys.capabilities"}},
		{"一条语句连加三列（852 的形态）", "ALTER TABLE IF EXISTS public.models_canonical\n    ADD COLUMN IF NOT EXISTS context_window_override integer,\n    ADD COLUMN IF NOT EXISTS context_window_source text DEFAULT 'catalog',\n    ADD COLUMN IF NOT EXISTS context_window_updated_at timestamp with time zone;",
			[]string{"models_canonical.context_window_override", "models_canonical.context_window_source", "models_canonical.context_window_updated_at"}},
		{"先改列再加列（527 的形态）", "ALTER TABLE handoff_pending_confirmations\n    ALTER COLUMN status TYPE VARCHAR(32),\n    ADD COLUMN IF NOT EXISTS goal_state JSONB,\n    ADD COLUMN IF NOT EXISTS restored_at TIMESTAMPTZ;",
			[]string{"handoff_pending_confirmations.goal_state", "handoff_pending_confirmations.restored_at"}},
		{"DO 块的 EXECUTE 字符串里：没有结尾分号的那条之后还有别的 ALTER", "DO $$\nBEGIN\n    EXECUTE 'ALTER TABLE public.t1 ADD COLUMN IF NOT EXISTS a int';\n    EXECUTE 'ALTER TABLE public.t2 ADD COLUMN IF NOT EXISTS b int, ADD COLUMN IF NOT EXISTS c int';\nEND $$;\nALTER TABLE public.t3 ADD COLUMN d int;",
			[]string{"t1.a", "t2.b", "t2.c", "t3.d"}},
		{"DO 块体内直接写的 ALTER，后面才是块的收尾 $$", "DO $$\nBEGIN\n    ALTER TABLE public.t4 ADD COLUMN e int;\n    ALTER TABLE public.t5 ADD COLUMN f int;\nEND $$;",
			[]string{"t4.e", "t5.f"}},
		{"字符串字面量里的 ADD 不算", "ALTER TABLE t ADD COLUMN label text DEFAULT 'ADD COLUMN fake int';",
			[]string{"t.label"}},
		{"加的是约束／主键／唯一键，不是列", "ALTER TABLE t ADD CONSTRAINT pk PRIMARY KEY (id), ADD UNIQUE (x), ADD CONSTRAINT c CHECK (y > 0);", nil},
		{"建表：列、带默认值的列、表级约束", "CREATE TABLE IF NOT EXISTS public.teams_x (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    tenant_id text NOT NULL,\n    name text DEFAULT 'a,b(c)',\n    meta jsonb DEFAULT '{}'::jsonb,\n    \"Quoted\" int,\n    CONSTRAINT teams_x_pkey UNIQUE (id, tenant_id),\n    CHECK ((length(name) > 0))\n);",
			[]string{"teams_x", "teams_x.id", "teams_x.meta", "teams_x.name", "teams_x.quoted", "teams_x.tenant_id"}},
		{"分区子表只记表名", "CREATE TABLE public.request_logs_2026_11 PARTITION OF public.request_logs FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');",
			[]string{"request_logs_2026_11"}},
		{"改名：新列名算已创建", "ALTER TABLE t RENAME COLUMN a TO b;\nALTER TABLE t RENAME c TO d;",
			[]string{"t.b", "t.d"}},
		{"删列不认（已登记的边界）", "ALTER TABLE t DROP COLUMN x;", nil},
		{"注释里的 DDL 不算", "-- ALTER TABLE t ADD COLUMN fake int\nALTER TABLE t ADD COLUMN real_col int;", []string{"t.real_col"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertKeys(t, "对象", objKeys(parseSQL("x.sql", tc.sql)), tc.want)
		})
	}
}

func TestParseSQL_RenamesAndPartitions(t *testing.T) {
	d := parseSQL("x.sql", "ALTER TABLE IF EXISTS old_keys RENAME TO api_keys;\nCREATE TABLE public.logs_2026 PARTITION OF public.logs FOR VALUES FROM (1) TO (2);\nCREATE OR REPLACE VIEW public.v_offers AS SELECT 1;\nALTER TABLE only_touched_here ADD COLUMN c int;")
	if !reflect.DeepEqual(d.renamedTo, map[string]string{"api_keys": "old_keys"}) {
		t.Errorf("表改名：实得 %v", d.renamedTo)
	}
	if !reflect.DeepEqual(d.partitions, map[string]string{"logs_2026": "logs"}) {
		t.Errorf("分区：实得 %v", d.partitions)
	}
	if !d.views["v_offers"] {
		t.Errorf("视图没记下：%v", d.views)
	}
	for _, tb := range []string{"old_keys", "api_keys", "logs", "logs_2026", "v_offers", "only_touched_here"} {
		if !d.tables[tb] {
			t.Errorf("DDL 提到的表 %s 没记下", tb)
		}
	}
}

func TestMergeDelivered(t *testing.T) {
	base := &sqlDefs{objects: map[Object]string{}}
	base.add(Object{"api_keys", ""}, "a.sql")
	base.add(Object{"api_keys", "capabilities"}, "a.sql")
	base.add(Object{"request_logs", ""}, "a.sql")
	base.add(Object{"request_logs", "id"}, "a.sql")
	renamed := &sqlDefs{objects: map[Object]string{}, renamedTo: map[string]string{"api_keys_v2": "api_keys"}}
	part := &sqlDefs{objects: map[Object]string{}, partitions: map[string]string{"request_logs_2026_11": "request_logs"}}
	merged := mergeDelivered([]*sqlDefs{base, renamed, part})
	for _, o := range []Object{{"api_keys_v2", ""}, {"api_keys_v2", "capabilities"}, {"request_logs_2026_11", "id"}} {
		if merged[o] == "" {
			t.Errorf("改名／分区没把列派生过去：%s", o)
		}
	}
	// 改名只增不删；自改名不成环。
	self := &sqlDefs{objects: map[Object]string{}, renamedTo: map[string]string{"x": "x"}}
	if got := mergeDelivered([]*sqlDefs{base, self}); got[Object{"api_keys", "capabilities"}] == "" {
		t.Error("合并把原有对象丢了")
	}
}

// ---------- 词法 ----------

func TestTokenize(t *testing.T) {
	toks := tokenize(`SELECT 'a, b', x::text, $1, $$ ( $$, $tag$ ) $tag$, "Q q", a.b FROM t`)
	type step struct {
		id string
		ch byte
	}
	want := []step{
		{"select", 0}, {"", ','}, {"x", 0}, {"", ':'}, {"text", 0}, {"", ','}, {"", '$'}, {"", ','},
		{"", ','}, {"", ','}, {"q q", 0}, {"", ','}, {"a", 0}, {"", '.'}, {"b", 0}, {"from", 0}, {"t", 0},
	}
	var got []step
	for _, tk := range toks {
		got = append(got, step{tk.id, tk.ch})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("词法单元：\n  实得 %v\n  期望 %v", got, want)
	}
}

// ---------- 引用侧：什么叫「引用」 ----------

// testIndex 的未投递对象取自真实树上出过的误报与真命中，逐条钉住绑定的口径。
func testIndex() *undeliveredIndex {
	u := &undeliveredIndex{tables: map[string]bool{}, cols: map[string]map[string]bool{}, known: map[string]bool{}}
	for _, tb := range []string{
		"api_keys", "credentials", "providers", "session_titles", "titles", "sensitive_words", "categories",
		"sessions", "session_analysis_summaries", "session_audit_records", "injection_attack_vectors", "request_logs",
	} {
		u.known[tb] = true
	}
	for _, o := range []Object{
		{"api_keys", "capabilities"}, {"credentials", "total_requests"}, {"credentials", "enabled"},
		{"credentials", "date"}, {"credentials", "count"}, {"sessions", "payload"},
	} {
		if u.cols[o.Column] == nil {
			u.cols[o.Column] = map[string]bool{}
		}
		u.cols[o.Column][o.Table] = true
	}
	for _, tb := range []string{"titles", "sensitive_words", "categories", "_request_logs_hot"} {
		u.tables[tb] = true
		u.known[tb] = true
	}
	return u
}

func TestReferencesIn_Binding(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		// —— 算引用的四种绑定 ——
		{"别名限定", "SELECT ak.id FROM public.api_keys ak WHERE COALESCE(ak.capabilities, '[]'::jsonb) <> '[]'::jsonb", []string{"api_keys.capabilities"}},
		{"schema 限定", "SELECT public.api_keys.capabilities FROM public.api_keys", []string{"api_keys.capabilities"}},
		{"表名限定", "UPDATE api_keys SET x = api_keys.capabilities", []string{"api_keys.capabilities"}},
		{"整段只提到一张表 ⇒ 裸列属于它", "SELECT capabilities FROM api_keys WHERE key_hash = $1", []string{"api_keys.capabilities"}},
		{"INSERT 列清单", "INSERT INTO api_keys (key_hash, capabilities) VALUES ($1, $2)", []string{"api_keys.capabilities"}},
		{"INSERT 列清单（schema 限定 ＋ AS 别名）", "INSERT INTO public.api_keys AS ak (key_hash, capabilities) VALUES ($1, $2)", []string{"api_keys.capabilities"}},
		{"ON CONFLICT DO UPDATE 的赋值目标", "INSERT INTO api_keys (key_hash) VALUES ($1) ON CONFLICT (key_hash) DO UPDATE SET capabilities = EXCLUDED.capabilities", []string{"api_keys.capabilities"}},
		{"UPDATE 的赋值目标", "UPDATE api_keys SET capabilities = $1 WHERE id = $2", []string{"api_keys.capabilities"}},
		{"UPDATE 带别名、括号组赋值", "UPDATE api_keys AS ak SET (capabilities, x) = ($1, $2) FROM providers p WHERE ak.id = p.id", []string{"api_keys.capabilities"}},
		{"UPDATE ONLY ＋ schema 限定", "UPDATE ONLY public.api_keys SET capabilities = $2 WHERE key_hash = $1", []string{"api_keys.capabilities"}},
		{"未投递的表：FROM", "SELECT t.id FROM titles t WHERE t.id = $1", []string{"titles"}},
		{"未投递的表：FROM 列表的第二项", "SELECT * FROM request_logs r, titles t WHERE r.id = t.rid", []string{"titles"}},
		{"未投递的表：INSERT／UPDATE", "INSERT INTO titles (id) VALUES ($1);\nUPDATE titles SET x = 1", []string{"titles"}},
		{"未投递的表：字面量开头的 FROM 片段", "titles WHERE id = $1", []string{"titles"}},
		{"SQL 注释不添引用，但语句本身照算", "SELECT id\n-- titles and sensitive_words retired\nFROM api_keys WHERE capabilities = '[]'::jsonb", []string{"api_keys.capabilities"}},
		// —— 不算引用（真实树上出过的误报形态）——
		{"输出别名", "SELECT c.id, 0::bigint AS total_requests FROM credentials c", nil},
		{"别表的列（经别名绑定到另一张表）", "SELECT c.id, p.enabled FROM credentials c JOIN providers p ON c.provider_id = p.id", nil},
		{"裸列名恰好是一张未投递的表名", "SELECT id, session_id, sensitive_words, status FROM session_audit_records WHERE id = $1", nil},
		{"括号里的裸列名恰好是表名", "SELECT COALESCE(categories::text[], '{}'), severity FROM injection_attack_vectors WHERE tenant_id = $1", nil},
		{"别名指向另一张表", "SELECT s.id, sam.payload FROM public.sessions s LEFT JOIN session_analysis_summaries sam ON sam.session_id = s.id", nil},
		{"别名声明处用的就是一张同名表的名字", "SELECT st.task_id FROM request_logs rl LEFT JOIN session_titles titles ON titles.task_id = rl.gw_task_id", nil},
		{"只出现在 SQL 注释里的表名", "SELECT count(*) FROM request_logs\n-- 2026-10-20（迁移 890）：v1 族（_request_logs / _request_logs_hot）整体退役", nil},
		{"名字只出现在字符串字面量里", "SELECT count(*) FROM information_schema.columns WHERE table_name = 'api_keys' AND column_name = 'capabilities'", nil},
		{"CTE 遮蔽了同名表", "WITH titles AS (SELECT id FROM request_logs WHERE id = $1) SELECT t.id FROM titles t", nil},
		{"函数名与类型名", "SELECT created_at::date, count(*) FROM credentials c", nil},
		{"另一张表的 INSERT 列清单", "INSERT INTO providers (name, capabilities) VALUES ($1, $2)", nil},
		{"另一张表的 UPDATE 目标", "UPDATE providers SET total_requests = $1 WHERE id = $2", nil},
		{"NEW／OLD／EXCLUDED 这类伪名限定", "UPDATE credentials SET x = NEW.total_requests WHERE id = (SELECT y FROM EXCLUDED)", nil},
		// —— 已登记的漏报形态：多表语句里的裸列、别名声明在另一个字面量里 ——
		{"（漏报）多表语句里的裸列", "SELECT total_requests FROM credentials c JOIN providers p ON p.id = c.provider_id", nil},
		{"（漏报）别名声明不在这段 SQL 里", "ak.capabilities <> '[]'::jsonb", nil},
	}
	u := testIndex()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, o := range u.referencesIn(tc.sql) {
				got = append(got, o.String())
			}
			assertKeys(t, "引用", got, tc.want)
		})
	}
}

// ---------- 合成仓：五处认可来源、候选来源、台账 ----------

const baseSQL = `CREATE TABLE public.api_keys (
    id bigserial PRIMARY KEY,
    key_hash text NOT NULL UNIQUE,
    team_id bigint
);
CREATE TABLE public.tenants (id text PRIMARY KEY);
`

// looseCaps 是 820fcc4f0 的形态：加列件躺在仓根的散目录里。
const looseCaps = `-- 散目录里的加列件
ALTER TABLE public.api_keys ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;
CREATE INDEX IF NOT EXISTS idx_api_keys_capabilities ON public.api_keys USING GIN (capabilities);
`

// codeRefs 是生产代码的引用形态：一处别名限定读、一处 INSERT 列清单写。
const codeRefs = "package pkg\n" +
	"\n" +
	"import \"context\"\n" +
	"\n" +
	"func load(ctx context.Context, q Queryer, hash string) error {\n" +
	"\treturn q.QueryRow(ctx, `SELECT COALESCE(ak.capabilities, '[]'::jsonb) FROM public.api_keys ak WHERE ak.key_hash = $1`, hash)\n" +
	"}\n" +
	"\n" +
	"func save(ctx context.Context, q Queryer, hash string, caps []byte) error {\n" +
	"\t_, err := q.Exec(ctx, `INSERT INTO api_keys (key_hash, capabilities) VALUES ($1, $2)`, hash, caps)\n" +
	"\treturn err\n" +
	"}\n"

const (
	looseRel = "migrations/20261125000001_api_keys_capabilities.sql"
	embedGo  = "db/api_key_capabilities.go"
	embedSQL = "db/migrations/20261125000001_api_keys_capabilities.sql"
)

var capsObject = Object{"api_keys", "capabilities"}

var bothRefs = []string{
	"pkg/a.go:" + strconv.Itoa(lineOf(codeRefs, "SELECT COALESCE")) + " api_keys.capabilities",
	"pkg/a.go:" + strconv.Itoa(lineOf(codeRefs, "INSERT INTO")) + " api_keys.capabilities",
}

// newRoot 造一个最小仓：两条通道各点名一个基线件，外加一处生产引用。
func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "sql/migrations/startup/0010_base.sql", baseSQL)
	write(t, root, InstallerEmbedDir+"/0010_base.sql", baseSQL)
	write(t, root, InstallerManifest, "# 合成清单\n0\t0010_base.sql\n")
	write(t, root, DeployChannelScript, "files=(\n  \"$ROOT_DIR/sql/migrations/startup/0010_base.sql\"\n)\n")
	write(t, root, "pkg/a.go", codeRefs)
	return root
}

func TestCheck_LooseFileReferencedByProductionSQL(t *testing.T) {
	root := newRoot(t)
	write(t, root, looseRel, looseCaps)
	res := check(t, root)
	assertKeys(t, "References", refKeys(res.References), bothRefs)
	if got := res.Undelivered[capsObject]; !reflect.DeepEqual(got, []string{looseRel}) {
		t.Errorf("Undelivered 的来源：实得 %v", got)
	}
	if res.CandidateFiles != 1 || res.ChannelFiles != 2 || res.RecognizedFiles != 0 || res.EmbedFiles != 0 || res.GoFiles != 1 || len(res.ParseErrors) != 0 {
		t.Errorf("读数：候选 %d／通道 %d／认可目录 %d／嵌入 %d／Go %d／解析失败 %v", res.CandidateFiles, res.ChannelFiles, res.RecognizedFiles, res.EmbedFiles, res.GoFiles, res.ParseErrors)
	}
	if _, ok := res.Delivered[capsObject]; ok {
		t.Errorf("散目录的加列件不该进 Delivered：%q", res.Delivered[capsObject])
	}
}

func TestCheck_DeliverySources(t *testing.T) {
	deployNames := func(rel string) string {
		return "files=(\n  \"$ROOT_DIR/sql/migrations/startup/0010_base.sql\"\n  \"$ROOT_DIR/" + rel + "\"\n)\n"
	}
	cases := []struct {
		name       string
		setup      func(t *testing.T, root string)
		src        string // Delivered 里记的来源
		wantCands  int    // 候选来源数（散文件始终是候选 ⇒ 1；被嵌入的那个不该再算）
		wantEmbeds int
	}{
		{"① 部署通道点名的迁移", func(t *testing.T, root string) {
			write(t, root, "sql/migrations/startup/0100_caps.sql", looseCaps)
			write(t, root, DeployChannelScript, deployNames("sql/migrations/startup/0100_caps.sql"))
		}, "sql/migrations/startup/0100_caps.sql", 1, 0},
		{"② 安装器清单点名的迁移", func(t *testing.T, root string) {
			write(t, root, InstallerEmbedDir+"/0100_caps.sql", looseCaps)
			write(t, root, InstallerManifest, "# 合成清单\n0\t0010_base.sql\n100\t0100_caps.sql\n")
		}, InstallerEmbedDir + "/0100_caps.sql", 1, 0},
		{"③ 认可目录：新装基线", func(t *testing.T, root string) {
			write(t, root, "sql/schema/0100_caps.sql", looseCaps)
		}, "sql/schema/0100_caps.sql", 1, 0},
		{"③ 认可目录：离线升级副本", func(t *testing.T, root string) {
			write(t, root, "installer/migrations/0100_caps/up.sql", looseCaps)
			write(t, root, "installer/migrations/0100_caps/down.sql", "ALTER TABLE public.api_keys DROP COLUMN IF EXISTS capabilities;")
		}, "installer/migrations/0100_caps/up.sql", 1, 0},
		{"④ 生产代码字面量自带的 DDL", func(t *testing.T, root string) {
			write(t, root, "pkg/ddl.go", "package pkg\n\nconst ddl = \"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb\"\n")
		}, "code:pkg/ddl.go", 1, 0},
		{"⑤ 生产代码 //go:embed 的 SQL", func(t *testing.T, root string) {
			write(t, root, "pkg/sql/0100_caps.sql", looseCaps)
			write(t, root, "pkg/embed.go", "package pkg\n\nimport _ \"embed\"\n\n//go:embed sql/0100_caps.sql\nvar capsSQL string\n")
		}, "embed:pkg/embed.go", 1, 1},
		{"⑤ //go:embed all: 前缀与通配", func(t *testing.T, root string) {
			write(t, root, "pkg/sql/0100_caps.sql", looseCaps)
			write(t, root, "pkg/sql/other.sql", "SELECT 1;\n")
			write(t, root, "pkg/embed.go", "package pkg\n\nimport _ \"embed\"\n\n//go:embed all:sql/*.sql\nvar sqlFS embed.FS\n")
		}, "embed:pkg/embed.go", 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			write(t, root, looseRel, looseCaps)
			tc.setup(t, root)
			res := check(t, root)
			assertKeys(t, "References（已投递 ⇒ 不该红）", refKeys(res.References), nil)
			if got := res.Delivered[capsObject]; got != tc.src {
				t.Errorf("Delivered 的来源：实得 %q，期望 %q", got, tc.src)
			}
			if res.CandidateFiles != tc.wantCands || res.EmbedFiles != tc.wantEmbeds {
				t.Errorf("读数：候选 %d／嵌入 %d，期望 %d／%d", res.CandidateFiles, res.EmbedFiles, tc.wantCands, tc.wantEmbeds)
			}
		})
	}
}

func TestCheck_WhatDoesNotCount(t *testing.T) {
	t.Run("测试文件里的 DDL 不算投递", func(t *testing.T) {
		root := newRoot(t)
		write(t, root, looseRel, looseCaps)
		write(t, root, "pkg/ddl_test.go", "package pkg\n\nconst ddl = \"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS capabilities int\"\n")
		assertKeys(t, "References", refKeys(check(t, root).References), bothRefs)
	})
	t.Run("tests/ 与 scripts/ 下的 Go：既不算投递，也不算生产引用", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, DeployChannelScript, "files=(\n)\n")
		write(t, root, InstallerManifest, "# 合成清单\n")
		for _, dir := range []string{"tests/tool", "scripts/tool"} {
			write(t, root, dir+"/main.go", "package main\n\nconst q = \"SELECT capabilities FROM api_keys\"\nconst ddl = \"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS capabilities int\"\n")
		}
		write(t, root, "sql/schema/01-schema.sql", baseSQL)
		write(t, root, looseRel, looseCaps)
		res := check(t, root)
		assertKeys(t, "References", refKeys(res.References), nil)
		if res.GoFiles != 0 {
			t.Errorf("这些目录下的 Go 不该进生产面：GoFiles=%d", res.GoFiles)
		}
	})
	t.Run("解析失败的 Go 文件如实列出", func(t *testing.T) {
		root := newRoot(t)
		write(t, root, looseRel, looseCaps)
		write(t, root, "pkg/broken.go", "package pkg\n\nconst q = \"SELECT id FROM api_keys\"\n\nfunc ({\n")
		res := check(t, root)
		if len(res.ParseErrors) != 1 || !strings.Contains(res.ParseErrors[0], "pkg/broken.go") {
			t.Errorf("ParseErrors：实得 %v", res.ParseErrors)
		}
		assertKeys(t, "References（别的文件照判）", refKeys(res.References), bothRefs)
	})
	t.Run("已投递来源建成的视图：它的列不判", func(t *testing.T) {
		root := newRoot(t)
		write(t, root, "sql/schema/01-schema.sql", baseSQL+"CREATE VIEW public.api_keys_v AS SELECT id, key_hash FROM public.api_keys;\n")
		write(t, root, looseRel, "ALTER TABLE public.api_keys_v ADD COLUMN IF NOT EXISTS capabilities jsonb;\n")
		write(t, root, "pkg/v.go", "package pkg\n\nconst q = `SELECT ak.capabilities FROM public.api_keys_v ak`\n")
		assertKeys(t, "References", refKeys(check(t, root).References), nil)
	})
	t.Run("通道点名的文件磁盘上没有：如实计数，不静默", func(t *testing.T) {
		root := newRoot(t)
		write(t, root, DeployChannelScript, "files=(\n  \"$ROOT_DIR/sql/migrations/startup/0010_base.sql\"\n  \"$ROOT_DIR/sql/migrations/startup/0999_ghost.sql\"\n)\n")
		res := check(t, root)
		if res.MissingFiles != 1 {
			t.Errorf("MissingFiles：实得 %d", res.MissingFiles)
		}
	})
}

func TestCheck_KnownLedgerIsCheckedBothWays(t *testing.T) {
	saved := KnownUndelivered
	t.Cleanup(func() { KnownUndelivered = saved })
	setLedger := func(cols ...string) {
		KnownUndelivered = map[string]map[string]KnownEntry{
			"pkg/a.go": {"api_keys": {Since: "t", Reason: "r", Cols: cols}},
		}
	}
	root := newRoot(t)
	write(t, root, looseRel, looseCaps)

	t.Run("命中台账 ⇒ 不进 References，进 Known，两头核账都成立", func(t *testing.T) {
		setLedger("capabilities")
		res := check(t, root)
		assertKeys(t, "References", refKeys(res.References), nil)
		assertKeys(t, "Known", refKeys(res.Known), bothRefs)
		assertKeys(t, "Stale", res.Stale, nil)
	})
	t.Run("台账精确到列：同一张表的别列照样红", func(t *testing.T) {
		setLedger("other")
		res := check(t, root)
		assertKeys(t, "References", refKeys(res.References), bothRefs)
		assertKeys(t, "Stale", res.Stale, []string{"pkg/a.go api_keys.other"})
	})
	t.Run("台账精确到文件：别的文件引用它照样红", func(t *testing.T) {
		setLedger("capabilities")
		write(t, root, "pkg/another.go", "package pkg\n\nconst q = `INSERT INTO api_keys (key_hash, capabilities) VALUES ($1, $2)`\n")
		res := check(t, root)
		if len(res.References) != 1 || res.References[0].File != "pkg/another.go" {
			t.Errorf("References：实得 %v", refKeys(res.References))
		}
	})
	t.Run("问题修好了却没删登记 ⇒ Stale 点名（防免死金牌）", func(t *testing.T) {
		setLedger("capabilities")
		write(t, root, "sql/migrations/startup/0100_caps.sql", looseCaps)
		write(t, root, DeployChannelScript, "files=(\n  \"$ROOT_DIR/sql/migrations/startup/0010_base.sql\"\n  \"$ROOT_DIR/sql/migrations/startup/0100_caps.sql\"\n)\n")
		res := check(t, root)
		assertKeys(t, "References", refKeys(res.References), nil)
		assertKeys(t, "Known", refKeys(res.Known), nil)
		assertKeys(t, "Stale", res.Stale, []string{"pkg/a.go api_keys.capabilities"})
	})
}

// ---------- 真实文件咬合：拿 820fcc4f0 当时的三个文件与上游的修法原样来跑 ----------

func TestRealFilesBite(t *testing.T) {
	const (
		keystore = "domains/authentication/keystore_sync.go"
		verifier = "domains/authentication/verifier.go"
		adminKey = "admin/keys.go"
	)
	looseSQL := readRepo(t, embedSQL)
	healGo := readRepo(t, embedGo)
	authFiles := []string{keystore, verifier, adminKey}
	sort.Strings(authFiles)
	for _, rel := range authFiles {
		readRepo(t, rel)
	}
	build := func(t *testing.T, withHeal bool, healSrc string) string {
		root := t.TempDir()
		write(t, root, DeployChannelScript, "files=(\n)\n")
		write(t, root, InstallerManifest, "# 合成清单\n")
		write(t, root, "sql/schema/01-schema.sql", "CREATE TABLE public.api_keys (id bigserial PRIMARY KEY, key_hash text NOT NULL UNIQUE);\n")
		for _, rel := range authFiles {
			write(t, root, rel, readRepo(t, rel))
		}
		write(t, root, looseRel, looseSQL) // 修法之前：加列件只在散目录里
		if withHeal {
			write(t, root, embedSQL, looseSQL)
			write(t, root, embedGo, healSrc)
		}
		return root
	}
	filesHit := func(t *testing.T, res *Result) []string {
		t.Helper()
		var files []string
		for _, r := range res.References {
			if r.Object != capsObject {
				t.Errorf("咬到了别的对象 %s（%s:%d）", r.Object, r.File, r.Line)
			}
			if !contains(files, r.File) {
				files = append(files, r.File)
			}
		}
		sort.Strings(files)
		return files
	}

	t.Run("修法之前：三个文件全被点到", func(t *testing.T) {
		res := check(t, build(t, false, ""))
		if got := filesHit(t, res); !reflect.DeepEqual(got, authFiles) {
			t.Fatalf("被点名的文件：实得 %v，期望 %v", got, authFiles)
		}
		if len(res.References) < 3 {
			t.Fatalf("引用处数：%d（三个文件至少各一处）", len(res.References))
		}
	})
	t.Run("上游的修法原样搬进来 ⇒ 回绿，来源记为那个嵌入它的 Go 文件", func(t *testing.T) {
		res := check(t, build(t, true, healGo))
		assertKeys(t, "References", refKeys(res.References), nil)
		if res.EmbedFiles != 1 || res.Delivered[capsObject] != "embed:"+embedGo {
			t.Errorf("嵌入 %d／来源 %q", res.EmbedFiles, res.Delivered[capsObject])
		}
	})
	t.Run("变异：只删掉那行 //go:embed（Go 仍编译得过）⇒ 重新变红", func(t *testing.T) {
		lines := strings.Split(healGo, "\n")
		mutated := make([]string, 0, len(lines))
		removed := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "//go:embed") {
				removed++
				continue
			}
			mutated = append(mutated, l)
		}
		if removed != 1 {
			t.Fatalf("变异没落地：上游文件里 //go:embed 行数 = %d", removed)
		}
		res := check(t, build(t, true, strings.Join(mutated, "\n")))
		if got := filesHit(t, res); !reflect.DeepEqual(got, authFiles) {
			t.Fatalf("变异后没红：被点名的文件 %v", got)
		}
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
