package ursm_test

// ursm-snapshot-health.sh 的「量级对比」必须与部署拓扑无关（2026-10-19，§141）
//
// # 缺陷本体
//
// 该脚本原来有一行自称「推断」的显示行，却用的是**写死 252 拓扑的绝对阈值**：
//
//	if [ "$peak" -gt 90000 ]; then 双写（154 authoritative + 245 shadow）
//	elif [ "$peak" -gt 45000 ]; then 单写（245 可能已退出 shadow）
//	else 显著低于常态，请结合上面逐桶判定看
//
// 把它带到 211（容器 acc-pg17，单写）装机后实测：峰值 35,708 行/桶 < 45000，
// 于是**每一次都落到 else 分支**，打印「显著低于常态」——
// 而同一份输出里正写着「正常 3/3」「OK: 无真空洞，无降级」。
//
// ★ 为什么这不只是难看：绝对阈值成立的前提是「这台机器的常态量级恰好等于
//   252 的双写量级」。这个前提在**任何其它实例上都不成立**，于是该行恒假；
//   而恒假的方向恰好是**报忧**——它在训练读者忽略工具的最后一行。
//   与「阈值型判据先量噪声底」（§17）、以及 252 那 9,772 条永不恢复的磁盘
//   warning 是同一条纪律的不同形态：**永不恢复的信号不是信号。**
//
// # 修法
//
// 改成与**同时段基线**比：脚本手里已有 p50（本窗，summary 第 7 字段）与
// ref_p50（基线中位，第 8 字段），比值与「有几台在写」无关。

import (
	"os"
	"strings"
	"testing"
)

// stripShellComments 去掉整行注释。
//
// ★ 本门必须剥注释再断言：订正处自己就写了 "90000" / "45000" 来解释
//   「为什么删掉这两个数」，不剥就会被自己的解释文字判绿——
//   这正是 §899 那条「源码门会被一条过期/解释性注释救活」的形态。
func stripShellComments(src string) string {
	var out []string
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// TestHealthVerdictIsTopologyFree 复现 211 的真实形态：单写、峰值远低于 252 的
// 45000/90000 两档，但本窗 p50 与同时段基线几乎相等（0.90）。
// 旧代码在这里必打「显著低于常态」——与同一份输出里的 OK 自相矛盾。
func TestHealthVerdictIsTopologyFree(t *testing.T) {
	//              buckets|sparse|slow|void|no_base|normal|p50|ref|peak|active%
	const summary = "3|0|0|0|0|3|90000|100000|20000|100"
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "2026-10-18|2026-10-11|8" ;;
  *)                        echo '` + summary + `' ;;
esac`
	_, stdout, _ := runHealth(t, stub)

	if !strings.Contains(stdout, "接近同时段常态") {
		t.Fatalf("★ p50/基线 = 0.90 却没判成「接近常态」—— 说明判定还在依赖绝对阈值，"+
			"在常态量级小于 252 双写的实例上会恒假（211 实测形态）\nstdout:\n%s", stdout)
	}
	if strings.Contains(stdout, "显著低于常态") {
		t.Fatalf("★ 峰值 20000 小于 252 的 45000 档就报「显著低于常态」—— "+
			"这是绝对阈值恒假的那一支\nstdout:\n%s", stdout)
	}
	if !strings.Contains(stdout, "0.90") {
		t.Fatalf("必须打印实测比值，否则事后无法复核这句话怎么来的\nstdout:\n%s", stdout)
	}
}

// TestHealthVerdictFlagsHalfWriteBand 负控：比值真的掉到 0.6 时必须报出来。
// 没有这条，上一条可以靠「永远说接近常态」变绿。
func TestHealthVerdictFlagsHalfWriteBand(t *testing.T) {
	// p50 60000 / ref 100000 = 0.60 ⇒ 双写掉一台正落此带。
	const summary = "3|1|0|0|0|2|60000|100000|60000|100"
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "2026-10-18|2026-10-11|8" ;;
  *)                        echo '` + summary + `' ;;
esac`
	_, stdout, _ := runHealth(t, stub)
	if !strings.Contains(stdout, "低于常态") || strings.Contains(stdout, "接近同时段常态") {
		t.Fatalf("比值 0.60 必须落到「低于常态」带（双写掉一台的形态），实际输出：\n%s", stdout)
	}
}

// TestHealthHourBranchHasNoAbsoluteThresholds 结构门：小时分支里不得再出现
// 绝对阈值。它判的是「这些可枚举的属性在不在」，所以允许文本断言 —
// 但必须先剥注释（见 stripShellComments 的说明）。
func TestHealthHourBranchHasNoAbsoluteThresholds(t *testing.T) {
	// ⚠ healthPath 是给 runHealth 当 cmd.Dir=".." 用的相对路径（见该文件 :20/:31）。
	// 进程内 ReadFile 的 CWD 就是包目录，必须自己补 ../，否则门读不到脚本而判红。
	b, err := os.ReadFile("../" + healthPath)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	seg := stripShellComments(string(b))

	for _, bad := range []string{"90000", "45000", "双写（154 authoritative", "单写（245 可能已退出"} {
		if strings.Contains(seg, bad) {
			t.Fatalf("小时分支里仍留着写死 252 拓扑的 %q —— 在任何常态量级不等于 252 双写的"+
				"实例上恒假，且恒假方向是报忧", bad)
		}
	}
	// 正面：判定必须由基线比值驱动。
	for _, want := range []string{"$ref_p50", "接近同时段常态", "显著低于常态"} {
		if !strings.Contains(seg, want) {
			t.Fatalf("判定应改为与同时段基线比（缺 %q）", want)
		}
	}
}
