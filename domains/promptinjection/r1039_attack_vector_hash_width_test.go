package promptinjection

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// 2026-10-20 12h 审计轮修正的门禁。
//
// ★ 缺陷本体：learnAttackPattern 原来把**输入原文的 hex** 当 attack_hash：
//
//	hash := fmt.Sprintf("%x", []byte(input))
//
// 列是 `attack_hash VARCHAR(64) NOT NULL,  -- SHA-256`，
// 而 learnAttackPattern 放行 10..10000 字节的输入
// ⇒ hex 长度 = 2 × len(input) ⇒ **任何 ≥33 字节的输入都超过 64**
// ⇒ INSERT 必然失败（PG: value too long for character varying(64)）
// ⇒ 失败被 slog.Warn 吞掉 ⇒ 表恒为空。
//
// ★ 这与本轮已修的「枚举数组列绑了 JSON 字节」是**互相独立**的第二条失败路径：
// 那条修好之后，这条仍然让绝大多数真实攻击载荷写不进去。
//
// ★ 判据打的是**产物**（生产代码里的 attackVectorHash），
// 不是标准库本身 —— 只测 sha256 库的用例在任何实现下都会绿，是恒绿门。
//
// 变异：把 detector.go 里的 attackVectorHash 改回
// fmt.Sprintf("%x", []byte(input)) ⇒ 下面两条都会失败
// （长度随输入线性增长 ⇒ 33 字节起超过 64）。
func TestAttackVectorHashIsFixedWidthSHA256(t *testing.T) {
	// 10 字节是放行下界、32 是旧写法的临界点、33 是**旧写法开始溢出的那一点**，
	// 再往上取到放行上界 10000。
	for _, size := range []int{10, 32, 33, 34, 100, 1000, 10000} {
		input := strings.Repeat("a", size)
		got := attackVectorHash(input)

		if len(got) != 64 {
			t.Fatalf("input %d bytes: hash length = %d, want 64 (列是 VARCHAR(64))：%q",
				size, len(got), got)
		}
		// 与标准库算出的 SHA-256 逐字一致（确认是真 SHA-256，不是碰巧 64 长）。
		ref := sha256.Sum256([]byte(input))
		if want := hex.EncodeToString(ref[:]); got != want {
			t.Fatalf("input %d bytes: hash = %q, want %q", size, got, want)
		}
	}
}

// 判别动作：旧写法（原文 hex）的长度**随输入线性增长**，而列宽固定 64
// ⇒ 门必须能看出这个差别。
func TestAttackVectorHashLengthIsIndependentOfInputLength(t *testing.T) {
	short := attackVectorHash(strings.Repeat("a", 10))
	long := attackVectorHash(strings.Repeat("a", 10000))
	if len(short) != len(long) {
		t.Fatalf("hash length depends on input length: %d vs %d ⇒ 旧写法（原文 hex）"+
			"会让 ≥33 字节的输入超出 VARCHAR(64)，INSERT 必然失败",
			len(short), len(long))
	}
	if len(long) > 64 {
		t.Fatalf("hash length %d exceeds the column width VARCHAR(64)", len(long))
	}
}

// 对照臂：旧写法的长度必须**确实会溢出**，否则上面两条门可能是恒绿门
// （即：判据没有区分力）。
func TestOldRawHexHashWouldOverflowColumnWidth(t *testing.T) {
	oldHash := func(input string) string {
		var b strings.Builder
		for _, b0 := range []byte(input) {
			b.WriteString(hex.EncodeToString([]byte{b0}))
		}
		return b.String()
	}
	if got := len(oldHash(strings.Repeat("a", 10))); got > 64 {
		t.Fatalf("对照臂前提不成立：10 字节输入的旧 hash 已经是 %d 字符", got)
	}
	if got := len(oldHash(strings.Repeat("a", 33))); got <= 64 {
		t.Fatalf("对照臂前提不成立：33 字节输入的旧 hash 只有 %d 字符（应 >64）", got)
	}
}
