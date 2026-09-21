package reqprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore 是 Full 部署模式的实现：单 hash 存全量记录（field=指纹），
// zset 按 LastSeen 排序，30 天惰性清理。记录量级是"每天每类问题一行"，
// 远小于任何 Redis 扫描压力，因此 List/Counts 直接全量读回内存过滤。
type RedisStore struct {
	rdb       redis.UniversalClient
	prefix    string
	nowFn     func() time.Time
	retention time.Duration
}

// NewRedisStore 创建 Redis 实现。prefix 形如 "llmgw:reqanom"。
func NewRedisStore(rdb redis.UniversalClient, prefix string) *RedisStore {
	if prefix == "" {
		prefix = "llmgw:reqanom"
	}
	return &RedisStore{
		rdb:       rdb,
		prefix:    prefix,
		nowFn:     time.Now,
		retention: 30 * 24 * time.Hour,
	}
}

func (s *RedisStore) recordsKey() string { return s.prefix + ":records" }
func (s *RedisStore) indexKey() string   { return s.prefix + ":idx" }
func (s *RedisStore) seqKey() string     { return s.prefix + ":seq" }

func (s *RedisStore) Upsert(ctx context.Context, rec Record) (Record, error) {
	if s.rdb == nil {
		return rec, nil
	}
	now := s.nowFn()
	if rec.FirstSeen.IsZero() {
		rec.FirstSeen = now
	}
	if rec.LastSeen.IsZero() {
		rec.LastSeen = now
	}
	if rec.Day == "" {
		rec.Day = Today(now)
	}
	rec.Fingerprint = Fingerprint(&rec)
	rec.ErrorSample = truncateSample(rec.ErrorSample)
	if rec.Occurrences < 1 {
		// 与 Coordinator.Record 的语义对齐：一次事件 = 1（直调方零值
		// 归一，合并 = 旧值 + 1）。
		rec.Occurrences = 1
	}

	// 乐观合并：读旧值 → 累加 → 写回。并发下偶发的计数丢失可接受
	// （观测数据，非计费）。
	raw, err := s.rdb.HGet(ctx, s.recordsKey(), rec.Fingerprint).Result()
	if err != nil && err != redis.Nil {
		return rec, fmt.Errorf("reqprobe redis hget: %w", err)
	}
	if err == nil {
		var old Record
		if json.Unmarshal([]byte(raw), &old) == nil {
			rec.ID = old.ID
			// Coordinator 每次事件记 Occurrences=1，合并语义 = 旧值+1。
			rec.Occurrences += old.Occurrences
			rec.RecoveredCount += old.RecoveredCount
			rec.FirstSeen = old.FirstSeen
			if old.Resolved && !rec.Resolved {
				// 已解决的指纹再次出现：重新翻开（未解决），运维需要
				// 看到回归的问题；解决备注保留供参考。
				rec.ResolutionNotes = old.ResolutionNotes
			}
		}
	} else {
		id, err := s.rdb.Incr(ctx, s.seqKey()).Result()
		if err != nil {
			return rec, fmt.Errorf("reqprobe redis incr: %w", err)
		}
		rec.ID = id
		if rec.Occurrences < 1 {
			rec.Occurrences = 1
		}
	}
	rec.Occurrences = max(rec.Occurrences, 1)

	blob, err := json.Marshal(rec)
	if err != nil {
		return rec, err
	}
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, s.recordsKey(), rec.Fingerprint, blob)
	pipe.ZAdd(ctx, s.indexKey(), redis.Z{Score: float64(rec.LastSeen.Unix()), Member: rec.Fingerprint})
	// 惰性清理：过期指纹从 hash 与 zset 一并移除。
	expired, err := s.rdb.ZRangeByScore(ctx, s.indexKey(), &redis.ZRangeBy{
		Max: fmt.Sprintf("%d", now.Add(-s.retention).Unix()),
	}).Result()
	if err == nil && len(expired) > 0 {
		pipe.ZRem(ctx, s.indexKey(), toAny(expired)...)
		pipe.HDel(ctx, s.recordsKey(), expired...)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return rec, fmt.Errorf("reqprobe redis exec: %w", err)
	}
	return rec, nil
}

func (s *RedisStore) loadAll(ctx context.Context) ([]Record, error) {
	if s.rdb == nil {
		return nil, nil
	}
	raw, err := s.rdb.HGetAll(ctx, s.recordsKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("reqprobe redis hgetall: %w", err)
	}
	out := make([]Record, 0, len(raw))
	for _, v := range raw {
		var rec Record
		if json.Unmarshal([]byte(v), &rec) == nil {
			out = append(out, rec)
		}
	}
	sortRecordsDesc(out)
	return out, nil
}

func (s *RedisStore) List(ctx context.Context, f Filter) ([]Record, int, error) {
	all, err := s.loadAll(ctx)
	if err != nil {
		return nil, 0, err
	}
	matched := make([]Record, 0, len(all))
	for _, rec := range all {
		if f.matches(rec) {
			matched = append(matched, rec)
		}
	}
	total := len(matched)
	if f.Offset > 0 && f.Offset < len(matched) {
		matched = matched[f.Offset:]
	} else if f.Offset >= len(matched) {
		matched = nil
	}
	if f.Limit > 0 && len(matched) > f.Limit {
		matched = matched[:f.Limit]
	}
	if matched == nil {
		matched = []Record{}
	}
	return matched, total, nil
}

func (s *RedisStore) Counts(ctx context.Context) (Counts, error) {
	all, err := s.loadAll(ctx)
	if err != nil {
		return Counts{}, err
	}
	today := Today(s.nowFn())
	var c Counts
	for _, rec := range all {
		if rec.Resolved {
			continue
		}
		c.Unresolved++
		if rec.Day == today {
			c.NewToday++
		}
	}
	return c, nil
}

func (s *RedisStore) Resolve(ctx context.Context, ids []int64, notes string) (int, error) {
	if s.rdb == nil || len(ids) == 0 {
		return 0, nil
	}
	all, err := s.loadAll(ctx)
	if err != nil {
		return 0, err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	now := s.nowFn()
	pipe := s.rdb.Pipeline()
	n := 0
	for _, rec := range all {
		if !want[rec.ID] || rec.Resolved {
			continue
		}
		rec.Resolved = true
		rec.ResolvedAt = &now
		rec.ResolutionNotes = notes
		blob, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		pipe.HSet(ctx, s.recordsKey(), rec.Fingerprint, blob)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("reqprobe redis resolve: %w", err)
	}
	return n, nil
}

func (s *RedisStore) ResolveFilter(ctx context.Context, f Filter, notes string) (int, error) {
	if s.rdb == nil {
		return 0, nil
	}
	all, err := s.loadAll(ctx)
	if err != nil {
		return 0, err
	}
	now := s.nowFn()
	pipe := s.rdb.Pipeline()
	n := 0
	for _, rec := range all {
		if rec.Resolved || !f.matches(rec) {
			continue
		}
		rec.Resolved = true
		rec.ResolvedAt = &now
		rec.ResolutionNotes = notes
		blob, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		pipe.HSet(ctx, s.recordsKey(), rec.Fingerprint, blob)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("reqprobe redis resolve-filter: %w", err)
	}
	return n, nil
}

func (s *RedisStore) LearnedParams(ctx context.Context, providerCode, outboundModel string) []string {
	if s.rdb == nil {
		return nil
	}
	all, err := s.loadAll(ctx)
	if err != nil {
		return nil
	}
	cutoff := s.nowFn().Add(-learnedTTL)
	providerCode = strings.ToLower(providerCode)
	outboundModel = strings.ToLower(outboundModel)
	set := map[string]bool{}
	for _, rec := range all {
		if rec.Trigger != TriggerParamRejected || rec.Resolved || rec.Param == "" {
			continue
		}
		if rec.RecoveredCount == 0 || rec.LastSeen.Before(cutoff) {
			continue
		}
		if strings.ToLower(rec.ProviderCode) != providerCode ||
			strings.ToLower(rec.OutboundModel) != outboundModel {
			continue
		}
		for _, p := range strings.Split(rec.Param, ",") {
			if p = strings.TrimSpace(p); p != "" {
				set[p] = true
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out
}

func (s *RedisStore) Close() error { return nil }

// sortRecordsDesc 按 LastSeen 倒序（简单插入排序即可，量级小）。
func sortRecordsDesc(recs []Record) {
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j].LastSeen.After(recs[j-1].LastSeen); j-- {
			recs[j], recs[j-1] = recs[j-1], recs[j]
		}
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// 编译期断言：两种实现都满足 Store。
var (
	_ Store = (*MemoryStore)(nil)
	_ Store = (*RedisStore)(nil)
)
