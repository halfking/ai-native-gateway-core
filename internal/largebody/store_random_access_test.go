package largebody

// 压缩归档上 ReadAt 的**定位读取**契约（2026-11-22 211 生产事故根修）。
//
// ## 事故形态
//
// sessioncontext.Reduce 全程共用一个归档句柄，对同一条消息要开 3 个以上
// 独立 reader（历史守卫预筛 + 两个文本视图，每规则组各一轮）。旧 ReadAt
// 把「往回读」实现成「回卷到文件开头、顺序解压到目标」⇒ 每次 reopen 都是
// O(偏移) ⇒ 3953 条消息的 7.7MB 请求体归约耗时 141 秒（211 真机 profile：
// 95.7% 在 ReadAt / readNextFrame），撞破 120 秒预算。
//
// ## 契约
//
//  1. 定位读取的字节**逐字节等于**源数据（回退 / 大跳 / 跨帧 / 越界语义全保留）；
//  2. 回退与大跳的代价是 **O(命中的帧)**，不是 O(偏移)：同一位置反复 reopen
//     每次至多解码 2 帧（跨帧时），从文件尾跳回中部同理；
//  3. 帧目录建不出来（帧头损坏）时退回旧行为——正确性与错误结论不变。

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveBody 生成可压缩但各处互异的正文（每 64KiB 帧的内容都不一样，
// 定位读错帧会立刻暴露）。
func archiveBody(size int) []byte {
	var b strings.Builder
	b.Grow(size + 4096)
	for i := 0; b.Len() < size; i++ {
		b.WriteString(fmt.Sprintf("line %09d | the quick brown fox jumps over the lazy dog | 偏移定位样本 %04d\n", i, i%97))
	}
	return []byte(b.String()[:size])
}

func captureArchive(t *testing.T, body []byte) (*Store, Ref) {
	t.Helper()
	s := newTestStore(t)
	ref, err := s.Capture(context.Background(), Identity{TenantID: "tenant", SessionID: "session"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadRange(context.Background(), ref, Span{Length: ref.Size}, ref.Size); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("capture round trip: err=%v equal=%v", err, bytes.Equal(got, body))
	}
	return s, ref
}

// readAtFull 读满 p；返回错误（越界时按 ReadFull 语义给 io.EOF 族）。
func readAtFull(f File, off int64, n int) ([]byte, error) {
	p := make([]byte, n)
	got, err := f.ReadAt(p, off)
	return p[:got], err
}

func TestCompressedReadAtRandomAccessMatchesSource(t *testing.T) {
	const size = 300_000 // 约 5 帧
	body := archiveBody(size)
	s, ref := captureArchive(t, body)
	f, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type step struct {
		name string
		off  int64
		n    int
	}
	steps := []step{
		{"forward from start", 0, 1000},
		{"small forward gap", 1500, 4096},
		{"backward reopen same span", 1500, 4096},
		{"backward reopen again", 1500, 4096},
		{"backward to earlier frame", 10, 64},
		{"span crossing frame boundary", frameSize - 100, 300},
		{"large forward jump (many frames)", 299_000, 1000},
		{"backward from tail to head", 0, 1},
		{"backward into middle frame", 130_000, 5000},
		{"exact last byte", size - 1, 1},
		{"section reader reopen x3", 70_000, 2048},
	}
	for _, st := range steps {
		got, err := readAtFull(f, st.off, st.n)
		if err != nil {
			t.Fatalf("%s: off=%d n=%d err=%v", st.name, st.off, st.n, err)
		}
		if !bytes.Equal(got, body[st.off:int(st.off)+st.n]) {
			t.Fatalf("%s: bytes differ at off=%d", st.name, st.off)
		}
		if st.name == "section reader reopen x3" {
			for k := 0; k < 3; k++ {
				// 归约器的真实形态：对同一 span 反复 NewSectionReader（各自从 0 读起）。
				sr := io.NewSectionReader(f, st.off, int64(st.n))
				all, err := io.ReadAll(sr)
				if err != nil || !bytes.Equal(all, body[st.off:int(st.off)+st.n]) {
					t.Fatalf("section reader reopen #%d: err=%v equal=%v", k, err, bytes.Equal(all, body[st.off:int(st.off)+st.n]))
				}
			}
		}
	}

	// 越界语义与 io.ReadFull / os.File.ReadAt 一致：读到文件尾字节不足即报错。
	if got, err := readAtFull(f, int64(size)-10, 100); err == nil || len(got) != 10 {
		t.Fatalf("read straddling EOF: got %d bytes err=%v, want 10 bytes + error", len(got), err)
	}
	if _, err := readAtFull(f, int64(size)+5, 4); err == nil {
		t.Fatal("read past EOF must error")
	}
	if _, err := readAtFull(f, -1, 4); err == nil {
		t.Fatal("negative offset must error")
	}
	if n, err := f.ReadAt(nil, 50); n != 0 || err != nil {
		t.Fatalf("empty read: n=%d err=%v", n, err)
	}
}

// TestCompressedReadAtBackwardIsFrameLocal 钉住复杂度契约：回退 / 大跳之后
// 每次定位读取至多解码 2 帧。旧实现下「同一位置 reopen 10 次」要从头解压
// 10 × 80 帧（变异回旧实现即红）。
func TestCompressedReadAtBackwardIsFrameLocal(t *testing.T) {
	const size = 5 << 20 // 80 帧
	body := archiveBody(size)
	s, ref := captureArchive(t, body)

	decodedBy := func(f File, run func()) int64 {
		before := FramesDecoded()
		run()
		return FramesDecoded() - before
	}

	// ── 1. 文件尾：首次顺序到达要解压全部帧，之后**反复 reopen 只解压所在帧** ──
	f, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	tail := int64(size) - 1000
	first := decodedBy(f, func() {
		if got, err := readAtFull(f, tail, 500); err != nil || !bytes.Equal(got, body[tail:tail+500]) {
			t.Fatalf("tail read: err=%v", err)
		}
	})
	if first > 81 {
		t.Fatalf("first sequential reach decoded %d frames, want <= 81 (80 + boundary)", first)
	}
	reopen := decodedBy(f, func() {
		for k := 0; k < 10; k++ {
			if got, err := readAtFull(f, tail, 500); err != nil || !bytes.Equal(got, body[tail:tail+500]) {
				t.Fatalf("tail reopen #%d: err=%v", k, err)
			}
		}
	})
	if reopen > 10*2 {
		t.Fatalf("10 reopens at the tail decoded %d frames, want <= 20 (frame-local); old behaviour = ~800", reopen)
	}

	// ── 2. 从尾部跳回中部：O(一帧)，不是 O(前半个文件) ──
	mid := int64(2_500_000)
	back := decodedBy(f, func() {
		if got, err := readAtFull(f, mid, 3000); err != nil || !bytes.Equal(got, body[mid:mid+3000]) {
			t.Fatalf("mid read: err=%v", err)
		}
	})
	if back > 2 {
		t.Fatalf("tail -> middle jump decoded %d frames, want <= 2", back)
	}
	_ = f.Close()

	// ── 3. 新句柄上的大跨步前进：不再从头解压 ──
	f2, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	jump := decodedBy(f2, func() {
		if got, err := readAtFull(f2, int64(size)-4096, 4096); err != nil || !bytes.Equal(got, body[size-4096:]) {
			t.Fatalf("big forward jump: err=%v", err)
		}
	})
	if jump > 2 {
		t.Fatalf("big forward jump on fresh handle decoded %d frames, want <= 2; old behaviour = 80", jump)
	}

	// ── 4. 小幅前进仍是纯顺序（不建目录、不多解压）──────────────────
	f3, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer f3.Close()
	seq := decodedBy(f3, func() {
		for off := int64(0); off < 3*frameSize; off += 1000 {
			n := 1000
			if off+int64(n) > int64(size) {
				n = int(int64(size) - off)
			}
			if got, err := readAtFull(f3, off, n); err != nil || !bytes.Equal(got, body[off:off+int64(n)]) {
				t.Fatalf("sequential read at %d: err=%v", off, err)
			}
		}
	})
	if seq > 4 {
		t.Fatalf("pure sequential scan of 3 frames decoded %d frames, want <= 4", seq)
	}
}

// TestCompressedReadAtCorruptFrameHeaderFallsBackToSequential 钉住降级合同：
// 帧目录建不出来时不改变正确性与错误结论——损坏帧之前的数据照常可读，
// 读到损坏帧及之后一律报 ErrCorrupted（与旧实现顺序解压时的结论相同）。
func TestCompressedReadAtCorruptFrameHeaderFallsBackToSequential(t *testing.T) {
	const size = 300_000
	body := archiveBody(size)
	s, ref := captureArchive(t, body)

	// 把第 3 帧的 compLen 改成越界值（帧头 = uncompLen(4) compLen(4) crc(4)）。
	rawPath := filepath.Join(s.RootPath(), ref.RelativePath())
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	// 定位第 3 帧帧头：跳过文件头，再按前两帧的 compLen 跳帧体。
	off := int64(headerSize)
	for i := 0; i < 2; i++ {
		compLen := int64(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		off += frameHeaderSize + compLen
	}
	binary.LittleEndian.PutUint32(raw[off+4:off+8], uint32(maxFrameSize+1))
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// 损坏帧之前（第 1 帧内）：直接读、回退读都正确。
	for k := 0; k < 2; k++ {
		if got, err := readAtFull(f, 500, 2000); err != nil || !bytes.Equal(got, body[500:2500]) {
			t.Fatalf("read before corrupt frame #%d: err=%v", k, err)
		}
	}
	// 读到损坏帧所在区域：无论走目录（建不出来）还是顺序重放，结论都是损坏。
	if _, err := readAtFull(f, int64(2*frameSize+10), 100); err == nil {
		t.Fatal("read into corrupt frame must error")
	}
}
