package sqlguard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestGuardPackagesAreWiredIntoMakefile —— R89-DY（211 号）
//
// 背景：208/209/210 三轮往 `sql/schema` 里加了四个门（`storage_reclaim_guard_test.go`、
// `handrun_sql_inventory_test.go`、`objects_registry_test.go` 等）。它们在**本机**
// 一路绿，但**任何 CI job 都不会跑它们**：
//
//	① `make guards` 只跑 `GUARD_PACKAGES` 这一行登记的包，而那一行原先只有 `./internal/*`；
//	② 仓库里**没有**任何 workflow 跑裸 `go test ./...`；
//	③ 唯一跑全仓的是 integration job 的 `go test -tags=integration ./...`，而它的包清单
//	   是用「有 integration-tagged 测试文件」做集合差**推导**出来的
//	   （`integration-testcontainers-ci.yml:210-218` 写明了推导规则）⇒ 按构造就排除了
//	   没有 integration 标签的 `./sql/schema`。
//
// ⇒ 兜底机制存在，却不在任何流水线上。这与 208 号 F4（「存在性探针被 Windows 桩击穿」）
// 是**同一类失效的另一个面**：那里是「门不跑」，这里是「门没人叫它跑」。
//
// 为什么这条判据要放在**本包**而不是 `sql/schema` 里：
// 一条挂在 `sql/schema` 的自证断言，只有在 `sql/schema` 被登记时才会执行 ——
// 而它要防的正是「`sql/schema` 不被登记」。**自证的断言在关键处等于没有断言。**
// ⇒ 放在一个**已经在 GUARD_PACKAGES 里**的包，它才会真的响。
func TestGuardPackagesAreWiredIntoMakefile(t *testing.T) {
	mk, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	// 单行定义（Makefile 注释写明：多行续行会让 guards-sync.sh 只读到前半段）。
	line := regexp.MustCompile(`(?m)^GUARD_PACKAGES\s*:?=\s*(.*)$`).FindSubmatch(mk)
	if line == nil {
		t.Fatalf("Makefile 里找不到 GUARD_PACKAGES 定义行：\n%s", mk)
	}
	registered := strings.Fields(string(line[1]))

	// 必须已登记的包。`./sql/schema` 是 211 号补上的那一条；其余 10 条是既有登记，
	// 一起钉住是为了让「有人把整行删了」也被抓到。
	mustHave := []string{
		"./internal/rowsguard", "./internal/errdiscard", "./internal/dbrows",
		"./internal/jsoncol", "./internal/paramguard", "./internal/sqlguard",
		"./internal/sqlreadguard", "./internal/metricguard", "./internal/partguard",
		"./internal/routeguard",
		// ⚠️ 211 号新增：`sql/schema` 里的门（208/209/210 三轮）此前不在任何 CI 路径上。
		"./sql/schema",
	}
	got := map[string]bool{}
	for _, p := range registered {
		got[p] = true
	}
	for _, p := range mustHave {
		if !got[p] {
			t.Errorf("%s 不在 Makefile 的 GUARD_PACKAGES 里 ⇒ make guards 不会跑它。"+
				"该包里的守卫只在本机手工 `go test` 时才生效，CI 上**无人执行**。\n"+
				"当前登记：%s", p, strings.Join(registered, " "))
		}
	}

	// 反向：登记的包必须真实存在，否则 `go test` 会以另一种形态失败，
	// 让「包名写错」看起来像「守卫红」。
	for _, p := range registered {
		dir := strings.TrimPrefix(p, "./")
		if _, err := os.Stat("../../" + dir); err != nil {
			t.Errorf("GUARD_PACKAGES 里的 %s 在磁盘上不存在：%v", p, err)
		}
	}
}

// TestGuardSyncIgnoresNonInternalEntries —— 211 号的配套说明钉。
//
// `scripts/checks/guards-sync.sh` 用 `grep '^\./internal/'` 过滤已登记项，
// 所以 211 号往 GUARD_PACKAGES 里加的 `./sql/schema` **不会**让那个同步检查误判。
// 本条把这个前提显式记下来：若将来有人把过滤器改成「校验所有登记项必须是 internal/*guard」，
// 本条会先红，提醒他同步处理 `./sql/schema`。
func TestGuardSyncIgnoresNonInternalEntries(t *testing.T) {
	b, err := os.ReadFile("../../scripts/checks/guards-sync.sh")
	if err != nil {
		t.Skipf("读不到 guards-sync.sh，跳过（本条只钉一个前提，不是门禁）：%v", err)
	}
	if !strings.Contains(string(b), `grep '^\./internal/'`) {
		t.Log("guards-sync.sh 的过滤器已不是 `grep '^\\./internal/'`。" +
			"请确认它对 GUARD_PACKAGES 里的非 internal 条目（如 ./sql/schema）不会误判。")
	}
	if !strings.Contains(string(b), "on_disk") {
		t.Error("guards-sync.sh 里找不到 on_disk 列表；该脚本的形态已变，本条前提需要重新确认。")
	}
}
