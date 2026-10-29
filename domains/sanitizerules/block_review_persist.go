package sanitizerules

// 拦截审核与放行的持久化层（2026-10-29 拦截持久化轮）。
//
// ## 为什么需要
//
// 第 33 增量的审核队列与放行表都是**进程内**的：重启即清空。用户侧症状是
// 「同一个误报反复打断同一个用户」——审核批准的放行活不过一次部署；触发
// 记录也无法回溯。owner 口径（本需求原话）：「一旦审批通过，对于同一客户端
// 的一周内的同样的规则将同样放行」⇒ 放行必须**跨重启存活** ⇒ 落库。
//
// ## 语义（本轮定死）
//
//   - **审批 ⇒ 放行是规则级 + 7 天**：批准一条审核单 = 该租户在该规则下
//     放行 7 天（expires_at = 批准时刻 + 168h）。不带载荷摘要——用户要的
//     是「同样的规则放行」，不是「同一次载荷放行」；7 天到期自动失效
//     （EgressReleaseAllowed 已跳过过期行），要提前收回就删放行记录。
//     客户端维度：记录里的 client_type 供展示与追溯；放行键取**租户**——
//     租户是本网关的客户端边界，检测热路径（egressReleaseAllowed）只有
//     租户上下文，按 client 收窄需要穿透整条检测链（扩大失败面，本轮不做）。
//   - **记录里没有载荷原文**：match_detail / reason 是说明文字，payload
//     只有不可逆摘要（迁移 917 的列约束 + 契约测试钉住）。
//   - **硬阻断清单的管理面状态落库**：后台保存/开关的清单写入
//     sanitize_hardblock_state，启动时装载覆盖 env（env 退化为「没有管理面
//     覆盖时的出厂默认」）。多实例部署下 DB 状态在**启动时**应用；运行中的
//     其它实例不自动跟随（211 是单实例部署；多实例同步留 TODO）。
//
// ## 装载
//
// cmd/gateway 在 installSanitizeRuleManager 里调 InstallReviewPersistence：
// 启动即从 DB 装载放行表与硬阻断状态（失败只告警不阻断启动——放行缺失的
// 后果是「误报复发」，比「网关起不来」轻）。未安装（测试 / 无 DB）时
// 队列与清单保持进程内行为，全部既有判据不变。

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// BlockReview 审核记录新增的三个展示/追溯字段（持久化轮）。
// ClientType 是拦截现场的客户端类型（ZCode / Claude Code…，extractClientType
// 的输出）；RuleName 是规则的展示名（builtin 走固定说明表，托管规则留空由
// 前端按 rule_id 关联规则配置）；MatchDetail 是给审核员看的匹配说明。
const (
	blockReviewReleaseTTL = 7 * 24 * time.Hour
)

// ReviewPersistence 是审核队列 / 放行表 / 硬阻断状态的落库通道。
// PostgresStore 实现（见 postgres_block_review.go）；实现必须全部幂等且
// 不返回载荷原文。
type ReviewPersistence interface {
	// FindPendingBlockReview 按去重键找既有 pending 单（跨重启去重）。
	FindPendingBlockReview(ctx context.Context, tenantID, scope, rule, digest, requestID string) (BlockReview, bool, error)
	// InsertBlockReview 落一条新记录（返回带 ID 的行）。
	InsertBlockReview(ctx context.Context, r BlockReview) (BlockReview, error)
	// ListBlockReviewsDB 按（状态可选、租户可选）倒序列出。
	ListBlockReviewsDB(ctx context.Context, status, tenantID string, limit int) ([]BlockReview, error)
	// GetBlockReviewDB 按 ID 取一条。
	GetBlockReviewDB(ctx context.Context, id int64) (BlockReview, error)
	// DecideBlockReviewDB 置状态与审核人；返回更新后的行。
	DecideBlockReviewDB(ctx context.Context, id int64, status, decidedBy string, decidedAt time.Time) (BlockReview, error)
	// 放行表落库（与进程内表同键：tenant+scope+rule+digest 唯一，同键覆盖）。
	UpsertEgressReleaseDB(ctx context.Context, r EgressRelease) error
	// DeleteEgressReleaseDB 删除放行（含按 digest 前缀语义：digest 为空删
	// 该键下全部摘要粒度的行），返回是否命中。
	DeleteEgressReleaseDB(ctx context.Context, tenantID, scope, rule, digest string) (bool, error)
	// LoadEgressReleasesDB 启动装载全部未过期放行。
	LoadEgressReleasesDB(ctx context.Context) ([]EgressRelease, error)
	// 硬阻断清单管理面状态（单例行）。
	LoadHardBlockState(ctx context.Context) ([]HardBlockAllow, bool, error)
	SaveHardBlockState(ctx context.Context, allows []HardBlockAllow, updatedBy string) error
}

var reviewPersistence atomic.Pointer[ReviewPersistence]

// InstallReviewPersistence 安装落库通道并做启动装载：
// 放行表 → 进程内 atomic 表；硬阻断状态（若存在）→ 覆盖当前清单。
// 装载失败只告警：放行缺失的后果是「误报复发」，比「网关起不来」轻。
func InstallReviewPersistence(p ReviewPersistence) {
	if p == nil {
		return
	}
	reviewPersistence.Store(&p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if releases, err := p.LoadEgressReleasesDB(ctx); err != nil {
		slog.Warn("sanitize egress release boot load failed; releases fall back to empty",
			"error", err)
	} else {
		SetEgressReleases(releases)
		slog.Info("sanitize egress releases loaded from database", "count", len(releases))
	}
	if allows, found, err := p.LoadHardBlockState(ctx); err != nil {
		slog.Warn("sanitize hardblock state boot load failed; keeping env/default list",
			"error", err)
	} else if found {
		SetHardBlockAllows(allows)
		items := make([]string, 0, len(allows))
		for _, a := range allows {
			items = append(items, a.Scope+":"+a.Rule)
		}
		slog.Info("sanitize hardblock allowlist loaded from managed state (overrides env)",
			"allows", items)
	}
}

// ResetReviewPersistence 撤下落库通道（测试用）；进程内行为不变。
func ResetReviewPersistence() {
	reviewPersistence.Store(nil)
}

// reviewPersistenceInstalled 返回当前通道（nil = 未安装）。
func reviewPersistenceInstalled() ReviewPersistence {
	if p := reviewPersistence.Load(); p != nil {
		return *p
	}
	return nil
}

// AddEgressReleasePersistent 写穿透的放行新增：DB + 进程内表。
// DB 失败时进程内仍然生效（当前进程的拦截不反弹），但重启会丢——记 WARN。
func AddEgressReleasePersistent(ctx context.Context, release EgressRelease) error {
	if err := AddEgressRelease(release); err != nil {
		return err
	}
	if p := reviewPersistenceInstalled(); p != nil {
		if err := p.UpsertEgressReleaseDB(ctx, release); err != nil {
			slog.Warn("sanitize egress release persist failed; in-process release still active",
				"error", err, "tenant", release.TenantID, "rule", release.Rule)
		}
	}
	return nil
}

// RemoveEgressReleasePersistent 写穿透的放行撤销。命中任一层即视为命中。
func RemoveEgressReleasePersistent(ctx context.Context, tenantID, scope, rule, digest string) bool {
	removed := RemoveEgressRelease(tenantID, scope, rule, digest)
	if p := reviewPersistenceInstalled(); p != nil {
		if dbRemoved, err := p.DeleteEgressReleaseDB(ctx, tenantID, scope, rule, digest); err != nil {
			slog.Warn("sanitize egress release revoke persist failed",
				"error", err, "tenant", tenantID, "rule", rule)
		} else {
			removed = removed || dbRemoved
		}
	}
	return removed
}

// SaveHardBlockStatePersistent 保存硬阻断清单的管理面状态（DB + 进程内）。
// DB 失败时进程内生效、重启回落 env——记 WARN，不阻断管理操作。
func SaveHardBlockStatePersistent(ctx context.Context, allows []HardBlockAllow, updatedBy string) {
	SetHardBlockAllows(allows)
	if p := reviewPersistenceInstalled(); p != nil {
		if err := p.SaveHardBlockState(ctx, allows, updatedBy); err != nil {
			slog.Warn("sanitize hardblock state persist failed; in-process list still active",
				"error", err)
		}
	}
}
