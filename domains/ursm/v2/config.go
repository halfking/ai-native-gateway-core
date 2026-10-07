package v2

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// ScoringWeights are the additive weights used by FilterAndScore when
// computing per-candidate scores. All weights are dimensionless — the
// resulting Score is used only to order candidates, never to bound
// behaviour. Tweak these in DefaultScoringWeights() rather than at
// call sites.
type ScoringWeights struct {
	Price     float64
	Latency   float64
	Stability float64
}

func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Price:     0.4,
		Latency:   0.4,
		Stability: 0.2,
	}
}

// Config is the static, mode-toggled configuration for the v2 facade.
// Always construct via DefaultConfig(); the zero value is not safe
// (RedisKeyPrefix is empty and Mode is "" rather than api.ModeOff).
type Config struct {
	Mode                api.RolloutMode
	loadErr             error
	CanaryPercent       int
	CanaryTenants       []string
	CanaryCredentials   []int
	CanaryModels        []string
	StrictCanary        bool
	ShadowSampleRate    float64
	RecordTimeoutMs     int
	RecoveryBlockOnMiss bool
	PersistIntervalSec  int
	RedisKeyPrefix      string
	// RedisDB overrides the logical Redis database URSM v2 uses, so its keys
	// can be isolated from the gateway-wide default (config.go: RedisDB, which
	// falls back to 2). -1 (the default) means "share the gateway's client,
	// unchanged" — so leaving URSM_V2_REDIS_DB unset preserves today's
	// behavior exactly.
	//
	// Why this exists (2026-10-03, production measurement): URSM's persist
	// writer collects every node with `SCAN <prefix>node:* MATCH ...`, and
	// SCAN must walk the **entire keyspace** of whatever db the client is on —
	// MATCH filters server-side but the traversal does not. Sharing db2 meant
	// walking ~1.58M session keys to find ~1.3K URSM nodes. Measured full
	// traversal: 12.95s–30.40s across samples, strongly load-dependent
	// (the same instance measured 149 ops/s in one window and 21,891 in
	// another). The writer's budget is 30s (cmd/gateway/main.go), so it lands
	// on both sides of the line and the snapshot lands intermittently.
	//
	// On an isolated db the same walk is ~0.11s (measured on a 1.6K-key db),
	// and it stops tracking the unrelated session keyspace as it grows.
	//
	// Cost of isolating: the ~1.3K node keys must be moved to the new db.
	// Prefer doing that with a one-shot copy while the writer is stopped,
	// not by letting them expire.
	RedisDB        int
	Window1mTTL    time.Duration
	Window5mTTL    time.Duration
	Window30mTTL   time.Duration
	NodeTTL        time.Duration
	ScoringWeights ScoringWeights
	// LRUMirrorSize is the capacity of the process-local NodeMirror LRU
	// (M2, spec Decision 2). Default 100000. 0 disables the mirror (every
	// FilterAndScore reads Redis). The mirror is a read accelerator only;
	// Redis remains authoritative and the LRU is never written to before a
	// successful Redis Lua write.
	LRUMirrorSize int
	// LRUMirrorSoftTTL is how long a mirror entry is trusted before the next
	// access re-reads Redis. Default 30s (spec Decision 2). Short enough that
	// a disabled/cooled node is observed within this window even if the LRU
	// still has an older Available=true entry.
	LRUMirrorSoftTTL time.Duration
	// CoolSeconds is the cooling duration (in seconds) when a node is disabled
	// due to consecutive failures. Default 30 (会话优化 v4 T5 target, was 120
	// before 2026-08-18): unstable small providers should return to the pool
	// quickly (spec §14.5 lever 3 “快恢复”). Overridable at boot via
	// URSM_V2_COOL_SECONDS and at runtime via settings_kv hot key
	// llmgw_ursm_cool_seconds (see LoadHot).
	CoolSeconds int
	// BackoffCapSeconds caps the exponential cool (cool_seconds × 2^disable_count)
	// during repeated failures inside record_request.lua. Default 1800
	// (30 minutes; T5 target — previously hard-coded 3600 in the Lua).
	// Runtime override: settings_kv llmgw_ursm_backoff_cap_seconds.
	BackoffCapSeconds int
	// MirrorGraceEnabled is the OPTIONAL availability-over-consistency gear
	// for the NodeMirror fast path (spec §14.3 灰度档). Default false = the
	// target contract: even a full NodeMirror hit is protectively rejected
	// when Redis cannot be reached. When true, a full mirror hit whose
	// entries are within the soft TTL (default 30s) may serve degraded
	// read-only routing WITHOUT touching Redis; expired entries fall back to
	// the Redis read path (and are rejected if that fails). Runtime override:
	// settings_kv llmgw_ursm_mirror_grace_enabled.
	MirrorGraceEnabled bool
	// OutageGrace bounds how long FilterAndScoreOutageFallback may serve
	// read-only routing from soft-expired NodeMirror entries after Redis
	// becomes unreachable (availability gear, 2026-09-04). Default 30m.
	// 0 disables the gear: an unreachable Redis rejects authoritative
	// routes (the pre-2026-09 fail-closed contract). Unlike
	// MirrorGraceEnabled this is boot-only (URSM_V2_OUTAGE_GRACE_SECONDS)
	// because it trades routing-state freshness for availability and ops
	// should size the window to their Redis HA budget deliberately.
	OutageGrace time.Duration
	// ShadowDoubleWrite opts shadow mode into writing sidecar records to
	// the v2 store. Default false. P0-3 (audit §7.1) flips this true during
	// the 7-day cutover comparison window. Routing stays on legacy
	// credentialstate (selectStateBackendWithReady only trusts v2 in
	// ModeAuthoritative). See rollout.Config.ShadowDoubleWrite for the
	// matching rollout-layer knob — both must agree for the sidecar to
	// fire. Loaded from URSM_V2_SHADOW_DOUBLE_WRITE (truthy 1/true/yes).
	ShadowDoubleWrite bool
	// ShadowPersist gates ONLY the snapshot writer, decoupled from
	// ShadowDoubleWrite.
	//
	// 2026-10-04 (pg_stat_statements 实测): ShadowDoubleWrite was doing two
	// unrelated jobs at once. (1) It makes the shadow sidecar record, which
	// feeds llm_gateway_ursm_v2_shadow_records_total — the ONLY source of the
	// 7-day legacy-vs-v2 drift evidence that cutover is gated on. (2) It also
	// enables the persist writer (cmd/gateway/main.go), which snapshots
	// Redis→ursm_node_snapshot_min. Measured cost of (2): 46,801,709 INSERT
	// attempts against 27,208,769 rows that actually landed — ~42% of every
	// write is a duplicate rejected by ON CONFLICT, because both gateways
	// generate snapshot_ts from their own local clock ~30s apart. That table
	// is 10 GB, 45% of the database.
	//
	// ★ 2026-10-07 更正（§10.63 实测，R49-D3）：上一段的「~42% 被 ON CONFLICT
	// 拒」是错的——两台网关的 snapshot_ts 相差 ~17s ⇒ 四元组主键不同 ⇒ 不构成
	// 冲突 ⇒ **0% 被拒、100% 双份落库**（17 分钟实测 dup_ratio=2.000，
	// distinct_pkey_rows == total_rows）。真实浪费是存储与写放大的 2 倍，
	// 不是 1.7 倍；「重复存在 ≠ 重复被拒」。ShadowPersist 也救不了：
	// ResolvePersistEnabled 对非 shadow 模式恒为 true（authoritative 分支
	// 不受此开关控制），单侧关闭只从 2 份降到 1 份。根修见 §10.63 起。
	//
	// Turning ShadowDoubleWrite off to stop the waste would also destroy the
	// drift evidence, so the two cannot be governed by one switch. This knob
	// separates them: leave the sidecar recording (evidence preserved) and
	// stop only the snapshot writer (waste removed).
	//
	// Default false = explicit opt-in only. ResolvePersistEnabled applies the
	// back-compat rule: when this is left unset, persist follows
	// ShadowDoubleWrite exactly as before, so existing deployments are
	// unchanged until an operator sets URSM_V2_SHADOW_PERSIST explicitly.
	ShadowPersist bool
	// ShadowPersistSet records whether URSM_V2_SHADOW_PERSIST was present in
	// the environment. Needed because false is both "operator said no" and
	// "operator said nothing".
	ShadowPersistSet bool
	// KeySchemaMode selects the Redis key grammar(s) the store maintains
	// (doc 14 §3): legacy (default), dual or canonical. It is boot-only
	// via URSM_V2_KEY_SCHEMA_MODE, independent of Mode above, and any
	// transition must close the recovery ready gate first. An invalid
	// value fails Validate instead of silently booting legacy.
	KeySchemaMode store.KeySchemaMode
}

func (c Config) ResolvePersistEnabled() bool {
	if c.Mode != api.ModeShadow {
		// Outside shadow mode the double-write flag is not the gate
		// (authoritative and canary always persist).
		return c.Mode != api.ModeOff
	}
	if c.ShadowPersistSet {
		return c.ShadowPersist
	}
	return c.ShadowDoubleWrite
}

func DefaultConfig() Config {
	return Config{
		Mode:                api.ModeAuthoritative,
		CanaryPercent:       0,
		ShadowSampleRate:    0.01,
		RecordTimeoutMs:     20,
		RecoveryBlockOnMiss: true,
		PersistIntervalSec:  60,
		RedisKeyPrefix:      "ursm:v2:",
		// -1 = share the gateway client (today's behavior, unchanged)
		RedisDB:      -1,
		Window1mTTL:  90 * time.Second,
		Window5mTTL:  6 * time.Minute,
		Window30mTTL: 35 * time.Minute,
		// 会话优化 v4 T5 (2026-08-18): NodeTTL 60min → 15min, paired with
		// CoolSeconds 120 → 30 and BackoffCapSeconds 3600(hard-coded) → 1800.
		// Spec §5 参数总表 / §14.5 “快恢复” — degraded nodes return to the
		// pool within 15min of the last probe/record touch. Hot-configurable
		// via settings_kv (see LoadHot).
		NodeTTL:        15 * time.Minute,
		ScoringWeights: DefaultScoringWeights(),
		// 2026-07-27 (M2): process LRU mirror defaults (spec Decision 2).
		LRUMirrorSize:    100000,
		LRUMirrorSoftTTL: 30 * time.Second,
		// 2026-08-18 (会话优化 v4 T5): 30s fast-recovery cooldown (was 120s
		// since 2026-07-24). Unstable small providers cool briefly and are
		// re-admitted after one success; repeated failures still escalate
		// exponentially up to BackoffCapSeconds.
		CoolSeconds: 30,
		// 2026-08-18 (会话优化 v4 T5 / P1-6): exponential-backoff cap, was
		// hard-coded 3600 in record_request.lua:183-184. Parameterized and
		// lowered to the spec target 1800s (30min).
		BackoffCapSeconds: 1800,
		// §14.3 target gear: mirror never bypasses a dead Redis. The grace
		// gear is opt-in per deployment via settings_kv.
		MirrorGraceEnabled: false,
		// 2026-09-04 availability gear: a hard Redis outage may serve
		// soft-expired mirror entries read-only for up to this window
		// instead of 503-ing every request. Sized well past a sentinel
		// failover; 0 opts back into strict fail-closed.
		OutageGrace: 30 * time.Minute,
		// P0-3: shadow double-write is opt-in. Operators must explicitly
		// flip URSM_V2_SHADOW_DOUBLE_WRITE=1 for the cutover comparison
		// window. Default off keeps v2 Redis namespace clean.
		ShadowDoubleWrite: false,
	}
}

// Hot-config keys (settings_kv, polled by hotconfig.Config). Values are
// platform-scoped (all instances) and take effect on the next read — the
// Manager consults them per call via effectiveConfig(), following the
// requestjourney/config.go live-source pattern (no second config channel).
const (
	HotKeyCoolSeconds       = "llmgw_ursm_cool_seconds"
	HotKeyNodeTTLSeconds    = "llmgw_ursm_node_ttl_seconds"
	HotKeyBackoffCapSeconds = "llmgw_ursm_backoff_cap_seconds"
	HotKeyMirrorGrace       = "llmgw_ursm_mirror_grace_enabled"
)

// HotConfigSource is the live settings_kv read surface the v2 facade needs.
// *hotconfig.Config satisfies it; the interface stays local so this domain
// does not import hotconfig (mirrors requestjourney.IntConfigSource).
type HotConfigSource interface {
	GetInt(key string, defaultValue int) int
	GetBool(key string, defaultValue bool) bool
}

// LoadHot layers live settings_kv values over the boot config and returns
// the merged copy (会话优化 v4 T5 / P1-6: “URSM 快恢复参数热更新”). Missing,
// zero, or negative values keep the boot value for each key independently,
// so a partial settings_kv row can never zero out a safety parameter.
func LoadHot(c Config, src HotConfigSource) Config {
	if src == nil {
		return c
	}
	if v := src.GetInt(HotKeyCoolSeconds, 0); v > 0 {
		c.CoolSeconds = v
	}
	if v := src.GetInt(HotKeyNodeTTLSeconds, 0); v > 0 {
		c.NodeTTL = time.Duration(v) * time.Second
	}
	if v := src.GetInt(HotKeyBackoffCapSeconds, 0); v > 0 {
		c.BackoffCapSeconds = v
	}
	c.MirrorGraceEnabled = src.GetBool(HotKeyMirrorGrace, c.MirrorGraceEnabled)
	return c
}

func LoadFromEnv() Config {
	c := DefaultConfig()
	if v := strings.TrimSpace(os.Getenv("URSM_V2_MODE")); v != "" {
		c.Mode = api.RolloutMode(v)
	}
	if v := strings.TrimSpace(os.Getenv("URSM_V2_CANARY_PERCENT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 100 {
			c.loadErr = fmt.Errorf("URSM_V2_CANARY_PERCENT must be an integer from 0 to 100")
		} else {
			c.CanaryPercent = n
		}
	}
	c.CanaryTenants = parseCSV(os.Getenv("URSM_V2_CANARY_TENANTS"))
	c.CanaryCredentials, c.loadErr = parsePositiveIntCSV(os.Getenv("URSM_V2_CANARY_CREDENTIALS"), c.loadErr)
	c.CanaryModels = parseCSV(os.Getenv("URSM_V2_CANARY_MODELS"))
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("URSM_V2_STRICT_CANARY"))); v == "1" || v == "true" || v == "yes" {
		c.StrictCanary = true
	}
	if v := strings.TrimSpace(os.Getenv("URSM_V2_REDIS_KEY_PREFIX")); v != "" {
		c.RedisKeyPrefix = v
	}
	// 2026-10-03: URSM 专用 db 号。留空 = 沿用网关的（行为不变）。
	// 显式设置且与网关不同时，main.go 会为 URSM 单独建一个 client。
	if v := strings.TrimSpace(os.Getenv("URSM_V2_REDIS_DB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 15 {
			c.RedisDB = n
		} else {
			c.loadErr = fmt.Errorf("URSM_V2_REDIS_DB must be an integer from 0 to 15")
		}
	}
	if v := os.Getenv("URSM_V2_SHADOW_SAMPLE_RATE"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n <= 1 {
			c.ShadowSampleRate = n
		}
	}
	// 2026-07-24: 支持通过环境变量配置 URSM v2 冷却时间
	if v := os.Getenv("URSM_V2_COOL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.CoolSeconds = n
		}
	}
	// 2026-07-27 (M2): LRU mirror tuning (spec Decision 6). URSM_V2_LRU_SIZE
	// accepts 1000..500000; 0 explicitly disables the mirror.
	if v := os.Getenv("URSM_V2_LRU_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.LRUMirrorSize = n
		}
	}
	if v := os.Getenv("URSM_V2_LRU_SOFT_TTL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LRUMirrorSoftTTL = time.Duration(n) * time.Millisecond
		}
	}
	// P0-3: shadow double-write env. Truthy (1/true/yes, case-insensitive)
	// turns on sidecar writes in ModeShadow. Operators MUST also leave
	// URSM_V2_MODE=shadow (NOT authoritative) so routing stays on legacy.
	// Invalid values are ignored; shadow double-write remains disabled unless explicitly enabled.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("URSM_V2_SHADOW_DOUBLE_WRITE"))); v != "" {
		if v == "1" || v == "true" || v == "yes" {
			c.ShadowDoubleWrite = true
		}
	}
	// 2026-10-04: shadow snapshot writer, decoupled from the sidecar switch
	// (see Config.ShadowPersist). Only "0"/"false"/"no" turns it OFF; anything
	// else is treated as "not stated" and leaves ResolvePersistEnabled
	// following ShadowDoubleWrite. An operator therefore has to *spell out* a
	// negative to change behaviour — a typo cannot silently stop snapshots.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("URSM_V2_SHADOW_PERSIST"))); v != "" {
		c.ShadowPersistSet = true
		c.ShadowPersist = !(v == "0" || v == "false" || v == "no")
	}
	// 2026-09-04 availability gear: URSM_V2_OUTAGE_GRACE_SECONDS bounds the
	// read-only mirror-serving window during a hard Redis outage. 0 or
	// negative disables the gear (strict fail-closed); the value is capped
	// at 24h so a typo cannot pin stale routing state forever.
	if v := os.Getenv("URSM_V2_OUTAGE_GRACE_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.loadErr = fmt.Errorf("URSM_V2_OUTAGE_GRACE_SECONDS must be an integer: %w", err)
		} else if n <= 0 {
			c.OutageGrace = 0
		} else if n > 86400 {
			c.OutageGrace = 24 * time.Hour
		} else {
			c.OutageGrace = time.Duration(n) * time.Second
		}
	}
	// Boot-only key schema mode (doc 14 §3). Fail closed on a typo: the
	// migration must never silently boot the wrong grammar.
	if v := strings.TrimSpace(os.Getenv("URSM_V2_KEY_SCHEMA_MODE")); v != "" {
		mode, err := store.ParseKeySchemaMode(v)
		if err != nil {
			c.loadErr = err
		} else {
			c.KeySchemaMode = mode
		}
	}
	return c
}

func (c Config) Validate() error {
	if c.loadErr != nil {
		return c.loadErr
	}
	switch c.Mode {
	case api.ModeOff, api.ModeShadow, api.ModeCanary, api.ModeAuthoritative:
	default:
		return fmt.Errorf("URSM_V2_MODE must be off, shadow, canary, or authoritative (got %q)", c.Mode)
	}
	if c.CanaryPercent < 0 || c.CanaryPercent > 100 {
		return fmt.Errorf("URSM_V2_CANARY_PERCENT must be an integer from 0 to 100")
	}
	if err := validateStrictCanaryScope(c); err != nil {
		return err
	}
	return nil
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func parsePositiveIntCSV(raw string, prior error) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, prior
	}
	values := make([]int, 0)
	seen := make(map[int]struct{})
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, fmt.Errorf("URSM_V2_CANARY_CREDENTIALS must contain positive integer IDs")
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("URSM_V2_CANARY_CREDENTIALS must contain positive integer IDs")
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		values = append(values, n)
	}
	return values, prior
}
