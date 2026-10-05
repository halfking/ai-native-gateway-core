package apihub

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ── §10.30 补门：批量路径的**部分失败**语义（2026-10-05 审计）────────────
//
// 背景：cd6c89145 把 watcher 从逐行 Register 改成 RegisterBatch。
// pgStore.UpsertBatch 在分片失败时会**退回逐行**并把坏行隔离掉，
// 于是「整批返回 error」时，**库里其实已经写进去绝大多数行了**。
//
// 而 RegisterBatch 原来是：
//
//	if err := s.store.UpsertBatch(ctx, prepared); err != nil {
//	    return err            // ← 提前返回，prepared 里的缓存一条都没失效
//	}
//	for _, a := range prepared { s.cache.invalidate(...) }
//
// ⇒ 失败路径下留下 N 条**陈旧缓存**（TTL 60s）。
//
// 为什么这在改动前不存在：逐行 Register 时代，watcher 对每个资产
// 单独调 Register，每个 Register 成功后**自己**失效那一条缓存。
// 批量化把「写成功」与「失效缓存」拆到了两个循环，中间夹了一个
// `return err` ⇒ 失败时两者不再配对。
//
// 症状是**静默**的：DB 里是新值，Get 命中缓存返回旧值，60s 后自愈。
// 读方（admin/资产列表）会看到「改了名字但列表没变」。

// partialFailStore 模拟 pgStore 的分片失败退路：
// **把资产都写进去**，但返回一个 error（因为确实有行写失败）。
var errChunkFailed = errors.New("apihub: simulated chunk failure")

type partialFailStore struct {
	*memStore
	err error
}

func (p *partialFailStore) UpsertBatch(ctx context.Context, assets []Asset) error {
	// 关键：先写入（与 pgStore 退回逐行后的实际效果一致），再返回错误
	_ = p.memStore.UpsertBatch(ctx, assets)
	return p.err
}

// 门：UpsertBatch 返回错误时，已写入的那些行**仍必须**逐条失效缓存。
func TestRegisterBatchInvalidatesCacheEvenWhenStoreReportsFailure(t *testing.T) {
	base := newMemStore()
	store := &partialFailStore{memStore: base, err: errChunkFailed}
	svc := New(store)

	ctx := context.Background()
	assets := []Asset{
		{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "a"},
		{Kind: KindLLMEndpoint, RefID: 2, TenantID: "t1", Name: "b"},
	}
	if err := svc.RegisterBatch(ctx, assets); err == nil {
		t.Fatal("期望 RegisterBatch 返回错误（store 明确报失败），实际为 nil")
	}

	// 先把缓存填成**陈旧**内容，模拟「上一轮 tick 读过一次」
	for _, a := range assets {
		stale := a
		stale.Name = "stale-name"
		svc.cache.put(stale)
	}

	// 再跑一次失败批次：库里会写成 "a"/"b"，但缓存若是陈旧的，
	// Get 就会返回 "stale-name"
	if err := svc.RegisterBatch(ctx, assets); err == nil {
		t.Fatal("期望 RegisterBatch 返回错误")
	}

	for _, a := range assets {
		got, ok := svc.cache.get(a.TenantID, a.Kind, a.RefID)
		if ok {
			t.Errorf("ref_id=%d 的缓存未失效，仍返回 %q（DB 里已是 %q）",
				a.RefID, got.Name, a.Name)
		}
	}
}

// 对照组：批次**成功**时缓存必须失效（证明上一条不是恒真）
func TestRegisterBatchInvalidatesCacheOnSuccess(t *testing.T) {
	m := newMemStore()
	svc := New(m)
	ctx := context.Background()
	a := Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "a"}

	if err := svc.RegisterBatch(ctx, []Asset{a}); err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	svc.cache.put(Asset{Kind: a.Kind, RefID: a.RefID, TenantID: a.TenantID, Name: "stale"})

	if err := svc.RegisterBatch(ctx, []Asset{a}); err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if got, ok := svc.cache.get(a.TenantID, a.Kind, a.RefID); ok {
		t.Errorf("成功路径下缓存未失效，仍返回 %q", got.Name)
	}
}

// 门：RegisterBatch 失败后，读方必须能**立刻**看到库里的新值
// （即失败不得把读方卡在陈旧值上超过一个 TTL）
func TestRegisterBatchFailureDoesNotStaleReads(t *testing.T) {
	base := newMemStore()
	store := &partialFailStore{memStore: base, err: errChunkFailed}
	svc := New(store, WithCacheTTL(time.Hour)) // TTL 拉长：若失效逻辑缺失，症状被放大到 1 小时

	ctx := context.Background()
	a := Asset{Kind: KindLLMEndpoint, RefID: 7, TenantID: "t1", Name: "new-name"}

	// 第一轮：写进库，同时缓存被填成旧值
	if err := svc.RegisterBatch(ctx, []Asset{a}); err == nil {
		t.Fatal("期望错误")
	}
	svc.cache.put(Asset{Kind: a.Kind, RefID: a.RefID, TenantID: a.TenantID, Name: "old-name"})

	// 第二轮：库里写 "new-name"，但返回错误
	if err := svc.RegisterBatch(ctx, []Asset{a}); err == nil {
		t.Fatal("期望错误")
	}

	got, err := svc.Get(WithTenant(ctx, a.TenantID), a.Kind, a.RefID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "new-name" {
		t.Errorf("Get 返回 %q，库里是 %q —— 失败路径留下了陈旧缓存（TTL 1 小时内读方都看到旧值）",
			got.Name, "new-name")
	}
}
