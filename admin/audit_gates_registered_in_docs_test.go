//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// audit_gates_registered_in_docs_test.go —— 「每道门都得在它自己那轮审计文档里登记」。
//
// 为什么要这道门：§9.265 的第五道门 `TestBodiesSwitchPairIsPinnedAndPaired` 写完代码、
// 推上 origin/main，并在 commit message 里写明了它的存在 —— 但**审计文档那节仍写
// 「门（4 道，常驻）」，表格只有 4 行**。下一轮现核「门名清单 vs 文档声明」时才发现：
// 源码树里 16 道，本节声称 4 道。
//
// 硬约束写的是「**新增的类/集合必须同时进报告和门**」。这道门是那条约束的**反向**检查：
// 已落地的门**不能只进代码不进报告**。
//
// ★ 门名从**源码树正则发现**，不在这里手写清单 ——
//
//	手写清单会自己有「加了一道新门忘了加进清单」的同一种病。
var testFuncRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(t \*testing\.T\)`)

// auditGateDocs 把每个门文件映射到「负责登记它的审计文档」。
// 一个文件只登记一份，但下面会额外校验「门名确实出现在**某一轮**文档里」，
// 所以映射写错的后果是报错信息指错文件，不会让真正漏登的门变绿。
var auditGateDocs = map[string]string{
	"admin/request_logs_retirement_tiering_test.go": "docs/audit/2026-10-05-v1-reader-retirement-tiering.md",
	"admin/v1_bodies_read_path_test.go":             "docs/audit/2026-10-06-v1-bodies-read-path.md",
	"admin/v1_bodies_read_path_realdb_test.go":      "docs/audit/2026-10-06-v1-bodies-read-path.md",
	"cmd/gateway/dual_read_both_faces_test.go":      "docs/audit/2026-10-06-v1-stopwrite-precondition.md",
	"cmd/gateway/v1_write_liveness_test.go":         "docs/audit/2026-10-06-v1-write-liveness-signal.md",
	"cmd/gateway/v1_write_liveness_realdb_test.go":  "docs/audit/2026-10-06-v1-write-liveness-signal.md",
}

// TestAuditGatesAreRegisteredInTheirDocs 从源码树取出每一道门名，
// 要求它在对应的审计文档里被逐字登记。
//
// 变异检验（两条方向）：
//   - 从门文件里删掉一个 Test 函数 ⇒ 门数的期望值变化，红
//   - 把文档里的某个门名改掉 ⇒ 「该门没有在文档里登记」，红
func TestAuditGatesAreRegisteredInTheirDocs(t *testing.T) {
	root := repoRootFromCaller(t)

	// 先把每一份文档读进来（一次读，多处用），并确认文档本身存在。
	docBody := map[string]string{}
	for _, doc := range auditGateDocs {
		if _, seen := docBody[doc]; seen {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("读审计文档 %s: %v", doc, err)
		}
		docBody[doc] = string(b)
	}

	total := 0
	for file, doc := range auditGateDocs {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("读门文件 %s: %v", file, err)
		}
		names := testFuncRe.FindAllStringSubmatch(string(b), -1)
		if len(names) == 0 {
			t.Errorf("%s 里一道 Test 函数都没有 —— 要么文件被清空了，要么它已经不属于本审计轮，"+
				"两种情况都不该继续登记在 auditGateDocs 里", file)
			continue
		}
		total += len(names)
		for _, m := range names {
			if !strings.Contains(docBody[doc], m[1]) {
				t.Errorf("门 %s（定义在 %s）**没有在 %s 里登记** —— "+
					"新门必须同时进报告与门，只进代码会让审计文档少算工作量、"+
					"也让「报告↔门对不上」这种缺陷留在仓库里",
					m[1], file, doc)
			}
		}
	}

	// 逐文件计数也钉住：改名不算破坏，但「少了一道门且没登记」会先在上面报出来。
	if total != 16 {
		t.Errorf("本审计门总数 = %d，期望 16 —— 若刚删/加过门，请连同 %d 道一起核对审计文档的「门（N 道）」",
			total, total)
	}
}
