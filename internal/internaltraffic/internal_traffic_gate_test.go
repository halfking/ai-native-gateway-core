// internal_traffic_gate_test.go — 「内部/合成流量」单一事实源的门（审计 §9.73）。
//
// # 这道门在防什么
//
// 判定「这一行是不是网关内部/合成流量」这件事，本仓曾有三份**互不依赖**的
// 手抄实现：telemetry（Go，4 臂）、autoroute（Go+SQL，2 臂）、db（SQL，4 臂）。
// 三者之间没有依赖边 ⇒ 「改了一处、忘了另两处」在编译期完全等价，
// 而编译器不会提醒你。审计 §9.58.3 记的「两份名单互不相认」是同一家族。
//
// 本轮把三者改到 `internal/internaltraffic`，所以现在要钉的是**这次改动没有偷偷
// 改变行为**，以及**没有留下第四份拼写**。
//
// # 四道断言各自的职责（它们不可互相替代）
//
//	A 字节等同：渲染出的 SQL 与改前逐字相同 ⇒ 这次重构是**行为保持**的，
//	  而不是「看起来等价」。这是让其余门不必重跑语义核对的前提。
//	B 无残留拼写：那三个文件里**不再有**含生成器名的**字符串字面量**
//	  （注释里提到名字是允许的，注释不参与判定）。
//	C 常量与集合互校：SQL 列表字面量常量与 Go 集合是同一份事实的两个投影。
//	D 臂语义：4 臂判定的逐格行为，含「非 nil 空串 ≠ nil」这一格。
package internaltraffic

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 改前 autoroute.SQLExcludeSyntheticActors 渲染出的文本，逐字记录。
//
// 来源：`/tmp/sa.orig` 里那句
//
//	" AND COALESCE(" + col + ", '') NOT LIKE 'goal-%'" +
//		" AND COALESCE(" + col + ", '') NOT IN ('auto-title-generator','auto-summary-generator','session-summary')"
//
// 的渲染结果。**不是**从当前实现反推出来的——那样这道门就只会证明「代码等于它自己」。
const (
	wantBARE    = " AND COALESCE(origin_actor, '') NOT LIKE 'goal-%' AND COALESCE(origin_actor, '') NOT IN ('auto-title-generator','auto-summary-generator','session-summary')"
	wantAliased = " AND COALESCE(r2.origin_actor, '') NOT LIKE 'goal-%' AND COALESCE(r2.origin_actor, '') NOT IN ('auto-title-generator','auto-summary-generator','session-summary')"
)

// TestSQLExcludeSyntheticActorsIsByteIdenticalToThePreexistingText —— 断言 A。
func TestSQLExcludeSyntheticActorsIsByteIdenticalToThePreexistingText(t *testing.T) {
	for _, tc := range []struct{ alias, want string }{
		{"", wantBARE},
		{"r2", wantAliased},
	} {
		if got := SQLExcludeSyntheticActors(tc.alias); got != tc.want {
			t.Errorf("alias=%q 的渲染结果与改前**不逐字相同**。\n  改前：%q\n  现在：%q\n"+
				"  本次重构的承诺是「只搬名字、不动谓词」；字节不同就说明谓词的形状、\n"+
				"  分隔符或先后顺序被动了 —— 那会影响所有以 SQL 文本为键的门，\n"+
				"  把真正的语义变化淹没在噪声里。", tc.alias, tc.want, got)
		}
	}
}

// migratedClassificationFiles 是本轮从「各自手抄」改到「委托 SSOT」的三个文件。
//
// 它们的共同点：**判定**内部流量，而不是**写入** actor 名。
// `admin/auto_title_generator.go` 之类真的发出该 actor 的写入方不在此列 ——
// 那里出现字面量是正确的，源头必须在某处是字面量。
var migratedClassificationFiles = []string{
	"autoroute/shadow_actors.go",
	"domains/hooks/observability/telemetry/internal_loopback.go",
	"db/request_logs_view_schema.go",
}

// TestNoGeneratorNameStringLiteralLeftInMigratedFiles —— 断言 B。
//
// 只看**字符串字面量**，不看注释：本轮刻意在这些文件里留了大量解释性注释，
// 其中提到 actor 名是有价值的（读代码的人需要知道判定的是什么）。
// 用 grep 判「文件里不能再出现这个名字」会把注释一起杀掉，
// 于是下一个人为了「让门变绿」把解释性注释删掉 ——
// 那是**为了让测量通过而销毁被测对象**，比门红更坏。
func TestNoGeneratorNameStringLiteralLeftInMigratedFiles(t *testing.T) {
	root := repoRoot(t)
	// 允许的例外：SSOT 包自己，以及下面这些**确属判定**的第三方——
	// 它们本轮没有迁移（cmd/gateway / admin 等包的测试当前无法运行，
	// 迁移它们会留下未经验证的改动）。例外必须具名，且新迁移一处就删一条。
	allowed := map[string]string{
		// 这三个就是本轮迁移的目标，必须干净。
		"autoroute/shadow_actors.go":                                 "本轮迁移目标",
		"domains/hooks/observability/telemetry/internal_loopback.go": "本轮迁移目标",
		"db/request_logs_view_schema.go":                             "本轮迁移目标",
	}
	// knownRemaining 登记「本轮之后仍存在的判定点」：它们必须**确实**还有字面量，
	// 否则说明有人已经迁了，这条登记就该删（过期登记比没有更坏）。
	//
	// ⚠ 2026-10-03 自变异实测：这里原本登记着 `cmd/gateway/dual_read_validator.go`，
	// 而 M2（模拟「只改了一半」）一跑就把它报成**已过期** —— 那个文件只是
	// `const mirrorDriftClassSQL = db.MirrorDriftClassSQL` 的转发，字面量随 db 一起走了。
	// ⇒ 该条已被自己的 stale 检查照出来并删除。这正是它存在的理由：
	// 一条永远为真的登记会让人以为那里仍需手工同步，而实际上早已没有第二份。
	var knownRemaining = map[string]string{}

	for _, rel := range migratedClassificationFiles {
		t.Run(rel, func(t *testing.T) {
			if _, ok := allowed[rel]; !ok {
				t.Fatalf("migratedClassificationFiles 里的 %q 没在 allowed 里登记理由。\n"+
					"  本门要求「例外必须具名」：新增一个迁移目标时，要么写清它为何允许有字面量，\n"+
					"  要么它就不该出现在这个列表里。", rel)
			}
			lits, err := stringLiteralsInFile(t, root, rel)
			if err != nil {
				t.Fatalf("%v", err)
			}
			// ★ 自指断言：必须真的解析出了字面量。
			// 解析器坏掉 ⇒ lits 为空 ⇒ 本门自动全绿，那等于没有门。
			if len(lits) == 0 {
				t.Fatalf("%s 里解析出 0 个字符串字面量。\n"+
					"  解析失败与「确实没有字面量」在输出里长得一样 —— 本门必须区分它们，\n"+
					"  否则解析器一坏它就变成永绿（同 §9.72.4 的 M4 家族）。", rel)
			}
			var hits []string
			for _, lit := range lits {
				for _, name := range generatorActors {
					if strings.Contains(lit.value, name) {
						hits = append(hits, lit.where+": "+strconv.Quote(lit.value))
					}
				}
			}
			if len(hits) > 0 {
				t.Errorf("%s 里仍有 %d 个含生成器名的**字符串字面量**：\n  %s\n"+
					"  它应当改从 internal/internaltraffic 取（SQL 用 GeneratorActorsSQLList /\n"+
					"  SQLInternalLoopbackPredicate，Go 用 IsGeneratorActor）。\n"+
					"  这正是本轮要消灭的形态：判定散落在多个互不依赖的包里，改一处不会波及另一处。\n"+
					"  （注释里提到这些名字不算违规 —— 本门只看字面量。）",
					rel, len(hits), strings.Join(hits, "\n  "))
			}
		})
	}

	for rel, why := range knownRemaining {
		lits, err := stringLiteralsInFile(t, root, rel)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		found := false
		for _, lit := range lits {
			if strings.Contains(lit.value, "auto-title-generator") {
				found = true
				break
			}
		}
		if !found {
			t.Logf("注意：knownRemaining 登记的 %s 里已找不到生成器名字面量。\n"+
				"  若它已迁移，请把这条登记删掉 —— 过期登记会让人以为那里仍需手工同步。\n"+
				"  它当初的理由：%s", rel, why)
		}
	}
}

// TestSQLListConstantsMatchTheGoSets —— 断言 C。
//
// init 里的 panic 已经覆盖运行期；这道测试把同一件事变成**可定位的失败**，
// 而且额外检查「const 字面量里出现的东西都在 Go 集合里」这个方向 ——
// init 只查了「生成 == 字面量」，若有人手写字面量而 Go 集合被清空成同值，
// init 仍会过，这里能补上。
func TestSQLListConstantsMatchTheGoSets(t *testing.T) {
	if got := sqlStringList(generatorActors); got != GeneratorActorsSQLList {
		t.Errorf("GeneratorActorsSQLList = %q，生成 = %q", GeneratorActorsSQLList, got)
	}
	if got := sqlStringList(generatorRequestTypes); got != GeneratorRequestTypesSQLList {
		t.Errorf("GeneratorRequestTypesSQLList = %q，生成 = %q", GeneratorRequestTypesSQLList, got)
	}
	// 反向：字面量里的每一项都必须属于 Go 集合。
	for _, name := range parseSQLList(t, GeneratorActorsSQLList) {
		if !IsGeneratorActor(name) {
			t.Errorf("GeneratorActorsSQLList 里的 %q 不在 Go 集合中 —— 两个投影已分叉。", name)
		}
	}
	if n := len(generatorActors); n != 3 {
		t.Errorf("生成器 actor 数 = %d。252 生产实测（审计 §9.73.3）actor 臂命中 3,200 行、"+
			"而 request_type 臂命中 0 行 ⇒ 集合被改动会让那个实测结论失效，请重测后再改。", n)
	}
}

// TestClassifyArms —— 断言 D。
//
// 逐格覆盖 4 臂，重点是「非 nil 空串 ≠ nil」那一格：
// telemetry.IsInternalAutoEntry 原实现写的是 `if entry.RequestType != nil { switch TrimSpace(...) }`，
// 而 SQL 侧写的是 `TRIM(COALESCE(col,”)) IN (...)`。把「非 nil」这个条件省掉，
// 两边就会在「非 nil 空串」这一格上分歧 —— 而那正是本包最容易被"顺手清理"掉的写法。
func TestClassifyArms(t *testing.T) {
	p := func(s string) *string { return &s }
	cases := []struct {
		name                         string
		isAuto                       bool
		requestType, actor, taskType *string
		want                         InternalLoopbackArm
	}{
		{"nil 入口：不是 auto", false, p("title_gen"), p("auto-title-generator"), p("x"), ArmNone},
		{"is_auto 为假，即使 actor 命中", false, nil, p("auto-title-generator"), p("x"), ArmNone},
		{"臂1 request_type", true, p("title_gen"), nil, p("x"), ArmRequestType},
		{"臂1 request_type 带空白仍命中", true, p("  summary  "), nil, p("x"), ArmRequestType},
		{"臂1 非 nil 但空串：不命中该臂，落到下一臂", true, p(""), p("auto-title-generator"), p("x"), ArmActor},
		{"臂1 非 nil 但空串、且后续臂都不命中", true, p(""), nil, p("x"), ArmNone},
		{"臂2 actor", true, nil, p("auto-title-generator"), p("x"), ArmActor},
		{"臂2 actor=session-summary", true, nil, p("session-summary"), p("x"), ArmActor},
		{"臂2 非 nil 但空串：不命中该臂，落到兜底", true, nil, p(""), p(""), ArmTaskless},
		{"臂3 task_type 为 nil（兜底）", true, nil, nil, nil, ArmTaskless},
		{"臂3 task_type 为空串（兜底）", true, nil, p("some-business-actor"), p(""), ArmTaskless},
		{"臂3 task_type 为纯空白（兜底）", true, nil, p("some-business-actor"), p("   "), ArmTaskless},
		{"臂3 task_type 非空 ⇒ 不是内部", true, nil, nil, p("code"), ArmNone},
		{"★ 臂的**顺序**：actor 命中时即使 task_type 也空，报的是 actor 臂",
			true, nil, p("auto-title-generator"), p(""), ArmActor},
		// ★ 这两格是被变异 M3 补上的：在写这道表时**缺**它们，于是把 actor 臂
		// 从 IsGeneratorActor 换成 IsSyntheticActor（goal- 影子轮次因此被算成
		// 内部回环）时，门**全绿**。
		// 缺口的具体形状是：只测了 IsGeneratorActor 认不认 goal- 前缀，
		// 却没有测「ClassifyInternalLoopback 的 actor 臂会不会因此变宽」——
		// 前者是函数性质，后者才是语义。两者不等价。
		{"actor 臂**不**认 goal- 前缀：goal 影子轮次不是内部回环",
			true, nil, p("goal-audit"), p("code"), ArmNone},
		{"actor 臂**不**认 goal- 前缀（另一形态）",
			true, nil, p("goal-continue"), p("code"), ArmNone},
		{"非 auto 的 taskless 业务轮次不算内部", false, nil, nil, nil, ArmNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyInternalLoopback(tc.isAuto, tc.requestType, tc.actor, tc.taskType); got != tc.want {
				t.Errorf("命中臂 = %q，期望 %q", got, tc.want)
			}
			gotBool := IsInternalLoopback(tc.isAuto, tc.requestType, tc.actor, tc.taskType)
			if wantBool := tc.want != ArmNone; gotBool != wantBool {
				t.Errorf("bool 形态 = %v，与臂形态 %q 不一致", gotBool, tc.want)
			}
		})
	}
}

// TestIsSyntheticActorIsSupersetOfGeneratorActors 钉住两个函数的包含关系。
//
// IsSyntheticActor = 生成器 ∪ goal- 前缀；IsGeneratorActor 只判前者。
// 若有人给 IsGeneratorActor 也加上 goal- 前缀，聚合面会把 goal 影子轮次
// 一起排除（也许是对的），但 IsInternalLoopback 的 actor 臂也会跟着变宽 ——
// 那是**另一套语义**，两处必须分别决定，不能顺带。
func TestIsSyntheticActorIsSupersetOfGeneratorActors(t *testing.T) {
	if IsGeneratorActor("goal-audit") || IsGeneratorActor("goal-continue") {
		t.Error("IsGeneratorActor 认了 goal- 前缀 —— 它只该认三个生成器。" +
			"把前缀挪进这里会同时改变 IsInternalLoopback 的 actor 臂（那是 4 臂判据，不是聚合面判据）。")
	}
	for _, a := range []string{"goal-audit", "goal-continue", "goal-model-switch"} {
		if !IsSyntheticActor(a) {
			t.Errorf("IsSyntheticActor(%q) = false，goal- 影子轮次应算合成流量。", a)
		}
	}
	if IsSyntheticActor("") {
		t.Error("空 actor 不该算合成流量（普通用户流量）。")
	}
	if IsSyntheticActor("  ") {
		t.Error("全空白 actor 不该算合成流量。")
	}
}

// ── 测试工具 ───────────────────────────────────────────────────────────

type lit struct {
	value string
	where string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根失败 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根不像样（%s/go.mod 读不到：%v）", root, err)
	}
	return root
}

func stringLiteralsInFile(t *testing.T, root, rel string) ([]lit, error) {
	t.Helper()
	path := filepath.Join(root, rel)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, errParseLiteral{rel, err}
	}
	var out []lit
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		out = append(out, lit{value: v, where: fset.Position(bl.Pos()).String()})
		return true
	})
	return out, nil
}

type errParseLiteral struct {
	file string
	err  error
}

func (e errParseLiteral) Error() string {
	return e.file + " 解析失败：" + e.err.Error() +
		"\n  解析失败必须报错而不是返回空集 —— 空集会让「没有残留字面量」这道判据自动为真。"
}

func parseSQLList(t *testing.T, list string) []string {
	t.Helper()
	items := strings.Split(list, "','")
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.Trim(it, "'"))
	}
	if len(out) == 0 || out[0] == "" {
		t.Fatalf("解析 SQL 列表 %q 得到空结果（自指断言：解析器坏了）。", list)
	}
	return out
}
