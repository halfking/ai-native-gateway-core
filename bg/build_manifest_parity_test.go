// build_manifest_parity_test.go — 产物门里的 check_id 清单必须与
// AllHealthChecks() **双向**一致
//
// 为什么需要这一条：scripts/verify-build-contents.sh 里有一份硬编码的
// `CHECKS="..."`，作用是「确认这些 check_id 的字符串**真的在二进制里**」——
// 少一个，那个缺陷在部署后就没人看得见。
//
// ★ 那个脚本只查「清单里的都在」，**不查「在的都在清单里」**。于是
//   「新增一条检查、忘了登记」是**静默**的：脚本照样 rc=0，而那条新检查在
//   部署后没有任何产物门守着。
//   2026-10-06 加第 16 条 `pricing_plan_stale` 时就是这么漏的 —— 同一轮里
//   是 `TestEveryHealthCheckHasScanBranch` 先抓到「加了 CheckID 没加 runChecks 的
//   case」，我顺手登记了清单；**如果当时没有那件事，这里就是一条静默缺口**。
//
// 同一个仓里已经有两条形状相近的门，它们各管一个方向：
//
//	TestEveryHealthCheckHasScanBranch  每条 CheckID 都有 runChecks 的 case（少写）
//	本条                          每条 CheckID 都在产物门清单里（少登记）
//
// ★ 刻意**文本扫脚本**而不是调脚本或反射枚举：脚本里那个变量是给 bash 用的
//   字符串字面量，Go 侧没有它的类型；把文件读出来按 token 切，是唯一能在
//   「忘了改脚本」这件事发生的**同一个 commit** 里发现它的办法。
//
// 不依赖真库：这是一条纯文本门。

package bg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const buildManifestPath = "../scripts/verify-build-contents.sh"

func TestEveryCheckIDIsInTheBuildManifest(t *testing.T) {
	raw, err := os.ReadFile(buildManifestPath)
	if err != nil {
		t.Fatalf("read build manifest: %v", err)
	}
	src, err2 := os.ReadFile("routing_health_checks.go")
	if err2 != nil {
		t.Fatalf("read routing_health_checks.go: %v", err2)
	}

	// 清单：取 CHECKS="..." 的第一个双引号块（脚本里只此一处同名变量）。
	body := string(raw)
	i := strings.Index(body, `CHECKS="`)
	if i < 0 {
		t.Fatalf(`%s has no CHECKS="..." assignment — the manifest's shape changed and this `+
			`test would now be vacuously green`, buildManifestPath)
	}
	body = body[i+len(`CHECKS="`):]
	j := strings.Index(body, `"`)
	if j < 0 {
		t.Fatal("CHECKS assignment is unterminated")
	}
	listed := map[string]bool{}
	for _, tok := range strings.Fields(body[:j]) {
		listed[tok] = true
	}
	if len(listed) == 0 {
		t.Fatal("parsed zero check_ids out of the manifest — the shape changed")
	}

	// 代码侧：AllHealthChecks() 里真实注册的那些。
	defined := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		i := strings.Index(line, `CheckID: "`)
		if i < 0 {
			continue
		}
		rest := line[i+len(`CheckID: "`):]
		if k := strings.IndexByte(rest, '"'); k > 0 {
			defined[rest[:k]] = true
		}
	}
	if len(defined) == 0 {
		t.Fatal("parsed zero CheckID out of routing_health_checks.go — the shape changed")
	}

	var unregistered, missing []string
	for id := range defined {
		if !listed[id] {
			unregistered = append(unregistered, id)
		}
	}
	for id := range listed {
		if !defined[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(missing)

	// 方向一（少登记）：新增检查忘了写进清单 ⇒ 部署后没有产物门守着它。
	if len(unregistered) > 0 {
		t.Errorf("these check_ids are registered in AllHealthChecks() but absent from %s: %v\n"+
			"The build gate only asserts \"every listed id is in the binary\". An id missing from "+
			"the list is therefore **silently** unverified after deployment — the new check would "+
			"ship with nothing watching it.", filepath.Base(buildManifestPath), unregistered)
	}
	// 方向二（多登记）：清单里留着已删掉的 id ⇒ 那条断言在守一个不存在的东西，
	// 而且会让「清单条数」这个数字本身开始骗人。
	if len(missing) > 0 {
		t.Errorf("the build manifest lists check_ids that AllHealthChecks() no longer defines: %v\n"+
			"Either the check was deleted (remove it from the manifest) or the id was renamed "+
			"(update both). A stale entry makes the manifest's own count a lie.",
			missing)
	}
	// 只在**真的**一致时打这行：t.Errorf 不中断执行，无条件 Logf 会在失败时
	// 也印出 "agree"，而那正是最容易被截图转发的一句。
	if !t.Failed() {
		t.Logf("manifest and AllHealthChecks() agree: %d check_ids", len(defined))
	}
}
