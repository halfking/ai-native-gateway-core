package compression

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D01/D03 反向解析入口（三十五轮续十一）。前向 buildAlignmentMap 早已存在，
// 但反向问题「压缩后的第 M 条由哪些原始消息构成」在代码里无法回答。

// TestResolveCompressedToOriginal_RetainedIsSelfMap pins the 1:1 case: a
// retained message resolves to exactly itself.
func TestResolveCompressedToOriginal_RetainedIsSelfMap(t *testing.T) {
	align := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: false, Hash: "h0", TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
		{OriginalIndex: 1, CompressedIndex: 1, IsCompressed: false, Hash: "h1", TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
	}
	res := ResolveCompressedToOriginal(align, 1, TargetSpaceMessages)
	require.True(t, res.Known, "index 1 must be a known compressed position")
	assert.True(t, res.FullyAccounted)
	require.Len(t, res.Origins, 1)
	assert.Equal(t, 1, res.Origins[0].OriginalIndex)
	assert.Equal(t, "h1", res.Origins[0].Hash)
}

// TestResolveCompressedToOriginal_SummaryFansInMany is the case the audit says
// could not be answered: one summary message is fed by many originals. Before
// this entry point there was no way to enumerate them.
func TestResolveCompressedToOriginal_SummaryFansInMany(t *testing.T) {
	align := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, CompressedInto: 0, IsCompressed: true, Hash: "h0", TargetKind: TargetKindSummary, TargetSpace: TargetSpaceMessages},
		{OriginalIndex: 1, CompressedIndex: 0, CompressedInto: 0, IsCompressed: true, Hash: "h1", TargetKind: TargetKindSummary, TargetSpace: TargetSpaceMessages},
		{OriginalIndex: 2, CompressedIndex: 0, CompressedInto: 0, IsCompressed: true, Hash: "h2", TargetKind: TargetKindSummary, TargetSpace: TargetSpaceMessages},
		{OriginalIndex: 3, CompressedIndex: 1, IsCompressed: false, Hash: "h3", TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
	}
	res := ResolveCompressedToOriginal(align, 0, TargetSpaceMessages)
	require.True(t, res.Known)
	assert.True(t, res.FullyAccounted, "summary folding accounts for every source")
	require.Len(t, res.Origins, 3, "the summary must resolve back to all three folded originals")
	// Sorted by OriginalIndex regardless of input order.
	assert.Equal(t, []int{0, 1, 2}, []int{res.Origins[0].OriginalIndex, res.Origins[1].OriginalIndex, res.Origins[2].OriginalIndex})

	// The retained tail resolves to itself, not to the summary.
	tail := ResolveCompressedToOriginal(align, 1, TargetSpaceMessages)
	require.Len(t, tail.Origins, 1)
	assert.Equal(t, 3, tail.Origins[0].OriginalIndex)
}

// TestResolveCompressedToOriginal_UnknownIndexIsNotSilentlyEmpty is the
// audit-safety property: asking about an index that does not exist must be
// distinguishable from asking about one whose sources were all dropped.
func TestResolveCompressedToOriginal_UnknownIndexIsNotSilentlyEmpty(t *testing.T) {
	align := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: false, TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
	}
	res := ResolveCompressedToOriginal(align, 7, TargetSpaceMessages)
	assert.False(t, res.Known, "a non-existent compressed index must report Known=false")
	assert.Empty(t, res.Origins, "…and carry no fabricated origins")
}

// TestResolveCompressedToOriginal_SpacesAreNotMixed guards the coordinate-space
// boundary. An Anthropic summary living in top-level system shares no meaning
// with a message-array index, even when the numbers collide.
func TestResolveCompressedToOriginal_SpacesAreNotMixed(t *testing.T) {
	align := []AlignmentInfo{
		// buildAlignmentMapForProtocol leaves BOTH indices at -1 for a summary that
		// lands in Anthropic top-level system (only TargetKind/TargetSpace are set),
		// so -1 IS that coordinate space's index — not an "unset" marker here.
		{OriginalIndex: 0, CompressedIndex: -1, CompressedInto: -1, IsCompressed: true, TargetKind: TargetKindSummary, TargetSpace: TargetSpaceTopLevelSystem},
		{OriginalIndex: 1, CompressedIndex: 0, IsCompressed: false, TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
	}

	// Index 0 in the messages space must NOT pick up the top_level_system entry.
	msgRes := ResolveCompressedToOriginal(align, 0, TargetSpaceMessages)
	require.Len(t, msgRes.Origins, 1)
	assert.Equal(t, 1, msgRes.Origins[0].OriginalIndex)

	// Asking in the other space finds it.
	sysRes := ResolveCompressedToOriginal(align, -1, TargetSpaceTopLevelSystem)
	require.True(t, sysRes.Known)
	require.Len(t, sysRes.Origins, 1)
	assert.Equal(t, 0, sysRes.Origins[0].OriginalIndex)

	// Defaulting to messages must not silently switch spaces.
	assert.Equal(t, TargetSpaceMessages, ResolveCompressedToOriginal(align, 0, "").TargetSpace)
}

// TestDroppedOriginals_ReportsMechanicalTrimLoss is the query that makes
// "the trim lost something" answerable at all.
func TestDroppedOriginals_ReportsMechanicalTrimLoss(t *testing.T) {
	align := []AlignmentInfo{
		{OriginalIndex: 0, CompressedIndex: 0, IsCompressed: false, TargetKind: TargetKindRetained, TargetSpace: TargetSpaceMessages},
		{OriginalIndex: 1, CompressedIndex: -1, IsCompressed: true, CompressedInto: -1, TargetKind: TargetKindDropped, TargetSpace: TargetSpaceNone},
		{OriginalIndex: 2, CompressedIndex: -1, IsCompressed: true, CompressedInto: -1, TargetKind: TargetKindDropped, TargetSpace: TargetSpaceNone},
	}
	dropped := DroppedOriginals(align)
	require.Len(t, dropped, 2, "mechanically trimmed messages must be enumerable")
	assert.Equal(t, 1, dropped[0].OriginalIndex)
	assert.Equal(t, 2, dropped[1].OriginalIndex)

	// A dropped original has no compressed coordinate, so no index query can
	// surface it — which is exactly why DroppedOriginals is a separate entry
	// point rather than a special case of the reverse lookup.
	for idx := -1; idx <= 2; idx++ {
		res := ResolveCompressedToOriginal(align, idx, TargetSpaceMessages)
		assert.True(t, res.FullyAccounted,
			"dropped originals live in space 'none', so no messages-space bucket is unaccounted")
	}

	// In the dropped coordinate space the sources ARE found, and finding them
	// is exactly the "not fully accounted" signal — the two must not be conflated.
	noneRes := ResolveCompressedToOriginal(align, -1, TargetSpaceNone)
	require.True(t, noneRes.Known)
	assert.Len(t, noneRes.Origins, 2)
	assert.False(t, noneRes.FullyAccounted, "content genuinely lost by the trim")
}

// TestResolveCompressedToOriginal_EmptyAndNilAreSafe keeps the entry point
// total: a nil alignment map is a legitimate "nothing compressed" state.
func TestResolveCompressedToOriginal_EmptyAndNilAreSafe(t *testing.T) {
	for _, align := range [][]AlignmentInfo{nil, {}} {
		res := ResolveCompressedToOriginal(align, 0, TargetSpaceMessages)
		assert.False(t, res.Known)
		assert.Empty(t, res.Origins)
		assert.True(t, res.FullyAccounted, "nothing dropped means nothing unaccounted")
		assert.Empty(t, DroppedOriginals(align))
	}
}

// TestReverseLookupAgreesWithForwardMap ties the reverse view back to the real
// builder: for every alignment entry, the reverse query at its own
// CompressedIndex must return it. This is what stops the two views from
// drifting.
func TestReverseLookupAgreesWithForwardMap(t *testing.T) {
	before := []byte(`{"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":"two"},
		{"role":"user","content":"three"}
	]}`)
	after := []byte(`{"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":"two"},
		{"role":"user","content":"three"}
	]}`)
	align := buildAlignmentMap(before, after, -1)
	require.NotEmpty(t, align)

	seen := map[int]bool{}
	for _, a := range align {
		if a.CompressedIndex < 0 {
			continue
		}
		res := ResolveCompressedToOriginal(align, a.CompressedIndex, TargetSpaceMessages)
		require.True(t, res.Known, "entry %d must be findable in reverse", a.OriginalIndex)
		found := false
		for _, o := range res.Origins {
			if o.OriginalIndex == a.OriginalIndex {
				found = true
			}
		}
		assert.True(t, found, "forward entry %d must appear in its own reverse bucket", a.OriginalIndex)
		seen[a.CompressedIndex] = true
	}
	assert.NotEmpty(t, seen)
}

// TestAlignmentBuilderKeepsCompressedIntoInvariant pins the builder-side
// invariant the reverse lookup now relies on: CompressedInto >= 0 implies
// CompressedInto == CompressedIndex, and retained entries leave CompressedInto
// at -1.
//
// This exists because the reverse view's earlier CompressedInto branch was
// unobservable — a mutation removing it stayed green, because the two fields
// are always equal on real builder output. Rather than keep untestable dead
// logic, the lookup was simplified and the invariant was made a test. If a
// future builder change breaks this, the reverse view's correctness is
// re-established here instead of by luck.
func TestAlignmentBuilderKeepsCompressedIntoInvariant(t *testing.T) {
	cases := []struct {
		name       string
		before     string
		after      string
		summaryIdx int
	}{
		{
			name:       "retained-and-summary",
			before:     `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`,
			after:      `{"messages":[{"role":"system","content":"summary of a and b"},{"role":"user","content":"c"}]}`,
			summaryIdx: 0,
		},
		{
			name:       "mechanical-drop",
			before:     `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`,
			after:      `{"messages":[{"role":"user","content":"a"}]}`,
			summaryIdx: -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			align := buildAlignmentMap([]byte(tc.before), []byte(tc.after), tc.summaryIdx)
			require.NotEmpty(t, align, "fixture must produce a non-empty map")
			for _, a := range align {
				if a.CompressedInto >= 0 {
					assert.Equal(t, a.CompressedIndex, a.CompressedInto,
						"original %d: builder must keep CompressedInto == CompressedIndex", a.OriginalIndex)
				} else {
					assert.Equal(t, -1, a.CompressedInto,
						"original %d: entries without a folding target keep CompressedInto = -1", a.OriginalIndex)
				}
			}
		})
	}
}
