package sanitize

// 归约量具（2026-11-23 大请求体归约提速轮入库；只用仓内规则文件与合成会话，
// 不依赖外部数据）。
//
//	go test ./security/sanitize/ -run '^$' -bench 'ReduceWithHistoryGuard' -benchtime 3x
//
// 量的是 sessioncontext.Reduce 对一份约 7.8MB / 3200 条的 agent 式会话在三种
// 形态下的耗时：
//
//	baseline        串行 + 无缓存 —— 2026-11-23 之前的线上行为
//	parallel-cold   并行 + 缓存冷 —— 进程刚启动（或规则刚变更）后的第一轮
//	parallel-warm   并行 + 缓存热 —— 同一会话后续每一轮的常态
//
// 三者的归约输出必须逐字节相同（bench 里顺带断言）。
//
// 规则取 configs/sensitive_patterns.yaml（经 PatternDetector.HistoryGuard，与
// 生产装配同构；生产上规则来自托管规则表，条数不同但量级相近）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

// benchConversation 生成一份结构上像 agent 会话的合成请求体（中英混排、
// 工具调用、思考块），不含任何真实会话内容。
func benchConversation(messages int) []byte {
	msgs := make([]any, 0, messages)
	msgs = append(msgs, map[string]any{"role": "system", "content": "You are a coding assistant working on a billing service."})
	line := func(i int) string {
		return fmt.Sprintf("第 %d 段：数据库端口 6432 的配置说明，mixed English tokens svc_%d，请核对部署文档后再继续。", i, i)
	}
	for i := 0; i < messages-2; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		var content any
		switch i % 4 {
		case 0:
			content = strings.Repeat(line(i)+"\n", 19)
		case 1:
			content = []any{
				map[string]any{"type": "thinking", "thinking": "thinking " + strings.Repeat(line(i), 4)},
				map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", i), "name": "Bash", "input": map[string]any{"command": fmt.Sprintf("sed -n '1,200p' config_%d.yaml", i)}},
			}
		case 2:
			content = []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%d", i-1), "content": strings.Repeat(line(i)+"\n", 14)}}
		default:
			content = strings.Repeat(line(i)+"\n", 27)
		}
		msgs = append(msgs, map[string]any{"role": role, "content": content})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "请继续，把剩下的部分也做完"})
	raw, err := json.Marshal(map[string]any{"model": "bench", "max_tokens": 10, "messages": msgs})
	if err != nil {
		panic(err)
	}
	return raw
}

func BenchmarkReduceWithHistoryGuard(b *testing.B) {
	const messages = 3213
	body := benchConversation(messages)
	if len(body) < 6<<20 {
		b.Fatalf("合成会话只有 %d 字节，不足以代表大请求体", len(body))
	}
	store, err := largebody.NewStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ref, err := store.Capture(context.Background(), largebody.Identity{TenantID: "default", SessionID: "bench"}, bytes.NewReader(body))
	if err != nil {
		b.Fatal(err)
	}
	detector := func() *PatternDetector {
		d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
		if err != nil {
			b.Fatal(err)
		}
		return d
	}
	want := ""
	for _, mode := range []struct {
		name    string
		memo    bool // 走缓存包装
		fresh   bool // 每次迭代换一个空缓存（冷）
		workers int
	}{
		{"baseline", false, false, 1},
		{"parallel-cold", true, true, 8},
		{"parallel-warm", true, false, 8},
	} {
		b.Run(mode.name, func(b *testing.B) {
			d := detector()
			if mode.memo && !mode.fresh {
				d.SetHistoryVerdictMemo(NewHistoryVerdictMemo(HistoryMemoConfig{}))
			}
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode.fresh {
					d.SetHistoryVerdictMemo(NewHistoryVerdictMemo(HistoryMemoConfig{}))
				}
				ctx := sessioncontext.WithReduceWorkers(context.Background(), mode.workers)
				ctx = sessioncontext.WithHistoryGuard(ctx, d.HistoryGuard(context.Background()))
				out, _, err := sessioncontext.Reduce(ctx, store, ref, 1<<20)
				if err != nil {
					b.Fatal(err)
				}
				sum := fmt.Sprintf("%x", sha256.Sum256(out))
				if want == "" {
					want = sum
				} else if sum != want {
					b.Fatalf("%s: 归约输出与基线不同（%s vs %s）", mode.name, sum[:12], want[:12])
				}
			}
		})
	}
}
