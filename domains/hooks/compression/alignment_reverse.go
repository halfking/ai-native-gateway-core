// docs/design/2026-08-09 O-2: AlignmentMap reverse lookup.
//
// D01/D03（三十五轮清点）：验收标准写的是「original→sanitized→compressed 对
// 每个 block 的 identity/occurrence 一致」。审计一直只有**前向**查询
// （"第 N 条原始消息去了哪里"，buildAlignmentMap），因此反向问题——
// "压缩后的第 M 条消息，是由哪些原始消息构成的"——在代码里**无法回答**。
//
// 本文件提供该反向入口。设计约束：
//
//   - **纯增量**：不改 AlignmentInfo 结构、不改 buildAlignmentMap、不改任何
//     既有行为；只在既有数据上做只读推导。
//   - **纯函数**：输入 []AlignmentInfo，输出反查结果，便于离线对账与审计
//     工具复用。
//   - **不吞掉 dropped**：TargetKind=dropped 的原始消息没有压缩后位置，
//     反查时必须能被单独问出来，而不是悄悄消失——「找不到」与「已知被丢弃」
//     是两件事。
//
// 坐标空间必须显式：TargetSpace ∈ {messages, top_level_system, none}。摘要
// 既可能落在 messages[summaryIdx]，也可能落在 Anthropic 顶层 system，两者的
// CompressedIndex 含义不同，不可混为一谈。

package compression

// ReverseOrigin describes where one compressed-side message came from.
type ReverseOrigin struct {
	// OriginalIndex 原始（压缩前）消息数组中的下标。
	OriginalIndex int
	// Hash 原始消息内容指纹，与 AlignmentInfo.Hash 同源，可用于内容级校验。
	Hash string
	// Occurrence 该 Hash 在原始序列中的第几次出现（identity/occurrence 的
	// 另一半：同一内容的重复消息不能被折叠成一条）。
	Occurrence int
	// TargetKind retained / summary / dropped，与 AlignmentInfo 一致。
	TargetKind string
}

// ReverseResolution is the answer to "what produced compressed message M".
type ReverseResolution struct {
	// CompressedIndex 被问的压缩后下标。
	CompressedIndex int
	// TargetSpace 该下标所属坐标空间；调用方按此解释 CompressedIndex。
	TargetSpace string
	// Origins 构成该压缩消息的原始消息，按 OriginalIndex 升序。
	Origins []ReverseOrigin
	// Known reports whether the queried index exists in the alignment map at
	// all. false = 该坐标空间内没有这个下标（查询本身错了，或压缩产生了
	// 更多消息）。
	Known bool
	// FullyAccounted reports whether every source message reached a mapped
	// target. false = 存在 TargetKind=dropped 的原始消息，即内容在机械裁剪
	// 中真丢了。审计上这与「查错了」必须区分。
	FullyAccounted bool
}

// ResolveCompressedToOriginal answers the reverse of buildAlignmentMap: given
// a compressed-side message index, which original messages produced it.
//
// space selects the coordinate space to query; pass TargetSpaceMessages for
// the ordinary case. Passing an empty space defaults to messages.
//
// A compressed summary can legitimately be fed by many originals, hence a
// slice return. Summary-folded originals share one CompressedIndex; retained
// ones have their own.
func ResolveCompressedToOriginal(align []AlignmentInfo, compressedIndex int, space string) ReverseResolution {
	if space == "" {
		space = TargetSpaceMessages
	}
	res := ReverseResolution{
		CompressedIndex: compressedIndex,
		TargetSpace:     space,
		Known:           false,
		// Default true; flipped when a dropped original is found for this index.
		FullyAccounted: true,
	}
	for _, a := range align {
		if a.TargetSpace != space {
			continue
		}
		// buildAlignmentMap's invariant: CompressedInto >= 0 implies
		// CompressedInto == CompressedIndex (summary branch writes both to
		// summaryIdx; retained leaves CompressedInto at -1; dropped leaves
		// both at -1). That invariant is pinned by
		// TestAlignmentBuilderKeepsCompressedIntoInvariant, so matching on
		// CompressedIndex alone is sufficient here. An earlier version
		// preferred CompressedInto when >= 0 — that branch was provably
		// identical to CompressedIndex on real builder output, i.e. dead
		// weight that a mutation could not distinguish.
		if a.CompressedIndex != compressedIndex {
			continue
		}
		res.Known = true
		res.Origins = append(res.Origins, ReverseOrigin{
			OriginalIndex: a.OriginalIndex,
			Hash:          a.Hash,
			Occurrence:    a.Occurrence,
			TargetKind:    a.TargetKind,
		})
		if a.TargetKind == TargetKindDropped {
			res.FullyAccounted = false
		}
	}
	sortReverseOrigins(res.Origins)
	return res
}

// DroppedOriginals lists every original message that reached no compressed
// counterpart. This is the query an auditor needs to answer "did the trim lose
// anything", and it must be expressible without asking about a specific index.
func DroppedOriginals(align []AlignmentInfo) []ReverseOrigin {
	var out []ReverseOrigin
	for _, a := range align {
		if a.TargetKind == TargetKindDropped {
			out = append(out, ReverseOrigin{
				OriginalIndex: a.OriginalIndex,
				Hash:          a.Hash,
				Occurrence:    a.Occurrence,
				TargetKind:    a.TargetKind,
			})
		}
	}
	sortReverseOrigins(out)
	return out
}

// sortReverseOrigins keeps the answer deterministic: alignment is built in
// source order today, but the reverse view is a map lookup in spirit and must
// not inherit that accident.
func sortReverseOrigins(o []ReverseOrigin) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && o[j].OriginalIndex < o[j-1].OriginalIndex; j-- {
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
}

// Coordinate-space and target-kind tokens. buildAlignmentMap used bare string
// literals; they are named here so the reverse view and the forward builder
// cannot drift apart on a typo.
const (
	TargetKindRetained = "retained"
	TargetKindSummary  = "summary"
	TargetKindDropped  = "dropped"

	TargetSpaceMessages       = "messages"
	TargetSpaceTopLevelSystem = "top_level_system"
	TargetSpaceNone           = "none"
)
