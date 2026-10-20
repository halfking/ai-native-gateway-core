// auto_index_refresher_rollup_atomic_test.go — 2026-10-20 退役轮 R69：
// credential_model_index rollup 的**跨进程原子性**门。
//
// ★ 这道门存在的理由（缺陷本体，2026-10-20 本机实测）：
//
//	8781（新构建，rollup 正确）  INSERT 75 行（bucket=B）→ 提交
//	8782（旧构建，rollup 42703）  DELETE bucket=B 成功 → INSERT 42703 失败
//	⇒ credential_model_index_hot 恒空
//
// 证据：pg_stat_user_tables 上 hot 表 n_tup_ins=1036 / n_tup_del=968 /
// n_live_tup=0 —— 「写进去又被删掉」的直接读数。
//
// 后果不是「指标陈旧」而是**功能缺失**：autoroute 候选池读视图
// credential_model_index_with_current_month（hot ∪ 月分区），新出现的
// (credential, raw_model) 组合在月分区里要等 8h 排水窗口才可见 ⇒ 在此
// 之前该组合在候选池里**根本不存在** ⇒ 路由层 model_not_found /
// no_candidate ⇒ 100% 流量失败。
//
// 为什么这个门要**两道**：
//
//	门 A（AST 形状门，离线）：DELETE 与 INSERT 必须跑在**同一个事务**上，
//	    且必须**先取跨进程 advisory lock**再取行锁。
//	    纯文本搜索抓不到「谁在 tx 上、谁在 pool 上」这种语义；
//	    用 go/ast 才能断言「DELETE 之前有锁、INSERT 之前有 DELETE、
//	    两者都在 tx 上、没有裸 r.db.Exec」。
//	门 B（真库阻塞门）：另一个会话持有同一把 advisory lock 时，
//	    产品代码的 rollup 必须**被挡住**；锁释放后必须成功完成。
//	    门 A 是形状，门 B 是行为 —— 门 A 说「代码里有取锁这一行」，
//	    门 B 说「这一行真的把并发挡下来了」。
//
// 真库腿零足迹：门 B 用的 bucket 是**非 5 分钟对齐**的时间戳
// （当月 1 日 00:01 UTC）⇒ 产品的 DELETE 不会误伤任何真实行；
// 门 B 结束后用同一 bucket 精确删除自己写入的行。
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
package bg

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

// rollupFuncInSource 解析 bg 包内的 auto_index_refresher.go。
func rollupFuncInSource(t *testing.T) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "auto_index_refresher.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 auto_index_refresher.go 失败：%v", err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "rollupCredentialModelIndex" {
			return fd
		}
	}
	t.Fatal("auto_index_refresher.go 里找不到 rollupCredentialModelIndex")
	return nil
}

// TestRollupCredentialModelIndex_DeleteInsertAreAtomicAndLockGuarded 是门 A。
//
// 断言（按源码顺序）：
//  1. 存在 `tx, err := r.db.Begin(...)` 形态的事务变量；
//  2. 函数体内**没有**任何直接打在 r.db 上的 Exec（即没有绕过事务的写）；
//  3. 第一条 tx 上的 Exec 是 advisory lock（SQL 里含 pg_advisory_xact_lock），
//     且它引用了 credentialModelIndexRollupLockKey 这个常量；
//  4. 之后依次是 DELETE（实参是 deleteSQL 变量）、INSERT（insertSQL 变量）；
//  5. 最后是 tx.Commit。
func TestRollupCredentialModelIndex_DeleteInsertAreAtomicAndLockGuarded(t *testing.T) {
	fn := rollupFuncInSource(t)

	// 1) 找事务变量：body 里 `tx, err := r.db.Begin(...)` 形态的赋值。
	txVar := ""
	for _, st := range fn.Body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Begin" {
			continue
		}
		recv, ok := sel.X.(*ast.SelectorExpr)
		if !ok || recv.Sel.Name != "db" {
			continue
		}
		if outer, ok := recv.X.(*ast.Ident); !ok || outer.Name != "r" {
			continue
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			txVar = id.Name
		}
	}

	type event struct {
		kind string // "exec" | "commit"
		recv string // 接收者变量名
		arg  string // Exec 的第 2 个实参（SQL 文本或变量名）
	}
	var events []event

	// 2) 收集所有调用事件（按源码顺序）。
	//
	// ⚠ 形态要点：`if _, err := tx.Exec(...); err != nil {` 里那次调用挂在
	// **IfStmt 的 Init**（AssignStmt）上，不是 ExprStmt —— 只认 ExprStmt
	// 的扫描器会**看不见**它，判据就会假红（本门第一版就踩了这个坑：
	// 门红但红因是「扫描器没看见那行」而不是「代码没取锁」）。
	// 所以这里不按语句类型分流，直接遍历所有 CallExpr。
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// 接收者有两种形态：事务变量（tx，Ident）与池/字段（r.db，SelectorExpr）。
		// 两种都要收进事件表 —— 漏掉 r.db 那种会让「绕过事务的写」这一条
		// 判据**看不见**它（第一版就漏了：只认 Ident）。
		var recv string
		switch v := sel.X.(type) {
		case *ast.Ident:
			recv = v.Name
		case *ast.SelectorExpr:
			if base, ok := v.X.(*ast.Ident); ok {
				recv = base.Name + "." + v.Sel.Name
			}
		}
		if recv == "" {
			return true
		}
		switch sel.Sel.Name {
		case "Exec":
			arg := ""
			if len(call.Args) > 1 {
				switch a := call.Args[1].(type) {
				case *ast.Ident:
					arg = a.Name
				case *ast.BasicLit:
					arg = strings.Trim(a.Value, "`\"")
				}
			}
			events = append(events, event{kind: "exec", recv: recv, arg: arg})
		case "Commit":
			events = append(events, event{kind: "commit", recv: recv})
		}
		return true
	})

	if txVar == "" {
		t.Fatalf("rollupCredentialModelIndex 里找不到 `tx, err := r.db.Begin(...)` 形态的事务变量；" +
			"DELETE 与 INSERT 必须落在同一个事务里（跨进程互斥 + 读者不可见中间态）")
	}

	// 2) 没有任何绕过事务的写。
	for _, e := range events {
		if e.kind == "exec" && e.recv != txVar {
			t.Fatalf("rollup 里存在不在这条事务上的写（接收者 %q，事务变量 %q）："+
				"参数=%q —— 绕过事务就等于把 DELETE 与 INSERT 拆回两个隐式事务，"+
				"跨实例时会互删（2026-10-20 实测 hot 表 n_live_tup=0）", e.recv, txVar, e.arg)
		}
	}

	// 3~5) 顺序：lock → delete → insert → commit。
	var lockIdx, delIdx, insIdx, commitIdx = -1, -1, -1, -1
	for i, e := range events {
		switch {
		case e.kind == "exec" && strings.Contains(e.arg, "pg_advisory_xact_lock"):
			if lockIdx == -1 {
				lockIdx = i
			}
		case e.kind == "exec" && e.arg == "deleteSQL":
			delIdx = i
		case e.kind == "exec" && e.arg == "insertSQL":
			insIdx = i
		case e.kind == "commit":
			commitIdx = i
		}
	}
	if lockIdx < 0 {
		t.Fatalf("rollup 里没有 pg_advisory_xact_lock —— 同库多实例（蓝绿部署窗口）" +
			"会互相删掉对方刚写的行")
	}
	if lockIdx != 0 {
		t.Fatalf("advisory lock 必须是事务里的第一条语句（当前在第 %d 条）—— "+
			"否则两个实例仍可能同时删同一个 bucket", lockIdx)
	}
	if delIdx < 0 || insIdx < 0 {
		t.Fatalf("rollup 里没找到 deleteSQL / insertSQL 的执行（delete=%d insert=%d）", delIdx, insIdx)
	}
	if !(lockIdx < delIdx && delIdx < insIdx) {
		t.Fatalf("语句顺序错：lock=%d delete=%d insert=%d，必须是 lock → delete → insert", lockIdx, delIdx, insIdx)
	}
	if commitIdx < 0 {
		t.Fatal("rollup 里没有 Commit —— DELETE 与 INSERT 不会原子生效")
	}
	if commitIdx < insIdx {
		t.Fatalf("Commit 必须排在 INSERT 之后（commit=%d insert=%d）", commitIdx, insIdx)
	}

	// 锁的键必须来自常量（而不是各处硬编码字面量）。
	if !strings.Contains(sourceOf(t, "auto_index_refresher.go"), "credentialModelIndexRollupLockKey") {
		t.Fatal("找不到 credentialModelIndexRollupLockKey 常量")
	}
}

// sourceOf 读同包源文件（供上面查常量名）。
func sourceOf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", name, err)
	}
	return string(b)
}

// TestRollupCredentialModelIndex_WaitsForCrossProcessAdvisoryLock 是门 B。
//
// 另一个会话（模拟同库的另一个网关实例）持有同一把 advisory lock 时，
// 产品代码的 rollup 必须**被挡住**；锁释放后必须成功完成。
//
// 这条门有牙的判据：把产品代码里的 advisory lock 去掉 ⇒ rollup 立即返回 ⇒
// 本门红（「没有等锁」）。
func TestRollupCredentialModelIndex_WaitsForCrossProcessAdvisoryLock(t *testing.T) {
	pool := rollupRealDBPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.session_turns_hot') IS NOT NULL
		    AND to_regclass('public.credential_model_index_hot') IS NOT NULL`,
	).Scan(&ok); err != nil {
		t.Skipf("probe session family: %v", err)
	}
	if !ok {
		t.Skip("会话族/credential_model_index_hot 不在位，跳过（需要装过 890 之后的库）")
	}

	// 非 5 分钟对齐的时间戳 ⇒ 产品的 DELETE 不会误伤任何真实行；
	// 它的 bucket 又比当前 bucket 老 ⇒ 候选池的 MAX(bucket) 不受影响。
	now := time.Now().UTC()
	bucket := time.Date(now.Year(), now.Month(), 1, 0, 1, 0, 0, time.UTC)
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM credential_model_index_hot WHERE bucket = $1`, bucket)
	}()

	// 另一个会话持锁（模拟同库第二个实例）。
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker connection: %v", err)
	}
	defer blocker.Release()
	blockTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	if _, err := blockTx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		credentialModelIndexRollupLockKey,
	); err != nil {
		t.Fatalf("blocker 取 advisory lock 失败: %v", err)
	}

	r := NewAutoIndexRefresher(pool, nil)

	type result struct {
		rows int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := r.rollupCredentialModelIndex(ctx, bucket)
		done <- result{rows: rows, err: err}
	}()

	select {
	case res := <-done:
		t.Fatalf("rollup 没有等跨进程 advisory lock（立即返回 rows=%d err=%v）—— "+
			"跨实例互斥失效，同库第二个实例会把本实例刚写的行删掉", res.rows, res.err)
	case <-time.After(1500 * time.Millisecond):
		// 期望：被锁挡住。
	}

	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatalf("释放 blocker 锁失败: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("释放锁后 rollup 仍失败：%v", res.err)
		}
		t.Logf("rollup 在锁释放后完成，产出 %d 行（bucket=%s）", res.rows, bucket.Format(time.RFC3339))
	case <-time.After(60 * time.Second):
		t.Fatal("释放锁后 rollup 未在 60s 内返回")
	}

	// 门自己写进去的行必须清干净（零足迹）。
	//
	// ⚠ 顺序要点：清理必须在**检查之前**显式做一次。deferred 清理是在函数
	// 返回时才跑的，而检查也在函数体内 ⇒ 只靠 defer 的话，检查必然看到门
	// 刚写进去的行 ⇒ 假红（本门第一版就踩了这个）。
	if _, err := pool.Exec(ctx,
		`DELETE FROM credential_model_index_hot WHERE bucket = $1`, bucket,
	); err != nil {
		t.Fatalf("清理门写入的行失败: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM credential_model_index_hot WHERE bucket = $1`, bucket,
	).Scan(&left); err != nil {
		t.Fatalf("count leftover: %v", err)
	}
	if left != 0 {
		t.Fatalf("门留下 %d 行残留（bucket=%s）—— 零足迹不成立", left, bucket.Format(time.RFC3339))
	}
}
