package main

import "testing"

// TestS4GateCoverageSQLReadsBothFaces pins the four relations v1CoverageHoursSQL
// reads.
//
// Why this is a test and not a comment: §9.238 re-measured the local window and
// found v1 wrote **zero** rows for 5 days (2026-09-06 21:00:27 → 2026-09-11
// 21:21:10) while `session_turns` and `request_wal` both kept writing. The gate
// correctly called that window void (`insufficient_v1_coverage_in_window`).
//
// The danger is that a *wrong* void looks exactly like that true one. Reading
// only the parent `request_logs` — §9.160.7's trap, hit three times already in
// this section — drops the newest hours and drives V1CoveragePP to ~0 while v1
// is in fact writing normally. Both land on the same reason string, so the
// verdict cannot tell them apart, and the obvious "fix" (lowering
// s4MinV1CoveragePP) deletes the rule instead of the bug.
//
// The four relations are asymmetric on purpose and are all load-bearing:
//
//   - `request_logs_hot` — the pre-promote window; without it the most recent
//     hours vanish from the numerator.
//   - `request_logs` — the promoted partitions; without it every historical
//     hour vanishes.
//   - `session_turns_hot` / `session_turns` — the denominator is "hours with
//     traffic on EITHER side". Losing the session side would shrink the
//     denominator instead, inflating the ratio toward a false Ready.
func TestS4GateCoverageSQLReadsBothFaces(t *testing.T) {
	for _, rel := range []string{
		"request_logs_hot", "request_logs",
		"session_turns_hot", "session_turns",
	} {
		if !containsIdentifier(v1CoverageHoursSQL, rel) {
			t.Errorf("v1CoverageHoursSQL no longer reads %s —— %s。"+
				"两面缺一都会让 V1CoveragePP 变成假数：少读 v1 侧 ⇒ 假 void（会诱使人调低 "+
				"s4MinV1CoveragePP，直接毁掉这条规则）；少读 session 侧 ⇒ 分母变小 ⇒ 假 Ready。",
				rel, coverageFaceLossConsequence[rel])
		}
	}
}

var coverageFaceLossConsequence = map[string]string{
	"request_logs_hot":  "丢的是最新若干小时（预提升窗口）",
	"request_logs":      "丢的是全部历史小时（已提升分区）",
	"session_turns_hot": "分母丢掉最近若干小时 ⇒ 比例被抬高",
	"session_turns":     "分母丢掉全部历史小时 ⇒ 比例被抬高",
}
