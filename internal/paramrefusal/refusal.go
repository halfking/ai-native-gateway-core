// Package paramrefusal implements P0-1（2026-10-14，
// docs/research/2026-10-14-magpie对比与优化方案.md）：「上游 400 指名可选
// 参数 → 剔除重试一次 → 按 (provider, model, wire) 记忆」的进程内记忆表。
//
// 设计边界（与既有 reqprobe 的分工）：
//   - reqprobe 的学习只在「剔除后重试成功」（RecoveredCount>0）后生效，且
//     其骨架保护集（skeletonParams）保护 stream_options 等字段——vapeur 三案
//     里 stream_options 一族只能靠静态 StripStreamOptions 逐腿修；
//   - 本包在**拒绝发生时**即记忆（Remember 在重试决策点调用，重试成败不
//     回滚——上游已经用 400 点名背书「该字段发不得」，重试失败说明还有
//     别的问题，不否定「这个字段该剔」）；
//   - 黑名单（model/messages/tools 等核心字段）优先于一切：核心字段被点名
//     也绝不剔除、绝不记忆、绝不重试。
//
// 进程内记忆：多实例各自学习（文档已认可），重启清零，24h TTL 滑动续期。
package paramrefusal

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// fieldTTL 是单条记忆的存活时长；同一字段再次被拒会刷新时间戳（滑动续期
// ——持续被拒的字段保持热记忆，静默字段自然过期）。
const fieldTTL = 24 * time.Hour

// sweepInterval 是整表清理的最小间隔：Remember 每次都做懒清理代价过高，
// 但也不等读者撞上过期条目才清（StrippableFields 读路径同样懒清）。
const sweepInterval = time.Hour

// maxFieldsPerKey 防御性封顶：Remember 只接受白名单字段（家族大小固定），
// 正常情况下到不了上限；防御「白名单将来被误扩」的记忆膨胀。
const maxFieldsPerKey = 32

// Store 是进程内记忆表。单 mutex 足够（读写都是微秒级、无 IO）。
type Store struct {
	mu         sync.Mutex
	fields     map[string]map[string]time.Time // key → field → rememberedAt
	lastSweep  time.Time
	nowFn      func() time.Time
	sweepCalls int
}

// NewStore 创建空记忆表。
func NewStore() *Store {
	now := time.Now
	return &Store{
		fields:    map[string]map[string]time.Time{},
		nowFn:     now,
		lastSweep: now(),
	}
}

// Default 是进程级共享实例：executor 未注入独立实例时使用（main.go 无需
// 接线；测试用注入实例隔离）。
func Default() *Store { return defaultStore }

var defaultStore = NewStore()

// now 供测试注入时钟（nowFn 由 NewStore 初始化为 time.Now）。
func (s *Store) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func memoryKey(providerID int, model, wire string) string {
	return fmt.Sprintf("%d|%s|%s", providerID,
		strings.ToLower(strings.TrimSpace(model)),
		strings.ToLower(strings.TrimSpace(wire)))
}

// Remember 把（已被拒绝的）字段记入 (providerID, model, wire) 键下。
// 只接受 Strippable 字段（黑名单/未登记字段静默丢弃——上层本就不该把
// 不可剔字段送进来，这里兜底）。时间戳刷新 = 滑动续期。
func (s *Store) Remember(providerID int, model, wire string, fields ...string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	k := memoryKey(providerID, model, wire)
	m := s.fields[k]
	if m == nil {
		m = map[string]time.Time{}
		s.fields[k] = m
	}
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if !Strippable(f) {
			continue
		}
		m[f] = now
	}
	// 防御性封顶：白名单外的字段已在上面的 Strippable 门被丢，这里只防
	// 家族表被误扩后的记忆膨胀（逐出最旧）。
	for len(m) > maxFieldsPerKey {
		oldest := ""
		var oldestAt time.Time
		for f, at := range m {
			if oldest == "" || at.Before(oldestAt) {
				oldest, oldestAt = f, at
			}
		}
		delete(m, oldest)
	}
}

// StrippableFields 返回 (providerID, model, wire) 键下仍在 TTL 内的全部
// 记忆字段（已按字母序稳定排序，便于测试断言与日志）。读路径懒清理过期
// 条目；键下无存活字段时连键一起删。
func (s *Store) StrippableFields(providerID int, model, wire string) []string {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memoryKey(providerID, model, wire)
	m, ok := s.fields[k]
	if !ok {
		return nil
	}
	var out []string
	for f, at := range m {
		if now.Sub(at) >= fieldTTL {
			delete(m, f)
			continue
		}
		out = append(out, f)
	}
	if len(m) == 0 {
		delete(s.fields, k)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// Len 返回仍有存活字段的 (provider, model, wire) 键数（测试用；含懒清理
// 效果——过期后首次 Len 即收缩）。
func (s *Store) Len() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	return len(s.fields)
}

// sweepLocked 整表清理（调用方持锁）。频率由 sweepInterval 封顶。
func (s *Store) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for k, m := range s.fields {
		for f, at := range m {
			if now.Sub(at) >= fieldTTL {
				delete(m, f)
			}
		}
		if len(m) == 0 {
			delete(s.fields, k)
		}
	}
}
