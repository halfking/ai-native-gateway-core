// Package nodestatecache 是 FR-10 定义的进程内派生读缓存：
// 全节点位图状态 + 每节点紧凑统计/资源仪表 + 节点选择闭包 + 需自检列表。
//
// 定位（R10.1）：本包是派生读模型，权威是 URSM v2 Redis。禁止双写——
// 本包永不写 URSM/DB。包不 import dispatch/executors（避免环）。
package nodestatecache

import (
	"container/list"
	"errors"
	"strconv"
	"sync"
)

// NodeRef 唯一标识一个「节点」：(tenant, credential, raw_model)。
// 对应 URSM 的节点维度（NodeKeyForTenant），也即注册表双射的一侧。
type NodeRef struct {
	TenantID     string
	CredentialID int64
	RawModel     string
}

// InvalidationReason 标识置脏来源。
type InvalidationReason uint8

const (
	// InvalidationPubSub 来自 NodeMirror pub/sub 失效广播（喂入路径②）。
	InvalidationPubSub InvalidationReason = 1
	// InvalidationEvicted 注册表容量满时的 LRU 回收。
	InvalidationEvicted InvalidationReason = 2
	// InvalidationReconcile 对账校正覆盖（喂入路径③）。
	InvalidationReconcile InvalidationReason = 3
)

// InvalidationListener 是 pub/sub 失效广播的接入点：注册表回收/置脏时回调。
// 包内不依赖 redis；集成者在此实现重拉（从 URSM/NodeMirror 重新装载权威值）。
type InvalidationListener interface {
	OnInvalidate(ref NodeRef, nodeID int32, reason InvalidationReason)
}

// ErrRegistryFull 注册表容量已满且无可回收的未活跃 id。
var ErrRegistryFull = errors.New("nodestatecache: registry full (no inactive id to evict)")

// maxModelIDs 模型索引容量上界（模型数量远小于节点数量）。
const maxModelIDs = 4096

// registry 维护 NodeRef ↔ int32 dense id 的双射，容量有界；
// 满时按 LRU 回收「未活跃」id（未活跃 = 不持有资源仪表占用，由
// isActive 回调判定，Cache 注入）。id 从 1 起，0 保留为无效值。
type registry struct {
	mu       sync.Mutex
	cap      int32
	nextID   int32
	freeIDs  []int32
	fwd      map[string]int32
	rev      []NodeRef // 索引即 dense id；0 号位保留
	lru      *list.List
	elem     map[int32]*list.Element
	models   map[string]int32
	nextMid  int32
	isActive func(id int32) bool
	onEvict  func(ref NodeRef, id int32)
}

func newRegistry(capacity int32, isActive func(id int32) bool) *registry {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &registry{
		cap:      capacity,
		fwd:      make(map[string]int32, 1024),
		rev:      make([]NodeRef, capacity+1),
		lru:      list.New(),
		elem:     make(map[int32]*list.Element, 1024),
		models:   make(map[string]int32, 128),
		isActive: isActive,
	}
}

// refKey 生成双射的规范 map key。用长度前缀避免分隔符歧义。
func refKey(r NodeRef) string {
	return strconv.Itoa(len(r.TenantID)) + ":" + r.TenantID +
		strconv.Itoa(len(r.RawModel)) + ":" + r.RawModel +
		strconv.FormatInt(r.CredentialID, 10)
}

// Register 返回 ref 的 dense id；不存在则分配（容量满时先回收 LRU 未活跃 id，
// 回收触发 onEvict 回调以清理位图/槽位并通知 InvalidationListener）。
// 第二个返回值表示是否为本次新分配。
func (r *registry) Register(ref NodeRef) (int32, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := refKey(ref)
	if id, ok := r.fwd[k]; ok {
		r.touchLocked(id)
		return id, false, nil
	}
	var id int32
	if n := len(r.freeIDs); n > 0 {
		id = r.freeIDs[n-1]
		r.freeIDs = r.freeIDs[:n-1]
	} else if r.nextID < r.cap {
		r.nextID++
		id = r.nextID
	} else {
		evicted, ok := r.evictInactiveLocked()
		if !ok {
			return 0, false, ErrRegistryFull
		}
		id = evicted
	}
	r.fwd[k] = id
	r.rev[id] = ref
	r.elem[id] = r.lru.PushFront(id) // front=MRU；Back() 为最久未触碰（LRU）
	return id, true, nil
}

// Get 返回 ref 的 dense id（不存在返回 0），并刷新 LRU。
func (r *registry) Get(ref NodeRef) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.fwd[refKey(ref)]
	if !ok {
		return 0
	}
	r.touchLocked(id)
	return id
}

// RefOf 返回 dense id 对应的 NodeRef。
func (r *registry) RefOf(id int32) (NodeRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id <= 0 || id >= int32(len(r.rev)) {
		return NodeRef{}, false
	}
	ref := r.rev[id]
	if ref.TenantID == "" && ref.CredentialID == 0 && ref.RawModel == "" {
		return NodeRef{}, false
	}
	return ref, true
}

// Len 当前注册节点数。
func (r *registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fwd)
}

// touchLocked 把 id 移到 LRU 头。调用方持锁。
func (r *registry) touchLocked(id int32) {
	if e, ok := r.elem[id]; ok {
		r.lru.MoveToFront(e)
	}
}

// evictInactiveLocked 从 LRU 尾部找第一个未活跃（不持有资源）的 id 回收。
// 回收后置脏回调由 onEvict 完成（清理位图/槽位）。调用方持锁。
func (r *registry) evictInactiveLocked() (int32, bool) {
	for e := r.lru.Back(); e != nil; e = e.Prev() {
		id := e.Value.(int32)
		if r.isActive != nil && r.isActive(id) {
			continue // 活跃（在途占用）不回收
		}
		ref := r.rev[id]
		k := refKey(ref)
		delete(r.fwd, k)
		r.rev[id] = NodeRef{}
		r.lru.Remove(e)
		delete(r.elem, id)
		if r.onEvict != nil {
			r.onEvict(ref, id) // 死锁注意：onEvict 不得再调 registry 方法
		}
		return id, true
	}
	return 0, false
}

// ModelID 返回标准模型的 dense id（不存在则分配；容量满返回 0）。
// 模型数量有界且小，不做 LRU。
func (r *registry) ModelID(model string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.models[model]; ok {
		return id
	}
	if r.nextMid >= maxModelIDs {
		return 0
	}
	r.nextMid++
	r.models[model] = r.nextMid
	return r.nextMid
}
