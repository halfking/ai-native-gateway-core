// wiring.go — W3：生产装配面（P1-2③，ADR §1/§2）。
//
// Wiring 把 W1 预热半边（Trigger + 旁路写）与 W2/W3 注入半边（读 + 渲染 +
// 注入）合成一个挂载点对象：ChatHandler 每请求调一次 Apply，其余细节全部
// 收在本包。门（Gate）关闭时 Apply 是纯字节透传——对照组/未灰度租户的
// 数据面路径与未装配完全同形（G3）。
package memoraprewarm

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// DefaultSessionTTL 是旁路字段的兜底 TTL（会话 hash 自身由 session.Manager
// 心跳续期，本值只兜 memora_context 悬挂）。
const DefaultSessionTTL = 24 * time.Hour

// GateSyncInterval 是 settings → Gate 的同步周期（灰度开关热更生效上界）。
// settings 读走 DB，不进数据面——由本 goroutine 独承担。
const GateSyncInterval = 15 * time.Second

// Wiring 是 ChatHandler 持有的挂载点对象（nil 字段结构性 no-op）。
type Wiring struct {
	client Client
	writer RedisSideWriter
	reader RedisSideReader
	gate   *Gate
	logger *slog.Logger
	ttl    time.Duration
	// dedup 实现 ADR §5 的「预热每会话至多一次（首请求）」。
	//
	// ★ 为什么必须有它：Apply 是**每请求**入口。若这里每请求都触发一次
	//   预热 ⇒ 一个会话的第 2..N 个请求各起一个 goroutine、各打一次
	//   ASM、各做一次 Redis 旁路写 ⇒ 写放大与 goroutine 放大都随会话
	//   长度线性增长 ⇒ 直接违反 ADR §5「无新增热路径成本」。
	dedup *sessionDedup
}

// NewWiring 构造。client/writer/reader/gate 任一为 nil ⇒ 对应半边 no-op
// （全部 nil = 未装配，等价于不挂 Wiring）。
func NewWiring(client Client, writer RedisSideWriter, reader RedisSideReader, gate *Gate, logger *slog.Logger) *Wiring {
	if logger == nil {
		logger = slog.Default()
	}
	return &Wiring{
		client: client, writer: writer, reader: reader, gate: gate,
		logger: logger, ttl: DefaultSessionTTL,
		dedup: newSessionDedup(DefaultDedupTTL, DefaultDedupMaxSessions),
	}
}

// Apply 是数据面每请求入口：①门内租户触发预热（fire-and-forget，请求
// N 的预热供请求 N+1 注入）；②读旁路字段渲染注入块，追加到最后一条
// user 消息。返回 (新 body, 是否注入)。
//
// 门关/未装配/读空/渲染空 ⇒ (原 body, false)——零副作用透传。
func (w *Wiring) Apply(ctx context.Context, tenantID, sessionID string, body []byte) ([]byte, bool) {
	if w == nil || len(body) == 0 {
		return body, false
	}
	if !w.gate.Allowed(tenantID) {
		return body, false
	}
	if w.client != nil && w.writer != nil && sessionID != "" {
		// ★ ADR §5：预热**每会话至多一次**。先用去重表抢占本会话的预热权，
		//   抢不到 ⇒ 本会话已预热过 ⇒ 跳过（不再起 goroutine、不再打 ASM、
		//   不再旁路写）。同一会话的并发请求只有**一个**赢家。
		//
		// ★ 为什么先取 query 再抢占：query 为空（无 user 消息/纯非文本块）
		//   时本次调用什么也做不了 ⇒ 若先抢占就会白白消耗掉本会话那一次
		//   预热机会 ⇒ 后续带正常 prompt 的请求反而拿不到预热。
		query := ExtractQueryFromBody(body)
		if query != "" && w.dedup.claim(sessionID, time.Now()) {
			Trigger(w.client, w.writer, w.gate, w.logger, tenantID, sessionID, query, w.ttl)
		}
	}
	injection := ReadAndRender(ctx, w.reader, sessionID)
	if injection == "" {
		return body, false
	}
	out, ok := InjectIntoMessages(body, injection)
	if ok {
		RecordInjectedBytes(len(injection))
	}
	return out, ok
}

// SyncGateFromSettings 以固定周期把 settings 热更键灌进 Gate（ADR §2：
// memory.prewarm.enabled 平台级 + memory.prewarm.tenant_allowlist 租户灰度）。
// 阻塞式，调用方 go 出去；ctx 取消即返回。
func SyncGateFromSettings(ctx context.Context, g *Gate, interval time.Duration) {
	if g == nil {
		return
	}
	if interval <= 0 {
		interval = GateSyncInterval
	}
	apply := func() {
		enabled := settings.GetPlatformBool(settings.KeyMemoryPrewarmEnabled, false)
		list := parseAllowlist(settings.GetPlatformString(settings.KeyMemoryPrewarmTenantAllowlist, ""))
		g.Set(enabled, list)
	}
	apply()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			apply()
		}
	}
}

// parseAllowlist 把逗号分隔的名单解析为去空白去空项的切片。
func parseAllowlist(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
