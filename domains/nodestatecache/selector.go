package nodestatecache

import (
	"context"
	"sync"
)

// SelectQuery 选择闭包入参（R10.2，签名照抄规格）。
type SelectQuery struct {
	Model            string          // 模型（必填；auto 已解析）
	TaskType         string          // 任务类型（可空；同品质档位换模型用）
	Used             []NodeUseRecord // 本请求已用节点与状态（排除表 + 状态参考）
	AllowModelSwitch bool            // 可切换模型标志
	RetryCount       int             // 重试次数
	Flags            uint32          // 其它标志位：bit0 sticky 优先、bit1 free 容忍、bit2 排除冷却中…
}

// SelectResult 选择闭包出参（R10.2，签名照抄规格）。
type SelectResult struct {
	NodeID, ModelID int32
	SwitchedModel   bool
	Exhausted       bool            // 组合穷尽信号（供 R2.4 提前终止）
	Alternatives    []NodeUseRecord // 后备序列（已按评分排序）
}

// NodeSelector 选择闭包类型（R10.2）。
type NodeSelector func(ctx context.Context, q SelectQuery) SelectResult

// SelectFlags（SelectQuery.Flags 位定义，R10.2）。
const (
	FlagSticky         uint32 = 1 << 0 // bit0 sticky 优先
	FlagFreeTolerant   uint32 = 1 << 1 // bit1 free 容忍（容忍降级态/瞬态错误节点）
	FlagExcludeCooling uint32 = 1 << 2 // bit2 排除冷却中（额外要求 State=available）
)

// NodeUseRecord 节点使用记录（R10.3，16B 紧凑整型，逐字段照抄规格）。
// 热路径紧凑结构；全量诊断走 journey attempt_* 事件，字段映射固定
// （见 record_test.go 钉死的映射表）。随 QueuedRequest append-only 携带，
// 上限 = 尝试预算（MaxUseRecords）。
type NodeUseRecord struct {
	Seq     int32  // 序号（请求内递增，1 起）↔ journey attempt_no
	ModelID int32  // 标准模型 id
	NodeID  int32  // 节点 dense id（R10.4 注册表分配）
	Packed  uint32 // bit0-7 状态枚举 | bit8-15 错误分类 | bit16 是否重试 | bit17 切换而来 | bit18 sticky | bit19-31 预留
}

// MaxUseRecords 每请求 NodeUseRecord append-only 上限（= 尝试预算 100）。
const MaxUseRecords = 100

// Packed 位域定义（R10.3）。
const (
	packedStateMask   uint32 = 0xff << 0 // bit0-7 状态枚举
	packedErrKindMask uint32 = 0xff << 8 // bit8-15 错误分类
	packedBitRetry    uint32 = 1 << 16   // bit16 是否重试
	packedBitSwitched uint32 = 1 << 17   // bit17 切换而来
	packedBitSticky   uint32 = 1 << 18   // bit18 sticky
	// bit19-31 预留。
)

// PackUseRecord 把 (状态, 错误分类, 重试, 切换而来, sticky) 编码进 Packed 位域。
func PackUseRecord(state uint8, errKind uint8, retry, switched, sticky bool) uint32 {
	p := uint32(state) | uint32(errKind)<<8
	if retry {
		p |= packedBitRetry
	}
	if switched {
		p |= packedBitSwitched
	}
	if sticky {
		p |= packedBitSticky
	}
	return p
}

// UnpackUseRecord 解码 Packed 位域为 (状态, 错误分类, 重试, 切换而来, sticky)。
// 与 PackUseRecord 互逆；映射固定并被 record_test.go 钉死。
func UnpackUseRecord(packed uint32) (state uint8, errKind uint8, retry, switched, sticky bool) {
	state = uint8(packed & 0xff)
	errKind = uint8(packed >> 8 & 0xff)
	retry = packed&packedBitRetry != 0
	switched = packed&packedBitSwitched != 0
	sticky = packed&packedBitSticky != 0
	return
}

// AppendUseRecord 向请求内 append-only 使用记录追加一条，上限
// MaxUseRecords（= 尝试预算 100），超限丢弃（热路径紧凑结构，
// 全量诊断走 journey）。
func AppendUseRecord(records []NodeUseRecord, r NodeUseRecord) []NodeUseRecord {
	if len(records) >= MaxUseRecords {
		return records
	}
	return append(records, r)
}

// ScoredNode 评分排序后的幸存节点。
type ScoredNode struct {
	NodeID int32
	Score  float64
}

// Scorer 幸存集评分排序接口——集成者接 URSM
// FilterAndScoreReadyWithSource（0.4·price + 0.4·latency + 0.2·失败率）。
// 返回已按分数从优到劣排序、且真正服务 model 的节点子集（模型归属过滤
// 由实现方负责：本包位图不区分模型）。nil 时退化为 dense id 序。
type Scorer interface {
	Score(ctx context.Context, model, taskType string, survivors []int32) []ScoredNode
}

// StickyLookup 同会话 sticky 绑定查询（AffinityKey → 绑定节点）。
type StickyLookup interface {
	StickyNode(ctx context.Context, model, taskType string) (NodeRef, bool)
}

// ModelFallback 按任务类型 → 品质档位换模型（AllowModelSwitch 且当前模型
// 幸存为空时调用；返回同品质档位的候选模型序列）。
type ModelFallback interface {
	FallbackModels(taskType, currentModel string) []string
}

// smallUsedLinearLimit 已试记录线性扫描上限：不超过该值时 excluded 判断
// 走切片线性比较，避免热路径每次 Select 分配 map（10k 宇宙 p99 门禁 R10.6）。
const smallUsedLinearLimit = 8

// scratch pools：Select 每次调用在 10k 节点宇宙下会产生 ~9.5k 元素的幸存集
// 与评分输出；不复用的话每调用 ~190KB 垃圾会带来 GC 抖动，使 p99 门禁对
// 同机负载不鲁棒。两池均为「取用→归还」语义，缓冲不跨 Select 存活。
var (
	survivorsPool sync.Pool // []int32
	scoredPool    sync.Pool // []ScoredNode
)

func getSurvivorsScratch() []int32 {
	if v := survivorsPool.Get(); v != nil {
		return v.([]int32)[:0]
	}
	return make([]int32, 0, 1024)
}

func putSurvivorsScratch(buf []int32) {
	survivorsPool.Put(buf)
}

func getScoredScratch() []ScoredNode {
	if v := scoredPool.Get(); v != nil {
		return v.([]ScoredNode)[:0]
	}
	return make([]ScoredNode, 0, 1024)
}

func putScoredScratch(buf []ScoredNode) {
	scoredPool.Put(buf)
}

// excluded 返回本请求已试节点判断函数：小 Used 走线性比较，大 Used 建 map。
func excluded(used []NodeUseRecord) func(int32) bool {
	if len(used) == 0 {
		return func(int32) bool { return false }
	}
	if len(used) <= smallUsedLinearLimit {
		return func(id int32) bool {
			for i := range used {
				if u := used[i].NodeID; u > 0 && u == id {
					return true
				}
			}
			return false
		}
	}
	m := make(map[int32]struct{}, len(used))
	for _, u := range used {
		if u.NodeID > 0 {
			m[u.NodeID] = struct{}{}
		}
	}
	return func(id int32) bool {
		_, ok := m[id]
		return ok
	}
}

// Select 是 R10.2 选择闭包的实现：位图 O(1) 预过滤（剔除不可用/冷却 =
// Avail 位 0、已试 Used 表、满载 = Full 位 1）→ 幸存集交 Scorer 排序 →
// 取第一；AllowModelSwitch 且当前模型幸存为空 → 经 ModelFallback 换模型
// 再选；全空 Exhausted=true；Flags bit0 sticky 优先。
//
// Selector / Updater / NeedProbe 三闭包单一责任、互不调用。
func (c *Cache) Select(ctx context.Context, q SelectQuery) SelectResult {
	isTried := excluded(q.Used)

	// Flags bit0 sticky 优先：绑定节点健康（Avail）、未满载、未试过 → 直接复用。
	if q.Flags&FlagSticky != 0 && c.sticky != nil {
		if ref, ok := c.sticky.StickyNode(ctx, q.Model, q.TaskType); ok {
			if id := c.reg.Get(ref); id > 0 && c.bitmap.TestAvail(id) &&
				!c.bitmap.TestFull(id) && !isTried(id) {
				return SelectResult{NodeID: id, ModelID: c.reg.ModelID(q.Model)}
			}
		}
	}

	// 位图 O(1) 预过滤：Avail=1 且 Full=0 且未试，再按 Flags 精筛状态。
	// 幸存集缓冲池化复用，Select 返回前归还（Scorer 契约：survivors 仅
	// 本次调用有效，不得保留引用）。
	survivors := getSurvivorsScratch()
	survivors = c.prefilter(isTried, q.Flags, survivors)

	if res := c.scoreAndPick(ctx, q.Model, q.TaskType, survivors, q); res.picked {
		putSurvivorsScratch(survivors)
		return res.result
	}

	// AllowModelSwitch 且当前模型幸存为空 → 品质档位换模型再选。
	if q.AllowModelSwitch && c.fallback != nil {
		for _, alt := range c.fallback.FallbackModels(q.TaskType, q.Model) {
			if alt == "" || alt == q.Model {
				continue
			}
			if res := c.scoreAndPick(ctx, alt, q.TaskType, survivors, q); res.picked {
				res.result.SwitchedModel = true
				for i := range res.result.Alternatives {
					res.result.Alternatives[i].Packed |= packedBitSwitched
				}
				putSurvivorsScratch(survivors)
				return res.result
			}
		}
	}

	putSurvivorsScratch(survivors)
	// 全空：组合穷尽。
	return SelectResult{ModelID: c.reg.ModelID(q.Model), Exhausted: true}
}

// prefilter 位图预过滤：剔除不可用/冷却（Avail=0）、满载（Full=1）、已试；
// FlagExcludeCooling 额外要求 State=available；FlagFreeTolerant 容忍
// State=degraded（缺省容忍 available+degraded；probing/offline/unknown 恒剔除）。
// 结果追加进调用方提供的 dst 缓冲（池化复用）。
func (c *Cache) prefilter(isTried func(int32) bool, flags uint32, dst []int32) []int32 {
	strict := flags&FlagExcludeCooling != 0 // tolerant（bit1）语义：degraded 仍可路由，与缺省一致
	c.bitmap.foreachSet(planeAvail, func(id int32) {
		if isTried(id) || c.bitmap.TestFull(id) {
			return
		}
		st := c.stats.get(id).State
		if strict && st != StateAvailable {
			return // bit2：排除冷却中/降级痕迹，仅 available
		}
		// 缺省与 bit1（free 容忍）均可路由 degraded（对齐 URSM：
		// degraded 可参与、由评分降权）；probing/offline/unknown 恒剔除。
		if !strict && st != StateAvailable && st != StateDegraded {
			return
		}
		dst = append(dst, id)
	})
	return dst
}

// pickOutcome 折叠 scoreAndPick 的选中状态与结果，避免把池化评分切片
// 泄漏到调用方。
type pickOutcome struct {
	result SelectResult
	picked bool
}

// scoreAndPick 对一个候选模型完成评分与取第一。picked=false 表示该模型
// 无可服务幸存节点。仅对本次自行分配的评分缓冲做池化归还；Scorer 返回的
// 切片归实现方所有（可能内部复用），不得入池。
func (c *Cache) scoreAndPick(ctx context.Context, model, taskType string, survivors []int32, q SelectQuery) pickOutcome {
	if len(survivors) == 0 {
		return pickOutcome{}
	}
	scored := getScoredScratch()
	poolOwned := true
	if c.scorer != nil {
		poolOwned = false
		scored = c.scorer.Score(ctx, model, taskType, survivors)
	} else {
		for _, id := range survivors {
			scored = append(scored, ScoredNode{NodeID: id})
		}
	}
	if len(scored) == 0 {
		if poolOwned {
			putScoredScratch(scored)
		}
		return pickOutcome{}
	}
	res := SelectResult{
		NodeID:  scored[0].NodeID,
		ModelID: c.reg.ModelID(model),
	}
	// 后备序列（已按评分排序），上限 MaxUseRecords。
	n := len(scored) - 1
	if n > MaxUseRecords {
		n = MaxUseRecords
	}
	res.Alternatives = make([]NodeUseRecord, n)
	for i := 0; i < n; i++ {
		id := scored[i+1].NodeID
		slot := c.stats.get(id)
		res.Alternatives[i] = NodeUseRecord{
			Seq:     int32(i + 2),
			ModelID: res.ModelID,
			NodeID:  id,
			Packed:  PackUseRecord(slot.State, 0, q.RetryCount > 0, false, false),
		}
	}
	if poolOwned {
		putScoredScratch(scored)
	}
	return pickOutcome{result: res, picked: true}
}
