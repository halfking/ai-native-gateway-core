package reqprobe

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryStore 是 lite 部署模式的进程内实现：重启清零，上限
// MemoryMaxRecords 条指纹按插入序 FIFO 淘汰。同机多副本（lite 通常单
// 副本）各自记账。
type MemoryStore struct {
	mu    sync.Mutex
	seq   int64
	byFP  map[string]*list.Element
	order *list.List // front=oldest，仅用于 FIFO 淘汰
	nowFn func() time.Time
}

type memEntry struct {
	rec Record
}

// NewMemoryStore 创建内存实现。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byFP:  map[string]*list.Element{},
		order: list.New(),
		nowFn: time.Now,
	}
}

func (m *MemoryStore) Upsert(_ context.Context, rec Record) (Record, error) {
	now := m.nowFn()
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
		// 一次事件 = 1（直调方零值归一，合并 = 旧值 + 1）。
		rec.Occurrences = 1
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.byFP[rec.Fingerprint]; ok {
		existing := el.Value.(*memEntry)
		e := &existing.rec
		e.Occurrences += rec.Occurrences
		e.RecoveredCount += rec.RecoveredCount
		e.LastSeen = rec.LastSeen
		if rec.LastRequestID != "" {
			e.LastRequestID = rec.LastRequestID
		}
		if rec.ErrorSample != "" {
			e.ErrorSample = rec.ErrorSample
		}
		return *e, nil
	}
	m.seq++
	rec.ID = m.seq
	rec.Occurrences = max(rec.Occurrences, 1)
	el := m.order.PushBack(&memEntry{rec: rec})
	m.byFP[rec.Fingerprint] = el
	for m.order.Len() > MemoryMaxRecords {
		front := m.order.Front()
		if front == nil {
			break
		}
		delete(m.byFP, front.Value.(*memEntry).rec.Fingerprint)
		m.order.Remove(front)
	}
	return rec, nil
}

func (m *MemoryStore) List(_ context.Context, f Filter) ([]Record, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []Record
	for el := m.order.Back(); el != nil; el = el.Prev() { // 新→旧
		rec := el.Value.(*memEntry).rec
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

func (m *MemoryStore) Counts(_ context.Context) (Counts, error) {
	today := Today(m.nowFn())
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	for el := m.order.Front(); el != nil; el = el.Next() {
		rec := el.Value.(*memEntry).rec
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

func (m *MemoryStore) Resolve(_ context.Context, ids []int64, notes string) (int, error) {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	now := m.nowFn()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for el := m.order.Front(); el != nil; el = el.Next() {
		entry := el.Value.(*memEntry)
		if want[entry.rec.ID] && !entry.rec.Resolved {
			entry.rec.Resolved = true
			entry.rec.ResolvedAt = &now
			entry.rec.ResolutionNotes = notes
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) ResolveFilter(_ context.Context, f Filter, notes string) (int, error) {
	now := m.nowFn()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for el := m.order.Front(); el != nil; el = el.Next() {
		entry := el.Value.(*memEntry)
		if entry.rec.Resolved {
			continue
		}
		if f.matches(entry.rec) {
			entry.rec.Resolved = true
			entry.rec.ResolvedAt = &now
			entry.rec.ResolutionNotes = notes
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) LearnedParams(_ context.Context, providerCode, outboundModel string) []string {
	cutoff := m.nowFn().Add(-learnedTTL)
	providerCode = strings.ToLower(providerCode)
	outboundModel = strings.ToLower(outboundModel)
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for el := m.order.Front(); el != nil; el = el.Next() {
		rec := el.Value.(*memEntry).rec
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

func (m *MemoryStore) Close() error { return nil }
