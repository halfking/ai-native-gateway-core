package logging

// recent.go — 进程内「近期服务日志」环形缓冲（2026-11-25 PI 分析取证轮）。
//
// ★ 用途：PI 智能体分析错误时要「检索服务的 log」。服务日志的去向因部署形态
//
//	而异（docker 的 json-file / systemd 的 journald / 可选的落盘轮转），
//	网关进程自己**没有**一个与部署形态无关的读面 ⇒ 本文件在最末端的
//	io.Writer 上挂一条旁路，把**已经格式化、已经脱敏**的日志行留最近一批。
//
// ★ 为什么挂在 writer 侧而不是 handler 侧：
//
//	· writer 侧拿到的是规范层（standardHandler + replaceAttr）**之后**的字节
//	  ⇒ 缓冲里的内容与落盘/stderr 的内容逐字相同 ⇒ 脱敏口径不可能出现第二份；
//	· 零额外格式化开销（只是把现成的字节拷一份）。
//
// ★ 有界性（三条硬上限，防「日志风暴吃光内存」）：
//
//	· WARN 及以上：recentWarnLines 行，单行 recentWarnMaxLen 字节；
//	· INFO/DEBUG：recentInfoLines 行，单行 recentInfoMaxLen 字节；
//	· 超长行**截断保留头部**并计数（不整行丢弃——取证要的往往就是行首的
//	  msg 与 request_id）。
//	两个环分开 ⇒ 高频 INFO 不会把低频的 WARN/ERROR 挤出缓冲。
//
// ★ 覆盖边界（如实披露，不假装全量）：
//
//	只含**本进程、启动以来**的日志；蓝绿切流/重启之后，旧进程的日志不在
//	缓冲里 ⇒ 读面把「缓冲里最老一行的时间」一并返回，调用方据此说
//	「日志只覆盖到 X 之后」，而不是把「没搜到」报成「没有发生」。

import (
	"bytes"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	recentWarnLines  = 4096     // WARN 及以上的行数上限
	recentWarnMaxLen = 8 << 10  // 单行上限（字节）
	recentInfoLines  = 8192     // INFO/DEBUG 的行数上限
	recentInfoMaxLen = 2 << 10  // 单行上限（字节）
	recentTapBufMax  = 32 << 10 // 旁路里「未见到换行」的残留上限
	recentDefaultHit = 200      // SearchRecent 默认返回条数
	recentMaxHit     = 1000     // SearchRecent 返回条数硬上限
)

// recentSlot 是缓冲里的一行。
type recentSlot struct {
	t     time.Time
	level slog.Level
	line  []byte
}

// recentRing 是定长环形缓冲（互斥保护；写侧一次只做一次拷贝 + 下标推进）。
type recentRing struct {
	mu      sync.Mutex
	slots   []recentSlot
	next    int // 下一个写入位置
	count   int // 已写入的行数（≤ len(slots)）
	maxLen  int // 单行字节上限（超出截断保留头部）
	clipped int64
}

func newRecentRing(n, maxLineLen int) *recentRing {
	return &recentRing{slots: make([]recentSlot, n), maxLen: maxLineLen}
}

func (r *recentRing) push(t time.Time, level slog.Level, line []byte) {
	if r == nil || len(r.slots) == 0 {
		return
	}
	rawLine := line
	if len(line) > r.maxLen {
		line = line[:r.maxLen]
	}
	buf := make([]byte, len(line))
	copy(buf, line)
	r.mu.Lock()
	r.slots[r.next] = recentSlot{t: t, level: level, line: buf}
	r.next = (r.next + 1) % len(r.slots)
	if r.count < len(r.slots) {
		r.count++
	}
	if len(line) < len(rawLine) {
		r.clipped++
	}
	r.mu.Unlock()
}

// clippedCount 返回「被截断的行数」。
//
// ★★ **必须持锁读**：写侧 `r.clipped++`（push）在锁内，而读侧
// `SearchRecent` / `RecentBufferStats` 在锁外直接读 `r.clipped`
// ⇒ **同一个 int64 既有锁内写、又有锁外读** ⇒ 按 Go 内存模型是**数据竞争**
// （`go run -race` 可复现：`WARNING: DATA RACE … Found 1 data race(s)`）。
// ⇒ **口径**：环形缓冲的其它可读字段（`next` / `count`）一律走 `snapshot()`
// 在锁内取值；`clipped` 同样不得在锁外裸读 ⇒ 本方法提供唯一的读出口。
//
// ★ 代价：读侧多一次互斥获取；与 `snapshot()` 的锁粒度相同 ⇒ 不引入新的死锁序
// （本缓冲只有 `r.mu` 一把锁，**无嵌套** ⇒ 不存在锁序倒置面）。
func (r *recentRing) clippedCount() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clipped
}

// snapshot 返回「旧 → 新」顺序的行视图（切片是新分配的；行字节只读共享）。
func (r *recentRing) snapshot() []recentSlot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]recentSlot, 0, r.count)
	start := r.next - r.count
	if start < 0 {
		start += len(r.slots)
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.slots[(start+i)%len(r.slots)])
	}
	return out
}

func (r *recentRing) reset() {
	// ★ 必须原地重置字段，不能 `*r = recentRing{…}` 整体覆盖：那会在持锁期间
	//   把**已锁的** mu 换成一把零值新锁，随后的 Unlock 解的是「没锁的锁」
	//   ⇒ runtime fatal error（不可 recover ⇒ 整个进程退出）。
	n := len(r.slots)
	r.mu.Lock()
	r.slots = make([]recentSlot, n)
	r.next, r.count, r.clipped = 0, 0, 0
	r.mu.Unlock()
}

var (
	recentWarnRing = newRecentRing(recentWarnLines, recentWarnMaxLen)
	recentInfoRing = newRecentRing(recentInfoLines, recentInfoMaxLen)
)

// RecentTap 是挂在日志管线最末端的旁路 writer。
//
// ★ 契约：Write 永不返回错误、永不阻塞等待。slog 的 JSONHandler 每条记录
//
//	一次 Write、以 '\n' 结尾 ⇒ 这里按行切分；理论外的「无换行超长输出」
//	在 recentTapBufMax 处丢弃残留，绝不让旁路自己变成内存泄漏点。
type RecentTap struct {
	mu      sync.Mutex
	buf     []byte
	orphans atomic.Int64
}

// recentTapWriter 是全进程共用的一份旁路（Init/ReInit 的各条安装路径都挂它
// ⇒ 运行期改日志配置之后缓冲不丢、不裂成两份）。
var recentTapWriter = &RecentTap{}

// Write implements io.Writer.
func (t *RecentTap) Write(p []byte) (int, error) {
	if t == nil || len(p) == 0 {
		return len(p), nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := t.buf[:i]
		t.buf = t.buf[i+1:]
		recentRecord(line)
	}
	if len(t.buf) > recentTapBufMax {
		t.orphans.Add(1)
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

// withRecent 给一组目的地 writer 加上旁路。
//
// ★ 旁路永远在最后、且永远在：传零个目的地时返回的 MultiWriter 里
//
//	也只有旁路一个成员（不会构造出「把日志全丢」的空 writer）。
func withRecent(ws ...io.Writer) io.Writer {
	all := make([]io.Writer, 0, len(ws)+1)
	for _, w := range ws {
		if w != nil {
			all = append(all, w)
		}
	}
	return io.MultiWriter(append(all, recentTapWriter)...)
}

// recentRecord 把一行已格式化的日志解析出（时间、级别）后放进对应的环。
func recentRecord(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	t, level := recentParseLine(line)
	if level >= slog.LevelWarn {
		recentWarnRing.push(t, level, line)
	} else {
		recentInfoRing.push(t, level, line)
	}
}

// timestamp / severity_number 在规范 JSON 行里的锚点
// （见 standard.go 的 replaceAttr 与 standardHandler.Handle）。
var (
	recentTSAnchor     = []byte(`"timestamp":"`)
	recentSevNumAnchor = []byte(`"severity_number":`)
)

// recentParseLine 从规范 JSON 行里取 timestamp 与级别。
//
//	解析失败不致命：时间取当前时间、级别按 INFO（行仍然保留——
//	「格式不符的行」恰恰是取证时最该看到的异常之一）。
func recentParseLine(line []byte) (time.Time, slog.Level) {
	t := time.Now()
	level := slog.LevelInfo
	if i := bytes.Index(line, recentTSAnchor); i >= 0 {
		rest := line[i+len(recentTSAnchor):]
		if j := bytes.IndexByte(rest, '"'); j > 0 {
			if parsed, err := time.Parse(time.RFC3339Nano, string(rest[:j])); err == nil {
				t = parsed
			}
		}
	}
	if i := bytes.Index(line, recentSevNumAnchor); i >= 0 {
		rest := line[i+len(recentSevNumAnchor):]
		if n, ok := recentAtoi(rest); ok {
			level = recentLevelOf(n)
		}
	}
	return t, level
}

// recentAtoi 读 rest 开头的十进制整数（遇到非数字即止；至多 3 位）。
func recentAtoi(rest []byte) (int, bool) {
	end := 0
	for end < len(rest) && end < 3 && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(string(rest[:end]))
	if err != nil {
		return 0, false
	}
	return n, true
}

// recentLevelOf 把 OpenTelemetry 风格的 severity_number 映射回 slog 级别
// （与 standard.go 的 severityNumber 互逆）。
func recentLevelOf(n int) slog.Level {
	switch {
	case n >= 21:
		return slog.LevelError + 4
	case n >= 17:
		return slog.LevelError
	case n >= 13:
		return slog.LevelWarn
	case n >= 9:
		return slog.LevelInfo
	case n >= 5:
		return slog.LevelDebug
	default:
		return slog.LevelDebug - 4
	}
}

// ── 读面 ───────────────────────────────────────────────────────────────────

// RecentQuery 是一次近期日志检索的条件。
type RecentQuery struct {
	// From / To 是时间窗（零值 ⇒ 该侧不设界）。
	From, To time.Time
	// MinLevel 是最低级别（低于 INFO 的值按 INFO 处理）。
	MinLevel slog.Level
	// MustContain 里的每个子串都必须出现（request_id / session_id / trace id）。
	MustContain []string
	// Limit 是返回条数上限（0 ⇒ recentDefaultHit；超过 recentMaxHit ⇒ 封顶）。
	Limit int
}

// RecentLine 是返回给调用方的一行。
type RecentLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Line  string    `json:"line"`
}

// RecentResult 是一次检索的结果。
type RecentResult struct {
	// Lines 按「新 → 旧」排列。
	Lines []RecentLine `json:"lines"`
	// Scanned 是落在时间窗内、达到级别的行数（两个环合计）。
	Scanned int `json:"scanned"`
	// Truncated 表示命中数超过 Limit（还有更早的匹配行没返回）。
	Truncated bool `json:"truncated"`
	// OldestAt / NewestAt 是缓冲里最老/最新一行的时间（覆盖窗口的两端）。
	// 缓冲为空时为 nil。
	OldestAt *time.Time `json:"oldest_at"`
	NewestAt *time.Time `json:"newest_at"`
	// ClippedLines 是因超长被截去尾部的行数（观测用）。
	ClippedLines int64 `json:"clipped_lines"`
}

// SearchRecent 在环形缓冲里检索（只读、不改变缓冲）。
func SearchRecent(q RecentQuery) RecentResult {
	minLevel := q.MinLevel
	if minLevel < slog.LevelInfo {
		minLevel = slog.LevelInfo
	}
	limit := q.Limit
	if limit <= 0 {
		limit = recentDefaultHit
	}
	if limit > recentMaxHit {
		limit = recentMaxHit
	}

	// 子串匹配发生在 JSON 文本上 ⇒ 针里的 " 与 \ 必须按 JSON 转义后再找
	// （否则含引号的串永远匹配不上，会被读成「没有相关日志」）。
	needles := make([][]byte, 0, len(q.MustContain))
	for _, s := range q.MustContain {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		needles = append(needles, []byte(escapeJSONSubstr(s)))
	}

	infoSlots := recentInfoRing.snapshot()
	warnSlots := recentWarnRing.snapshot()

	res := RecentResult{Lines: []RecentLine{}}
	res.OldestAt, res.NewestAt = recentBounds(infoSlots, warnSlots)
	res.ClippedLines = recentWarnRing.clippedCount() + recentInfoRing.clippedCount()

	// 两个快照各自按时间升序 ⇒ 从尾部（最新）同时回走，产出「新 → 旧」。
	i, w := len(infoSlots), len(warnSlots)
	matched := 0
	for i > 0 || w > 0 {
		var pick recentSlot
		switch {
		case i == 0:
			w--
			pick = warnSlots[w]
		case w == 0:
			i--
			pick = infoSlots[i]
		case infoSlots[i-1].t.After(warnSlots[w-1].t):
			i--
			pick = infoSlots[i]
		default:
			w--
			pick = warnSlots[w]
		}
		if pick.level < minLevel || !recentInWindow(pick.t, q.From, q.To) {
			continue
		}
		res.Scanned++
		if !recentMatchAll(pick.line, needles) {
			continue
		}
		matched++
		if len(res.Lines) < limit {
			res.Lines = append(res.Lines, RecentLine{Time: pick.t, Level: pick.level.String(), Line: string(pick.line)})
		}
	}
	res.Truncated = matched > len(res.Lines)
	return res
}

func recentInWindow(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// recentMatchAll 报告 line 是否含有全部针。
func recentMatchAll(line []byte, needles [][]byte) bool {
	for _, n := range needles {
		if !bytes.Contains(line, n) {
			return false
		}
	}
	return true
}

// recentBounds 取两个环里最老/最新一行的时间。
func recentBounds(info, warn []recentSlot) (*time.Time, *time.Time) {
	var oldest, newest *time.Time
	consider := func(slots []recentSlot) {
		if len(slots) == 0 {
			return
		}
		lo, hi := slots[0].t, slots[len(slots)-1].t
		if oldest == nil || lo.Before(*oldest) {
			oldest = &lo
		}
		if newest == nil || hi.After(*newest) {
			newest = &hi
		}
	}
	consider(info)
	consider(warn)
	return oldest, newest
}

// escapeJSONSubstr 把针里的 JSON 特殊字符转义（与 slog JSONHandler 的输出形态对齐）。
func escapeJSONSubstr(s string) string {
	if !strings.ContainsAny(s, `"\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RecentStats 是缓冲的占用读数（观测/测试用）。
type RecentStats struct {
	WarnLines int   `json:"warn_lines"`
	InfoLines int   `json:"info_lines"`
	WarnBytes int64 `json:"warn_bytes"`
	InfoBytes int64 `json:"info_bytes"`
	Clipped   int64 `json:"clipped_lines"`
	Orphans   int64 `json:"orphan_chunks"`
}

// RecentBufferStats 返回缓冲占用。
func RecentBufferStats() RecentStats {
	warn := recentWarnRing.snapshot()
	info := recentInfoRing.snapshot()
	st := RecentStats{
		WarnLines: len(warn),
		InfoLines: len(info),
		Clipped:   recentWarnRing.clippedCount() + recentInfoRing.clippedCount(),
		Orphans:   recentTapWriter.orphans.Load(),
	}
	for _, s := range warn {
		st.WarnBytes += int64(len(s.line))
	}
	for _, s := range info {
		st.InfoBytes += int64(len(s.line))
	}
	return st
}

// ResetRecentBuffers 清空缓冲（测试隔离用；生产零调用方）。
func ResetRecentBuffers() {
	recentWarnRing.reset()
	recentInfoRing.reset()
}
