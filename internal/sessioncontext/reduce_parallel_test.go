package sessioncontext_test

// 归约并行化的等价性测试（2026-11-23，大请求体归约提速轮）。
//
// ## 钉住的不变量
//
// 并行（WithReduceWorkers）与串行必须**逐字节等价**：
//
//  1. 成功路径：输出字节、Report 全字段相同——用随机会话做属性测试（含小消息、
//     >64KB 大消息、媒体标记、含敏感内容的消息、工具调用链、多个人类消息）；
//  2. 出错路径：返回的是「下标最小的那条错误」——串行版本在第一条错误处
//     就返回，并行也必须是同一条（两条消息都出错、错误类型不同）；
//  3. 取消：中途取消返回 ctx 错误；
//  4. 读归档仍按序：并行度不改变解码帧数的量级（读在装载方协程里顺序做）；
//  5. runIndexed 自己的语义：最小下标错误 / 出错后不再装载 / 空输入 / 并行度
//     大于条目数 / 投递时取消。
//
// 测试守卫是**故意乱序完成**的（按内容散列决定睡多久）：谁先算完不可预测，
// 任何「依赖完成顺序」的 bug 都会让输出与串行不一致。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

var blockedIndex = regexp.MustCompile(`history (\d+) BLOCK`)

// shufflingGuard 可并发安全；对含 SECRET 的消息判不安全；含 BLOCKidx 的消息
// 返回带下标的错误；每条消息读 3 遍（预筛 + 两个视图，贴近真实守卫）；按内容
// 散列睡 0~400µs 让完成顺序乱掉。
type shufflingGuard struct {
	opens       atomic.Int64
	maxInFlight atomic.Int64
	inFlight    atomic.Int64
	// failAfter >0：第 N 次调用起返回 cancelErr（构造时定死，之后只读）。
	failAfter int64
	cancelErr error
}

func (g *shufflingGuard) guard(ctx context.Context, open func() io.Reader) (bool, error) {
	n := g.opens.Add(1)
	cur := g.inFlight.Add(1)
	for {
		old := g.maxInFlight.Load()
		if cur <= old || g.maxInFlight.CompareAndSwap(old, cur) {
			break
		}
	}
	defer g.inFlight.Add(-1)
	var last []byte
	for k := 0; k < 3; k++ {
		data, err := io.ReadAll(open())
		if err != nil {
			return false, err
		}
		last = data
	}
	sum := sha256.Sum256(last)
	time.Sleep(time.Duration(sum[0]) * time.Microsecond)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if g.failAfter > 0 && n >= g.failAfter {
		return false, g.cancelErr
	}
	if bytes.Contains(last, []byte("BLOCK")) {
		return false, fmt.Errorf("test: blocked at %s", hex.EncodeToString(sum[:2]))
	}
	return !bytes.Contains(last, []byte("SECRET")), nil
}

type reduceOutcome struct {
	out    []byte
	report sessioncontext.Report
	errStr string
}

func runReduce(t *testing.T, ctx context.Context, store *largebody.Store, ref largebody.Ref, maxBytes int, guard sessioncontext.HistoryGuard) reduceOutcome {
	t.Helper()
	if guard != nil {
		ctx = sessioncontext.WithHistoryGuard(ctx, guard)
	}
	out, rep, err := sessioncontext.Reduce(ctx, store, ref, maxBytes)
	o := reduceOutcome{out: out, report: rep}
	if err != nil {
		o.errStr = err.Error()
	}
	return o
}

// randomConversation 生成一个结构上像 agent 会话的随机请求体。
func randomConversation(rng *rand.Rand) []byte {
	filler := func(n int, tag string) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(fmt.Sprintf("line %d %s port=%d 中文说明\n", rng.Intn(1000), tag, rng.Intn(65536)))
		}
		return b.String()
	}
	var msgs []any
	msgs = append(msgs, map[string]any{"role": "system", "content": "Protect the billing configuration. " + filler(rng.Intn(300), "sys")})
	msgs = append(msgs, map[string]any{"role": "user", "content": "First task: maintain billing " + filler(rng.Intn(200), "first")})
	serial := 0
	round := func() {
		kind := rng.Intn(10)
		switch {
		case kind < 6: // 一轮工具调用 + 结果
			calls := 1 + rng.Intn(3)
			var uses, results []any
			for c := 0; c < calls; c++ {
				serial++
				id := fmt.Sprintf("toolu_%d", serial)
				uses = append(uses, map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": fmt.Sprintf("cat file_%d.go", serial)}})
				size := []int{80, 400, 2000, 9000, 30_000, 62_000}[rng.Intn(6)]
				if rng.Intn(14) == 0 {
					size = 70_000 + rng.Intn(90_000) // >64KB：走流式路径
				}
				content := filler(size, "res")
				switch rng.Intn(9) {
				case 0:
					content += "\nthe SECRET token is here\n"
				case 1:
					content += "\n" + strings.Repeat("A", 64_000)
				}
				item := map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
				if rng.Intn(12) == 0 {
					item["content"] = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("QUJD", 4000)}}, map[string]any{"type": "text", "text": "screenshot attached"}}
				}
				results = append(results, item)
			}
			if rng.Intn(2) == 0 {
				uses = append([]any{map[string]any{"type": "text", "text": "thinking about it " + filler(rng.Intn(120), "think")}}, uses...)
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": uses})
			msgs = append(msgs, map[string]any{"role": "user", "content": results})
		case kind < 8: // 普通人类消息（把尾部起点往后挪）
			msgs = append(msgs, map[string]any{"role": "user", "content": "Follow-up question " + filler(rng.Intn(500), "human")})
		default: // 纯文本助手消息
			msgs = append(msgs, map[string]any{"role": "assistant", "content": "summary " + filler(rng.Intn(800), "asst")})
		}
	}
	for i := 0; i < 12+rng.Intn(10); i++ {
		round()
	}
	if rng.Intn(3) == 0 {
		msgs = append(msgs, map[string]any{"role": "developer", "content": "dev note " + filler(60, "dev")})
	}
	// 保证请求体明显超过测试用的 256KB 预算：否则 Reduce 原样返回，等价性比较
	// 只是在比「什么都没做」（2026-11-23 的覆盖断言抓到 8 个种子里 3 个如此）。
	for {
		msgs = append(msgs, map[string]any{"role": "user", "content": "Now summarize what changed " + filler(rng.Intn(100), "last")})
		raw, err := json.Marshal(map[string]any{"model": "m", "max_tokens": 10, "messages": msgs})
		if err != nil {
			panic(err)
		}
		if len(raw) >= 320<<10 {
			return raw
		}
		msgs = msgs[:len(msgs)-1] // 摘掉结尾的 user 消息，再加轮次
		round()
	}
}

// TestReduceParallelMatchesSequential —— 属性测试：同一份归档，串行与并行
// （多个并行度、多次重复）输出与 Report 必须完全相同。
func TestReduceParallelMatchesSequential(t *testing.T) {
	// 覆盖断言：等价性只有在「被比较的路径真的被走到」时才有意义。
	omitted := 0
	for seed := int64(1); seed <= 8; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			body := randomConversation(rng)
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			base := sessioncontext.WithHistoryGuard(context.Background(), (&shufflingGuard{}).guard)
			want := runReduce(t, base, store, ref, 256<<10, nil)
			if len(want.out) == 0 && want.errStr == "" {
				t.Fatal("empty outcome")
			}
			if !want.report.Reduced {
				t.Fatalf("seed=%d: 请求体超过预算却没有触发归约，属性测试在空转", seed)
			}
			omitted += want.report.SensitiveHistoryOmitted
			for _, workers := range []int{2, 8, 64} {
				for rep := 0; rep < 2; rep++ {
					g := &shufflingGuard{}
					ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), g.guard), workers)
					got := runReduce(t, ctx, store, ref, 256<<10, nil)
					if !bytes.Equal(got.out, want.out) || got.report != want.report || got.errStr != want.errStr {
						t.Fatalf("workers=%d rep=%d: parallel diverged from sequential\n err: %q vs %q\n out: %d vs %d bytes\n report: %+v vs %+v",
							workers, rep, got.errStr, want.errStr, len(got.out), len(want.out), got.report, want.report)
					}
				}
			}
		})
	}
	if omitted == 0 {
		t.Fatal("8 个种子加起来一条「被判不安全而省略」的历史消息都没有——省略路径没被覆盖")
	}
}

// TestReduceParallelReallyRunsConcurrently —— 并行度声明确实生效：工作协程
// 里最多同时在跑的守卫数 >1（否则上面的等价性只是把串行又跑了一遍）。
func TestReduceParallelReallyRunsConcurrently(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	body := randomConversation(rng)
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	g := &shufflingGuard{}
	ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), g.guard), 8)
	runReduce(t, ctx, store, ref, 256<<10, nil)
	if g.maxInFlight.Load() < 2 {
		t.Fatalf("守卫从未并发执行（max in-flight = %d）：并行度声明没有生效", g.maxInFlight.Load())
	}
	// 对照：不声明并行度 ⇒ 恒为 1。
	g2 := &shufflingGuard{}
	runReduce(t, sessioncontext.WithHistoryGuard(context.Background(), g2.guard), store, ref, 256<<10, nil)
	if g2.maxInFlight.Load() != 1 {
		t.Fatalf("未声明并行度时不得并发: %d", g2.maxInFlight.Load())
	}
}

// TestReduceParallelReturnsLowestIndexError —— 两条消息都出错且错误不同：
// 串行返回下标较小的那条，并行也必须返回同一条。
func TestReduceParallelReturnsLowestIndexError(t *testing.T) {
	var msgs []any
	msgs = append(msgs, map[string]any{"role": "system", "content": "sys"})
	msgs = append(msgs, map[string]any{"role": "user", "content": "first task"})
	for i := 0; i < 40; i++ {
		content := fmt.Sprintf("history %d %s", i, strings.Repeat("x", 3000+i*997))
		msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "last task"})
	raw, _ := json.Marshal(map[string]any{"messages": msgs})
	// 让第 7 条和第 25 条出错，且错误里带下标：期望永远返回第 7 条的错误。
	mutate := func(raw []byte) []byte {
		s := string(raw)
		s = strings.Replace(s, "history 7 ", "history 7 BLOCK ", 1)
		s = strings.Replace(s, "history 25 ", "history 25 SECRET-BLOCK ", 1)
		return []byte(s)
	}
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(mutate(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// 守卫按内容给每条 BLOCK 消息一个**不同的**错误（带它在历史里的序号）。
	indexed := func(ctx context.Context, open func() io.Reader) (bool, error) {
		data, err := io.ReadAll(open())
		if err != nil {
			return false, err
		}
		if m := blockedIndex.FindSubmatch(data); m != nil {
			return false, fmt.Errorf("test: blocked at index %s", m[1])
		}
		return true, nil
	}
	want := runReduce(t, sessioncontext.WithHistoryGuard(context.Background(), indexed), store, ref, 64<<10, nil)
	if !strings.Contains(want.errStr, "blocked at index 7") {
		t.Fatalf("串行基线本身应返回下标 7 的错误: %q", want.errStr)
	}
	for _, workers := range []int{2, 4, 8, 32} {
		for rep := 0; rep < 5; rep++ {
			ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), indexed), workers)
			got := runReduce(t, ctx, store, ref, 64<<10, nil)
			if got.errStr != want.errStr {
				t.Fatalf("workers=%d rep=%d: 返回了别的下标的错误: %q vs %q", workers, rep, got.errStr, want.errStr)
			}
		}
	}
}

// TestReduceParallelAdjacentErrorsReturnLowestIndex —— 一片相邻的历史消息
// 都出错（都落在在途窗口里，谁先算完不可预测）：仍返回下标最小的那条。
func TestReduceParallelAdjacentErrorsReturnLowestIndex(t *testing.T) {
	var msgs []any
	msgs = append(msgs, map[string]any{"role": "user", "content": "first task"})
	for i := 0; i < 60; i++ {
		content := fmt.Sprintf("history %d %s", i, strings.Repeat("x", 1500+i*17))
		if i >= 20 && i <= 40 {
			content = fmt.Sprintf("history %d BLOCK %s", i, strings.Repeat("x", 1500+i*17))
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "last task"})
	raw, _ := json.Marshal(map[string]any{"messages": msgs})
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	indexed := func(ctx context.Context, open func() io.Reader) (bool, error) {
		data, err := io.ReadAll(open())
		if err != nil {
			return false, err
		}
		if m := blockedIndex.FindSubmatch(data); m != nil {
			return false, fmt.Errorf("test: blocked at index %s", m[1])
		}
		return true, nil
	}
	want := runReduce(t, sessioncontext.WithHistoryGuard(context.Background(), indexed), store, ref, 64<<10, nil)
	if !strings.Contains(want.errStr, "blocked at index 20") {
		t.Fatalf("串行基线: %q", want.errStr)
	}
	for _, workers := range []int{2, 4, 8} {
		for rep := 0; rep < 8; rep++ {
			ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), indexed), workers)
			if got := runReduce(t, ctx, store, ref, 64<<10, nil); got.errStr != want.errStr {
				t.Fatalf("workers=%d rep=%d: %q vs %q", workers, rep, got.errStr, want.errStr)
			}
		}
	}
}

// TestReduceParallelBigMessageErrorsAlsoOrdered —— >64KB 的流式消息与并行
// 的小消息**交错**出错：返回的仍是下标最小的那条（两条方向都测）。
func TestReduceParallelBigMessageErrorsAlsoOrdered(t *testing.T) {
	build := func(blockSmallAt, blockBigAt int) []byte {
		var msgs []any
		msgs = append(msgs, map[string]any{"role": "user", "content": "first task"})
		for i := 0; i < 12; i++ {
			if i%3 == 0 {
				content := strings.Repeat("y", 80_000)
				if i == blockBigAt {
					content = "BIGBLOCK " + content
				}
				msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
			} else {
				content := fmt.Sprintf("small %d %s", i, strings.Repeat("x", 2500))
				if i == blockSmallAt {
					content = "SMALLBLOCK " + content
				}
				msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
			}
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": "last task"})
		raw, _ := json.Marshal(map[string]any{"messages": msgs})
		return raw
	}
	indexed := func(ctx context.Context, open func() io.Reader) (bool, error) {
		data, err := io.ReadAll(open())
		if err != nil {
			return false, err
		}
		switch {
		case bytes.Contains(data, []byte("SMALLBLOCK")):
			return false, errors.New("test: small blocked")
		case bytes.Contains(data, []byte("BIGBLOCK")):
			return false, errors.New("test: big blocked")
		}
		return true, nil
	}
	cases := []struct {
		name                 string
		small, big           int
		wantSequentialResult string
	}{
		{"小消息在前", 4, 6, "small"},
		{"大消息在前", 7, 3, "big"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := largebody.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(build(tc.small, tc.big)))
			if err != nil {
				t.Fatal(err)
			}
			want := runReduce(t, sessioncontext.WithHistoryGuard(context.Background(), indexed), store, ref, 32<<10, nil)
			if !strings.Contains(want.errStr, tc.wantSequentialResult) {
				t.Fatalf("串行基线应返回 %q 的错误: %q", tc.wantSequentialResult, want.errStr)
			}
			for _, workers := range []int{2, 8} {
				ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), indexed), workers)
				got := runReduce(t, ctx, store, ref, 32<<10, nil)
				if got.errStr != want.errStr {
					t.Fatalf("workers=%d: %q vs %q", workers, got.errStr, want.errStr)
				}
			}
		})
	}
}

// TestReduceParallelCancelMidWay —— 守卫在第 N 条消息处返回 ctx 式错误：
// 串行与并行返回同一条错误。
func TestReduceParallelCancelMidWay(t *testing.T) {
	var msgs []any
	msgs = append(msgs, map[string]any{"role": "user", "content": "first task"})
	for i := 0; i < 40; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": fmt.Sprintf("history %d %s", i, strings.Repeat("x", 4000+i*311))})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "last task"})
	raw, _ := json.Marshal(map[string]any{"messages": msgs})
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	cancelErr := errors.New("test: cancelled mid-way")
	newGuard := func() *shufflingGuard { return &shufflingGuard{failAfter: 5, cancelErr: cancelErr} }
	want := runReduce(t, sessioncontext.WithHistoryGuard(context.Background(), newGuard().guard), store, ref, 64<<10, nil)
	if want.errStr != cancelErr.Error() {
		t.Fatalf("串行基线应返回取消错误: %q", want.errStr)
	}
	// 真正的 ctx 取消也测一遍：守卫在第 5 条处 cancel 掉 ctx。
	ctx, cancel := context.WithCancel(context.Background())
	// 守卫在 8 个协程里被调用 ⇒ 它自己必须可并发（计数用原子）。
	var seen atomic.Int64
	guard := func(c context.Context, open func() io.Reader) (bool, error) {
		if _, err := io.ReadAll(open()); err != nil {
			return false, err
		}
		if seen.Add(1) == 5 {
			cancel()
		}
		return c.Err() != nil, c.Err()
	}
	if got := runReduce(t, sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(ctx, guard), 8), store, ref, 64<<10, nil); !errors.Is(stringErr(got.errStr), context.Canceled) {
		t.Fatalf("并行下 ctx 取消应返回 context.Canceled: %q", got.errStr)
	}
}

// stringErr 只为断言方便：把 reduceOutcome 的错误文本还原成可判断的 sentinel。
func stringErr(s string) error {
	if s == context.Canceled.Error() {
		return context.Canceled
	}
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// TestReduceParallelKeepsArchiveReadsSequential —— 读归档在装载方协程里按序
// 进行：并行度的量级不改变解码帧数（仍是「归档帧数」量级，而不是逐条重读）。
func TestReduceParallelKeepsArchiveReadsSequential(t *testing.T) {
	body, marked := manySmallMessagesBody(2 << 20)
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "s"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	frames := (ref.Size + 64<<10 - 1) / (64 << 10)
	opens := 0
	var mu sync.Mutex
	guard := func(ctx context.Context, open func() io.Reader) (bool, error) {
		for k := 0; k < 3; k++ {
			if _, err := io.ReadAll(open()); err != nil {
				return false, err
			}
		}
		mu.Lock()
		opens++
		mu.Unlock()
		return true, nil
	}
	before := largebody.FramesDecoded()
	ctx := sessioncontext.WithReduceWorkers(sessioncontext.WithHistoryGuard(context.Background(), guard), 8)
	out, rep, err := sessioncontext.Reduce(ctx, store, ref, 1<<20)
	decoded := largebody.FramesDecoded() - before
	if err != nil || !json.Valid(out) || !rep.Reduced {
		t.Fatalf("reduce: err=%v valid=%v report=%+v", err, json.Valid(out), rep)
	}
	if limit := int64(12 * frames); decoded > limit {
		t.Fatalf("并行归约解码 %d 帧（归档 %d 帧，上限 %d）：读归档不再按序", decoded, frames, limit)
	}
	// 每条非保护消息都被守卫完整检查过（非保护消息数远多于带标记的消息数）。
	if opens <= marked {
		t.Fatalf("守卫只完整跑了 %d 次（带标记的消息就有 %d 条）", opens, marked)
	}
}
