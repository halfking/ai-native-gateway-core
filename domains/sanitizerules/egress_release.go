package sanitizerules

// r1039（D4，2026-10-20）：egress 拦截的**放行**路径。
//
// ## owner 口径
//
// 「对于数据外泄的阻断需要再次审计，**不要放大范围**……在被阻断时，
// **需要给客户端提示，由用户来选择是否执行**。」
//
// ⇒ 拦截必须**可放行**：被拦的那一刻客户端已经知道「因为什么规则、哪个参数」
// （见 domains/streaming 的 writeOutputPolicyBlockedJSON），运维/管理面则能
// 对**某个租户**放开**某条规则**。
//
// ## 为什么放行决定必须在服务端
//
// 放行绝不能由客户端自带的头/参数触发——那正是 hop-header 豁免翻车过的形态
// （见 security/sanitize/hop.go 与 AGENTS.md 里的「hop-header 豁免教训」）。
// ⇒ 本表只存在于**网关进程内**，由管理面写入；客户端读不到、也写不了。
//
// 持久化说明：当前实现是**进程内**的（与硬阻断清单同一层：环境变量 → 进程
// 级清单）。跨重启/多副本的持久化需要管理面与存储层（不在本轮范围内），
// 详见交付说明里的「下一步」。
//
// ## 与硬阻断清单的方向差别（容易读反）
//
//	HardBlockAllow：哪些规则**允许**终止回合（收窄面）
//	EgressRelease： 哪些 (租户, 规则) **不再**被拦（放行面）
//
// 两者都是服务端持有的显式清单，互不替代。

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// EgressRelease 是一次「放行」记录：某租户在某条规则下被允许继续。
type EgressRelease struct {
	// TenantID 是放行生效的租户。**不允许**空值：没有租户就没有边界，
	// 那等于全网放行。
	TenantID string `json:"tenant_id"`
	// Scope 目前只支持 "egress"（外传方向）。
	Scope string `json:"scope"`
	// Rule 是被放行的规则 ID（精确匹配，不接受通配/前缀族）。
	Rule string `json:"rule"`
	// PayloadDigest 可选：把放行绑定到**这一次**的具体载荷
	// （见 security/sanitize.EgressPayloadDigest）。为空 = 该租户在该规则下
	// 一律放行。
	PayloadDigest string `json:"payload_digest,omitempty"`
	// ApprovedBy 记录放行人（审计用）。
	ApprovedBy string `json:"approved_by,omitempty"`
	// ExpiresAt 为零表示不过期；非零且已过期则该记录不再生效。
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// egressReleases 是进程级放行表。原子替换，读侧无锁。
var egressReleases atomic.Pointer[[]EgressRelease]

// egressReleasesMu 只护写侧（增删）。
var egressReleasesMu sync.Mutex

// EgressReleases 返回当前放行表（副本）。
func EgressReleases() []EgressRelease {
	src := egressReleases.Load()
	if src == nil {
		return nil
	}
	out := make([]EgressRelease, len(*src))
	copy(out, *src)
	return out
}

// SetEgressReleases 覆盖放行表。空切片表示「没有任何放行」。
func SetEgressReleases(releases []EgressRelease) {
	egressReleases.Store(&releases)
}

// ValidateEgressRelease 校验一条放行记录。
func ValidateEgressRelease(release EgressRelease) error {
	if release.TenantID == "" {
		return fmt.Errorf("egress release: tenant_id is required (an empty tenant would release every tenant)")
	}
	if release.Scope != "egress" {
		return fmt.Errorf("egress release: unsupported scope %q", release.Scope)
	}
	if release.Rule == "" {
		return fmt.Errorf("egress release: rule is required")
	}
	if !release.ExpiresAt.IsZero() && !release.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("egress release: expires_at is already in the past")
	}
	return nil
}

// AddEgressRelease 追加一条放行记录（同键覆盖）。
//
// 这是**服务端**入口：管理面/运维代码调用它，客户端无法触及。
func AddEgressRelease(release EgressRelease) error {
	if err := ValidateEgressRelease(release); err != nil {
		return err
	}
	egressReleasesMu.Lock()
	defer egressReleasesMu.Unlock()
	current := EgressReleases()
	out := make([]EgressRelease, 0, len(current)+1)
	for _, r := range current {
		if r.TenantID == release.TenantID && r.Scope == release.Scope &&
			r.Rule == release.Rule && r.PayloadDigest == release.PayloadDigest {
			continue
		}
		out = append(out, r)
	}
	out = append(out, release)
	SetEgressReleases(out)
	return nil
}

// RemoveEgressRelease 删除一条放行记录，返回是否命中。
func RemoveEgressRelease(tenantID, scope, rule, payloadDigest string) bool {
	egressReleasesMu.Lock()
	defer egressReleasesMu.Unlock()
	current := EgressReleases()
	out := make([]EgressRelease, 0, len(current))
	removed := false
	for _, r := range current {
		if r.TenantID == tenantID && r.Scope == scope && r.Rule == rule && r.PayloadDigest == payloadDigest {
			removed = true
			continue
		}
		out = append(out, r)
	}
	SetEgressReleases(out)
	return removed
}

// EgressReleaseAllowed 报告 (租户, scope, 规则, 载荷摘要) 是否被放行。
//
// 口径：
//   - 租户必须**精确**匹配：没有租户 ⇒ 不放行（fail-closed）。
//   - 规则精确匹配，不接受通配。
//   - 放行记录带载荷摘要时，摘要必须相等（放行只对这一次载荷有效）。
//   - 过期记录不生效。
func EgressReleaseAllowed(tenantID, scope, rule, payloadDigest string) bool {
	if tenantID == "" || rule == "" {
		return false
	}
	src := egressReleases.Load()
	if src == nil {
		return false
	}
	now := time.Now()
	for _, r := range *src {
		if r.TenantID != tenantID || r.Scope != scope || r.Rule != rule {
			continue
		}
		if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			continue
		}
		if r.PayloadDigest != "" && r.PayloadDigest != payloadDigest {
			continue
		}
		return true
	}
	return false
}
