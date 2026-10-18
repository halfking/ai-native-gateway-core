package ursm_test

// 252 巡检脚本的「VACUUM 隐式事务块」缺陷**全族门**（2026-10-18，§96）
//
// §88 在 pg17-drop-old-columnar-partitions.sh 找到一个从未成功执行的分支；
// §96 在 pg17-emergency-cleanup.sh 找到**另外两处同族**（且更隐蔽：
// `|| true` 吞掉一切 + `docker_exec` 内部的 `2>/dev/null` 让错误连日志都进不去）。
//
// ⇒ 本门是**按目录扫**而不是按已知文件名点名的：
// 点名只能挡住已知的两三个文件，而这一族的特征是**语句串形态**，
// 扫形态才能挡住下一个被写出来的同类。
//
// 判据形态：源码文本断言（脚本要 docker 才能跑，单测里无法执行），
// 先剥整行注释 —— 否则把语句写进注释就能骗过它（§87 的 N3 变异）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reSETstmt 匹配「语句串里的 SET 语句」。
//
// ★ 两代教训叠在这一个正则上：
//   第 1 代（§88 的门）：`;\s*SET\s+\w+\s*=` —— 只认 SET 在分号**之后**，
//     对 `SET x=…; VACUUM`（SET 在开头）**漏网** ⇒ 变异首轮假绿。
//   第 2 代（本门初版）：`(?:^|;)\s*SET\s+\w+\s*=.*VACUUM` —— 加了 `^`，
//     但 `^` 之后是**缩进与引号**（语句串以 `"` 开头）⇒ 同样漏网 ⇒ 又一次假绿。
//   ⇒ 正解：**不锚定位置**，只问「这一行有没有 SET 赋值语句」，
//     再由外层循环确认同一行有 VACUUM。两种顺序都覆盖，且不误报
//     `PGOPTIONS=-c statement_timeout=…`（那里面没有 `SET ` 这个 token）。
//     `\b` 守住 `RESET foo=1` 这类不含独立 SET 的串。
//   ⇒ 六个用例（含三条历史真实形态与三条不应误报的）已逐一验过。
var reSETstmt = regexp.MustCompile(`\bSET\s+\w+\s*=`)

// monitorDir 是 252 巡检脚本目录（相对本包）。
const monitorDir = "../252-monitor"

func TestNoScriptSendsVacuumInsideImplicitTransaction(t *testing.T) {
	entries, err := os.ReadDir(monitorDir)
	if err != nil {
		t.Fatalf("read %s: %v", monitorDir, err)
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(monitorDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := stripFullLineComments(string(b))
		for i, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, "VACUUM") {
				continue
			}
			if reSETstmt.MatchString(line) {
				found++
				t.Errorf("%s:%d 把 SET 与 VACUUM 塞进同一条 psql 语句串：%s\n"+
					"  PostgreSQL 会把 psql 单个 -c 的多条语句包成**隐式事务块**，\n"+
					"  而 VACUUM 明确禁止在事务块内运行 ⇒ 这条分支**每次都失败**。\n"+
					"  修法：超时设置走 PGOPTIONS（docker exec -e），语句串里只留 VACUUM。\n"+
					"  （§88 drop 脚本 6 次失败/0 次成功；§96 emergency-cleanup 两处同族。）",
					e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
	if found == 0 {
		t.Logf("扫描 %s：未发现「SET + VACUUM 同串」形态（本门仍有效，判据未因重构而失效）", monitorDir)
	}
}
