package sanitize

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026-10-20 12h 审计轮修正（第二轮 HIGH-1）的**防回退**门禁。
//
// ★ 钉住的是什么：subtractSpans 的输入 inner **不保证按位置升序**。
//
//   - dropLabelWrappedAddressFragments 里的 precise 列表是**按规则迭代顺序**
//     收集的（片段按规则 append），而片段的位置排序（sort.SliceStable）发生在
//     dropLabelWrappedAddressFragments **返回之后**。
//   - 同文件里还有一个用途相近的 outsideReservedSpans，但它**假定输入已升序**
//     （遇到 marker[0] >= end 就 break）。
//
// ⇒ 若把 subtractSpans "简化"成复用 outsideReservedSpans，未排序的 inner 会被
//
//	提前 break 截断 ⇒ 后面那个片段**整个被忽略** ⇒ 残余区间算错。
//
// ★ 变异：把 subtractSpans 里的 sort.SliceStable 去掉，并把裁剪改成直接调用
//
//	outsideReservedSpans ⇒ TestR1039SubtractSpansHandlesUnsortedInner 与
//	TestR1039LabelWrappedKeepsAddressResidualWithUnsortedPrecise 同时转红。
func TestR1039SubtractSpansHandlesUnsortedInner(t *testing.T) {
	// text 的字节布局（中文按 UTF-8 3 字节算）：
	//
	//	[0,32)   北京市朝阳区建国路88号   （含地址专有字 ⇒ 残余必须保留）
	//	[32,33)  空格
	//	[33,44)  13800138001              （精确值）
	//	[44,45)  空格
	//	[45,47)  AB                       （第二个精确值）
	text := "北京市朝阳区建国路88号 13800138001 AB"
	require.Equal(t, 47, len(text), "夹具的字节长度被改动了，下面的偏移量就不对了")

	inner := []SensitiveFragment{
		{Type: TypePhone, Start: 45, End: 47}, // **故意放在前面**（位置靠后）
		{Type: TypeEmail, Start: 33, End: 44}, // 位置靠前
	}

	gaps := subtractSpans(0, 47, inner)
	require.Len(t, gaps, 2, "两个内部片段应把区间切成两段残余：%v", gaps)
	// 残余必须**按序**且精确等于「减掉两个内部片段」的结果。
	assert.Equal(t, [2]int{0, 33}, gaps[0], "第一段残余应是 [0,33)")
	assert.Equal(t, [2]int{44, 45}, gaps[1], "第二段残余应是 [44,45)")

	// ★ 关键断言：残余区间**不得包含**任何内部片段的字节。
	//   未排序实现会在这里露出破绽：它在看到 (45,47) 后把游标推到 47，
	//   随后的 (33,44) 被判为「已在游标之前」而**整段忽略** ⇒ 残余 [0,45)，
	//   也就是**电话号码仍留在残余里**。
	for _, g := range gaps {
		for _, in := range inner {
			assert.False(t, g[0] < in.End && in.Start < g[1],
				"残余 %v 与内部片段 %v 重叠 ⇒ 未排序输入被漏算", g, [2]int{in.Start, in.End})
		}
	}
}

// TestR1039LabelWrappedKeepsAddressResidualWithUnsortedPrecise 走
// dropLabelWrappedAddressFragments 本体，验证「地址在前 + 精确值在后」时
// 真地址残余仍然保留，**且**精确值不被卷进 custom 残余。
func TestR1039LabelWrappedKeepsAddressResidualWithUnsortedPrecise(t *testing.T) {
	text := "北京市朝阳区建国路88号 13800138001 AB"
	require.Equal(t, 47, len(text), "夹具的字节长度被改动了，下面的偏移量就不对了")

	custom := SensitiveFragment{Type: TypeCustom, Start: 0, End: 47}
	phone := SensitiveFragment{Type: TypePhone, Start: 33, End: 44}
	email := SensitiveFragment{Type: TypeEmail, Start: 45, End: 47}

	// 故意让「位置靠后」的片段排在切片前面 —— 复刻规则迭代序 ≠ 位置序。
	fragments := []SensitiveFragment{custom, email, phone}

	kept := dropLabelWrappedAddressFragments(fragments, text)

	// 精确片段必须原样保留。
	var sawPhone, sawEmail bool
	for _, f := range kept {
		if f.Type == TypePhone {
			sawPhone = true
			assert.Equal(t, 33, f.Start)
			assert.Equal(t, 44, f.End)
		}
		if f.Type == TypeEmail {
			sawEmail = true
		}
	}
	assert.True(t, sawPhone, "phone 片段被误删：%+v", kept)
	assert.True(t, sawEmail, "email 片段被误删：%+v", kept)

	// custom 残余必须**只**剩真地址那一段（[0,33)），不得把电话号码卷进去。
	// 未排序实现下 custom 会被裁成 [0,45) —— 电话号码 13800138001 留在残余里。
	var customKept []SensitiveFragment
	for _, f := range kept {
		if f.Type == TypeCustom {
			customKept = append(customKept, f)
		}
	}
	require.Len(t, customKept, 1, "真地址残余应保留为恰好一段 custom：%+v", kept)
	assert.Equal(t, 0, customKept[0].Start)
	assert.Equal(t, 33, customKept[0].End, "custom 残余不得越过电话号码起点（未排序 ⇒ 残余算错）")
	assert.NotContains(t, customKept[0].Value, "13800138001",
		"电话号码被卷进了 custom 残余 ⇒ 残余区间算错")
	assert.Contains(t, customKept[0].Value, "北京市朝阳区建国路88号",
		"真地址明文必须仍在 custom 残余里（12h 审计轮 HIGH-1 的核心）")
}
