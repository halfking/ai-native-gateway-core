package persist

import "strconv"

func atoi(s string) int           { n, _ := strconv.Atoi(s); return n }
func atoi64(s string) int64       { n, _ := strconv.ParseInt(s, 10, 64); return n }
func parseFloat(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// 818：assign* 系列把 hash 的字符串值转成 typed 列的指针。
//
// 一律用指针 + 「空串/缺失即 nil」语义，而不是零值：hash 是 HGETALL 的
// map[string]string，**键可能根本不存在**。落成 0 会让「这一时刻没采集到
// 这个字段」与「采集到 0」在库里无法区分 —— 而这两者在排障时是相反的结论
// （例如 last_probe_latency_ms=0 意味着探测从未成功过，不是「延迟为零」）。
//
// 解析失败同样返回 nil：宁可让调用方看到「值不可解析」，也不要写入一个
// 被静默当作真实读数的 0。

func assignInt64(dst **int64, s string) {
	if s == "" {
		return
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*dst = &n
	}
}

func assignInt(dst **int, s string) {
	if s == "" {
		return
	}
	if n, err := strconv.Atoi(s); err == nil {
		*dst = &n
	}
}

func assignFloat(dst **float64, s string) {
	if s == "" {
		return
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		*dst = &f
	}
}

// assignBool 接受 hash 的 "0"/"1" 约定（与 Row.Available 的解析一致）。
// 任何其他取值一律不赋值，而不是当成 false。
func assignBool(dst **bool, s string) {
	switch s {
	case "1":
		v := true
		*dst = &v
	case "0":
		v := false
		*dst = &v
	}
}

func assignStr(dst **string, s string) {
	if s == "" {
		return
	}
	*dst = &s
}

// isPayloadDuplicateKey 报告该 hash 键是否已在 typed 列中存在。
func isPayloadDuplicateKey(k string) bool {
	for _, dup := range payloadDuplicateKeys {
		if k == dup {
			return true
		}
	}
	return false
}
