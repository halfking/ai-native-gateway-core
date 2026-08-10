package paramreg

import "encoding/json"

// translateRandomSeed 在 Mistral 的 random_seed 与通用 seed 之间转换。
//
// Mistral 用 random_seed 表达其它厂商的 seed。值语义一致（整数），只是键名不同。
func translateRandomSeed(value json.RawMessage, src, dst Dialect) (string, json.RawMessage, bool) {
	if dst == DialectMistral {
		return "random_seed", value, true
	}
	// 出向非 Mistral：改回通用 seed 键名。
	//
	// 注意 seed 本身是 KindIRHandled（IR 有 Seed 字段），所以目标 body 里
	// 很可能已经有 seed 了。还原器只写目标不存在的键，不会覆盖 IR 的输出。
	return "seed", value, true
}
