package ir

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// 客户端「数值/布尔参数被写成字符串」的容错层（2026-11-11 审计轮）。
//
// ── 缺口 ───────────────────────────────────────────────────────────────
// 真实客户端（JS/TS SDK 手搓、跨语言中转）常把数值参数写成字符串：
//
//	{"model":"gpt-4","messages":[…],"temperature":"0.7","max_tokens":"100"}
//
// ⇒ 严格反序列化失败 ⇒ 网关直接 400，**客户端没有任何线索知道该改什么**。
// 而 `"0.7"` 的意图是**无歧义**的（它就是那个数）⇒ 网关有条件修正它。
//
// ── 设计要点 ───────────────────────────────────────────────────────────
//  ① **零手写清单**：目标字段集合由**反射**从 parser 自己的 src 结构体推导
//     ⇒ 新增/改名参数不会与容错层漂移（这是「手写清单必然漂移」的反例：
//     清单的唯一真源就是**类型定义本身**）。
//  ② **只转无歧义的**：字符串必须能被**无损**解析成该字段的目标类型；
//     解析不了（"abc" → 0.7）就**不转** ⇒ 仍然报错，不猜客户端意图。
//  ③ **不碰字符串类型字段**：model="12345"、user="007" 这类**合法**的
//     字符串值不会被误转（目标类型是 string ⇒ 不在候选集）。
//  ④ **不静默**：每次强制转换都上报 anomaly ⇒ 运维能看见"有多少客户端
//     在发这种错格式"，这正是评估是否要在客户端侧修的信号。
//
// ── 边界（如实登记）────────────────────────────────────────────────────
//  · 只处理**顶层**字段。嵌套对象（如 Gemini 的 generationConfig.temperature）
//    不在本轮范围内 —— 嵌套层的字段类型不在同一个 src 结构体里，
//    反射推导不到 ⇒ 不做猜测。
//  · 数组元素内的数值字段同理不处理。
//  · 修正后的值会进入 Extensions（若该字段被登记为已知字段则不进）
//    ⇒ 出站线上是**修正后的数值**，与客户端意图一致。

// coerceNumericStrings 尝试把 body 里的「字符串化数值/布尔」修正为对应类型。
//
// dst 必须是**指向 parser 的 src 结构体**的指针（反射用其类型推导字段）。
// 返回 (修正后的 body, 是否发生了修正)。
//
// 不修改任何既有行为：没发生修正时返回原始 body 与 false。
func coerceNumericStrings(body []byte, dst any) ([]byte, bool) {
	if dst == nil {
		return body, false
	}
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return body, false
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return body, false
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil || rawMap == nil {
		return body, false
	}

	targets := numericFieldTypes(elem.Type())
	if len(targets) == 0 {
		return body, false
	}

	changed := false
	for key, kind := range targets {
		rawVal, ok := rawMap[key]
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(string(rawVal))
		if len(trimmed) < 2 || trimmed[0] != '"' {
			continue // 不是字符串值 ⇒ 无需修正
		}
		var s string
		if err := json.Unmarshal(rawVal, &s); err != nil {
			continue
		}
		converted, ok := coerceScalar(s, kind)
		if !ok {
			continue // 无歧义性不成立 ⇒ 不猜
		}
		rawMap[key] = json.RawMessage(converted)
		changed = true
		ReportParseUnknownField("unknown", "", key, map[string]any{
			"coerced_from": "string",
			"coerced_to":   kindName(kind),
			"value":        s,
		})
	}

	if !changed {
		return body, false
	}

	out, err := json.Marshal(rawMap)
	if err != nil {
		return body, false
	}
	return out, true
}

// numericFieldTypes 从结构体类型推导「顶层 JSON key → 目标标量类型」。
//
// 只收指针/值形态的数值与布尔基本类型；json.RawMessage（[]byte）与
// 结构体/切片/映射一律不收（它们的内部形态不由本层处理）。
func numericFieldTypes(t reflect.Type) map[string]reflect.Kind {
	out := map[string]reflect.Kind{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			out[name] = reflect.Float64 // 数值统一按 float64 判定
		case reflect.Bool:
			out[name] = reflect.Bool
		default:
			// json.RawMessage 是 []byte ⇒ 落在这里 ⇒ 不收 ✓
		}
	}
	return out
}

// coerceScalar 把字符串转成目标标量的 JSON 字面量。
//
// 返回 ("", false) 表示**无歧义性不成立**（不转）。
func coerceScalar(s string, kind reflect.Kind) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	switch kind {
	case reflect.Float64:
		// 只接受能被无损往返的十进制数（排除 NaN/Inf/科学计数歧义）。
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false
		}
		if strings.ContainsAny(s, "xXnNiI") {
			return "", false
		}
		return strconv.FormatFloat(f, 'g', -1, 64), true
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return "", false
		}
		return strconv.FormatBool(b), true
	default:
		return "", false
	}
}

// kindName 给标量类型一个可读名（用于 anomaly metadata）。
func kindName(kind reflect.Kind) string {
	switch kind {
	case reflect.Float64:
		return "number"
	case reflect.Bool:
		return "bool"
	default:
		return fmt.Sprintf("%v", kind)
	}
}
