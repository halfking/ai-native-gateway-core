package nodestatecache

import "context"

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

// Select 是 R10.2 选择闭包的实现：位图 O(1) 预过滤（剔除不可用/冷却 =
// Avail 位 0、已试 Used 表、满载 = Full 位 1）→ 幸存集交 Scorer 排序 →
// 取第一；AllowModelSwitch 且当前模型幸存为空 → 经 ModelFallback 换模型
// 再选；全空 Exhausted=true；Flags bit0 sticky 优先。
//
// Selector / Updater / NeedProbe 三闭包单一责任、互不调用。
func (c *Cache) Select(ctx context.Context, q SelectQuery) SelectResult {
	tried := make(map[int32]struct{}, len(q.Used))
	for _, u := range q.Used {
		if u.NodeID > 0 {
			tried[u.NodeID] = struct{}{}
		}
	}

	// Flags bit0 sticky 优先：绑定节点健康（Avail）、未满载、未试过 → 直接复用。
	if q.Flags&FlagSticky != 0 && c.sticky != nil {
		if ref, ok := c.sticky.StickyNode(ctx, q.Model, q.TaskType); ok {
			if id := c.reg.Get(ref); id > 0 && c.bitmap.TestAvail(id) &&
				!c.bitmap.TestFull(id) && !seen(tried, id) {
				return SelectResult{NodeID: id, ModelID: c.reg.ModelID(q.Model)}
			}
		}
	}

	// 位图 O(1) 预过滤：Avail=1 且 Full=0 且未试，再按 Flags 精筛状态。
	survivors := c.prefilter(tried, q.Flags)

	// 当前模型评分取第一。
	if res, scored := c.scoreAndPick(ctx, q.Model, q.TaskType, survivors, q); scored != nil {
		return res
	}

	// AllowModelSwitch 且当前模型幸存为空 → 品质档位换模型再选。
	if q.AllowModelSwitch && c.fallback != nil {
		for _, alt := range c.fallback.FallbackModels(q.TaskType, q.Model) {
			if alt == "" || alt == q.Model {
				continue
			}
			if res, scored := c.scoreAndPick(ctx, alt, q.TaskType, survivors, q); scored != nil {
				res.SwitchedModel = true
				for i := range res.Alternatives {
					res.Alternatives[i].Packed |= packedBitSwitched
				}
				return res
			}
		}
	}

	// 全空：组合穷尽。
	return SelectResult{ModelID: c.reg.ModelID(q.Model), Exhausted: true}
}

func seen(tried map[int32]struct{}, id int32) bool {
	_, ok := tried[id]
	return ok
}

// prefilter 位图预过滤：剔除不可用/冷却（Avail=0）、满载（Full=1）、已试；
// FlagExcludeCooling 额外要求 State=available；FlagFreeTolerant 容忍
// State=degraded（缺省容忍 available+degraded；probing/offline/unknown 恒剔除）。
func (c *Cache) prefilter(tried map[int32]struct{}, flags uint32) []int32 {
	var out []int32
	strict := flags&FlagExcludeCooling != 0 // tolerant（bit1）语义：degraded 仍可路由，与缺省一致
	c.bitmap.foreachSet(planeAvail, func(id int32) {
		if seen(tried, id) || c.bitmap.TestFull(id) {
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
		out = append(out, id)
	})
	return out
}

// scoreAndPick 对一个候选模型完成评分与取第一。第二返回值非 nil 表示选中。
func (c *Cache) scoreAndPick(ctx context.Context, model, taskType string, survivors []int32, q SelectQuery) (SelectResult, []ScoredNode) {
	if len(survivors) == 0 {
		return SelectResult{}, nil
	}
	var scored []ScoredNode
	if c.scorer != nil {
		scored = c.scorer.Score(ctx, model, taskType, survivors)
	} else {
		scored = make([]ScoredNode, len(survivors))
		for i, id := range survivors {
			scored[i] = ScoredNode{NodeID: id}
		}
	}
	if len(scored) == 0 {
		return SelectResult{}, nil
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
	return res, scored
}
