package outputcompliance

import "context"

// RestoredValues 是「本次响应里由占位符还原器注入回原文的值」集合。
//
// 背景：响应拦截链的顺序是（cmd/gateway/goal_control.go）
//
//	强制输出守卫 → 占位符还原 → 输出合规
//
// 输出合规**排在还原之后**，因此它检查的文本里混着两类来源完全不同的值：
//
//   - 模型自己写的值（无出处，不在网关的映射里）
//   - 刚被还原器从 {SENSITIVE:type:n} 换回**调用方原始输入**的值
//
// 旧实现分不清这两者，于是一视同仁地单向遮蔽——用户自己发过来的 IP、账号、
// 手机号，被网关亲手还原出来，又被网关亲手压成星号，且没有任何映射能换回去。
// 结果就是「请求里好好的、响应回来变成 ****」，运维命令、工具参数（代码执行
// 路径同样走这条链）全部失效。
//
// 契约：**网关自己注入的值不得被输出侧遮蔽销毁**。它按定义已经回到调用方
// 手里，遮蔽它既不防泄漏（调用方本来就有），又必然破坏内容。
//
// 判定口径是「整段文本精确相等」，不是子串包含：只有当某个命中区间**整个**
// 就是某个已还原值时才豁免。模型新写的、恰好与调用方某个值重合的片段同样
// 豁免——那是调用方自己的数据，回给它不构成新的信息泄漏。
type RestoredValues map[string]struct{}

type restoredValuesCtxKey struct{}

// WithRestoredValues 把本次响应的已还原值集合挂到 ctx 上，供下游每一次
// checker.Check 调用读取。空集合返回原 ctx（不制造无意义的 context 层）。
func WithRestoredValues(ctx context.Context, values RestoredValues) context.Context {
	if ctx == nil || len(values) == 0 {
		return ctx
	}
	return context.WithValue(ctx, restoredValuesCtxKey{}, values)
}

// RestoredValuesFromContext 读取 WithRestoredValues 挂上的集合；没有时返回 nil。
func RestoredValuesFromContext(ctx context.Context) RestoredValues {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(restoredValuesCtxKey{}).(RestoredValues)
	return values
}

// Contains 报告整段文本是否就是一个已还原值。
func (v RestoredValues) Contains(text string) bool {
	if len(v) == 0 || text == "" {
		return false
	}
	_, ok := v[text]
	return ok
}

// NewRestoredValues 由一组原值构造集合。空值被丢弃：命中「空串」不构成豁免。
func NewRestoredValues(values []string) RestoredValues {
	live := make(RestoredValues, len(values))
	for _, value := range values {
		if value != "" {
			live[value] = struct{}{}
		}
	}
	if len(live) == 0 {
		return nil
	}
	return live
}
