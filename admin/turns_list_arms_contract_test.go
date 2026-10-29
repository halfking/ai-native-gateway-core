package admin

// turns_list_arms_contract_test.go —— 列表读点【生产路径】的 $N 参数契约门（R183 轮）。
//
// ★★★ 为什么这道门是【新增】的（★ 不是重复已有测试 ★★★）：★★
//
//	★ 既有的 `admin/turns_list_test.go` 钉的是 `buildTurnsListWhere(...)`，
//	★ 而那个函数只是 `buildTurnsListWhereFor(..., "t")` 的**别名包装** ★
//	★ ⇒ ⇒ 生产路径**早就不走它了**（R182 起生产走 `buildTurnsListArms`
//	★ ⇒ 两臂：`buildTurnsListWhereFor(..., "hot")` + `(..., "p")`）★★
//	★ ⇒ ⇒ 于是「$1/$2 = partition_date 上下界、$3/$4 = ts 窗口、$5… = 过滤条件、
//	★ nextArg = 10」这条契约**在生产路径上没有任何测试覆盖** ★★
//	★ ⇒ ⇒ **回归无人拦截** ⇒ ★ 本门把它补上。
//
// ★ 判据（两条，都要真牙）：
//
//	① **生产路径**生成的 SQL 里，两臂的谓词**逐字同构、只有别名不同**
//	   ⇒ ⇒ 参数编号在两臂之间**必须完全一致**（★ 两臂共用同一批 $N，
//	   ★ 所以 args 只需一份 —— ★ 这正是 `buildTurnsListArms` 只返回一份 args 的前提）。
//	② **参数编号契约**逐字钉死：$1/$2 = partition_date 上下界、$3/$4 = ts 窗口、
//	   $5… = 过滤条件、nextArg = 10。
//
// ★ 另加一条**阳性对照**：★ 断言两臂里**都**出现 partition_date 界
// ⇒ ★ 若有人把界只加到一臂上 ⇒ 本门红（★ 这正是「反连接内侧不裁剪」那类
// ★ 半截修复的形状）。
import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTurnsListArms_ParameterContract(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/turns?"+
		"project_id=p_demo&namespace=team-a&parent_request_id=parent-7&task_type=code", nil)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	from := now.Add(-time.Hour)

	sql, args, nextArg := buildTurnsListArms(req, "tenant-1", from, now, time.Time{}, "", 0)

	// ── 判据②：参数编号契约 ──
	if nextArg != 10 {
		t.Errorf("nextArg = %d, want 10（limit 占位符）", nextArg)
	}
	if len(args) != 9 {
		t.Fatalf("len(args) = %d, want 9 (%v)", len(args), args)
	}
	wantPDFrom, wantPDTo := sessionPartitionDateBounds(from, now)
	if args[0] != wantPDFrom || args[1] != wantPDTo {
		t.Errorf("args[0..1] = %v,%v want %v,%v", args[0], args[1], wantPDFrom, wantPDTo)
	}
	want := []any{from, now, "tenant-1", "p_demo", "team-a", "parent-7", "code"}
	for i, v := range want {
		if args[i+2] != v {
			t.Errorf("arg[%d] = %v, want %v", i+2, args[i+2], v)
		}
	}

	// ── 判据①：两臂谓词逐字同构、只有别名不同 ──
	// ★ 两臂共用的谓词片段（去掉别名后的形状）。
	for _, frag := range []string{
		"partition_date >= $1",
		"partition_date <= $2",
		"ts >= $3",
		"ts <= $4",
		"tenant_id = $5",
		"project_id = $6",
		"namespace = $7",
		"parent_request_id = $8",
		"task_type = $9",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("★ 生产路径 SQL 里缺片段 %q\nSQL=%s", frag, sql)
		}
	}

	// ★ 两臂各自的**别名**都必须带分区键界（★ 半截修复 ⇒ 本门红）。
	hotWithBound := strings.Contains(sql, "hot.partition_date >= $1")
	parentWithBound := strings.Contains(sql, "p.partition_date >= $1")
	if !hotWithBound {
		t.Errorf("★ hot 臂没有带 partition_date 下界 ⇒ ★ 反连接/该臂仍会全扫月分区\nSQL=%s", sql)
	}
	if !parentWithBound {
		t.Errorf("★ 父表臂没有带 partition_date 下界 ⇒ ★ 父表臂仍会全扫月分区\nSQL=%s", sql)
	}

	// ★ 两臂的谓词**逐字同构**（★ 只有别名不同）⇒ ★ 参数编号才可能共用。
	hotPred, _, _ := buildTurnsListWhereFor(req, "tenant-1", from, now, time.Time{}, "", 0, "hot")
	parentPred, _, _ := buildTurnsListWhereFor(req, "tenant-1", from, now, time.Time{}, "", 0, "p")
	normalize := func(s, alias string) string {
		s = strings.ReplaceAll(s, alias+".", "X.")
		return strings.Join(strings.Fields(s), " ")
	}
	if normalize(hotPred, "hot") != normalize(parentPred, "p") {
		t.Errorf("★ 两臂谓词不同构 ⇒ ★ 参数编号无法共用\nhot=%s\nparent=%s", hotPred, parentPred)
	}
}
