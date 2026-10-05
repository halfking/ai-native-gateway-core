package main

import (
	"context"
	"time"
)

// v1_write_liveness.go —— v1 写入腿的存活读数与判决（审计 §9.264）。
//
// 实现 §9.238.6 留下的那条结论「S4 停写演练必须另有独立信号（行存在性 + 演练记录）」
// 的**代码那一半**。门在 v1_write_liveness_test.go。
//
// # 这道门在还原一条「已记录但没做」的结论
//
// §9.238 定位到本地 v1 有一次 **5 天写入中断**（09-06 22:00 → 09-11 21:21），
// 机制没定位。§9.238.5 的第 1 条是这个事件的要害：
//
//	★ **「v1 还在写」只能由行存在性证明，绝不能由开关证明。**
//	  这次就是反例：开关读数是 true、v1 实际零行、**全程无任何信号**。
//
// §9.238.6 给出的可执行结论是：
//
//	「S4 停写演练必须另有独立信号（行存在性 + 演练记录）」
//
// **这句结论到本轮为止没有实现。** 探针视图覆盖的是模型健康 / 探针队列 /
// 节点状态（`db/probe_views_unified.go`），**没有「v1 写入腿是否还活着」这一项**；
// 也没有任何指标或告警挂在 v1 写入新鲜度上（grep `v1_write|v1Write` 的
// 指标用法为空）。
//
// ⇒ 本文件补上「行存在性」那一半。**「演练记录」那一半是运维动作，不在代码里。**

// v1WriteLivenessSQL —— 「v1 写入腿是否还活着」的读数。
//
// ★ **必须读两个存储面**，这不是风格问题，是本轮在真库上量到的活的假阳性：
//
//	最近 30 分钟：request_logs_hot  91 行 / 落后 1 分钟
//	             request_logs      0 行 / 落后 **491 分钟**
//
// ⇒ 只读父表的信号**此刻就会报「v1 写腿已死」**，而它活得好好的。
// 父表落后 hot 是**正常形态**（hot 只留最新窗口、父表按月分区并有晋级延迟），
// 把这个正常形态当成死亡信号，会让运维去「修」一个健康的系统。
//
// 同一个坑 `dual_read_gate.go` 的文件头已经记成「§9.160.7 的 trap，
// 在本节里第三次命中」，本轮（§9.263/§9.264）是第四、第五次。
//
// 第三个面（session 侧）是**用来区分「死了」和「集群没流量」的**：
// 两侧同时为零时，什么都没证明，判 `quiet` 而不是 `dead`
// —— 这与 `dual_read_gate.go` 规则 2（「空扫描 = 什么都没证明」）同源。
const v1WriteLivenessSQL = `
SELECT
  (SELECT COUNT(*) FROM request_logs_hot     WHERE ts >= $1) AS v1_hot_rows,
  (SELECT COUNT(*) FROM request_logs         WHERE ts >= $1) AS v1_parent_rows,
  (SELECT COUNT(*) FROM session_turns_hot   WHERE ts >= $1) AS turns_hot_rows,
  (SELECT COUNT(*) FROM session_turns       WHERE ts >= $1) AS turns_parent_rows`

// v1WriteLivenessVerdict 是三个取值，**不合并**。
//
// 「死了」与「没人用」在运维上是两件不同的事：前者要修，后者不用管。
// 把它们合成一个布尔量，等于让运维在集群安静时收到死亡告警 ——
// 那类告警的真实结局是被忽略，然后下一次真的死亡也一起被忽略。
const (
	// livenessAlive：v1 侧在窗口内有行。判定只看**行存在性**。
	livenessAlive = "alive"
	// livenessDead：v1 侧两个面都零行，而 session 侧有行 ⇒ 写腿死了。
	// ★ 与开关读数无关：§9.238 里开关读 true 而 v1 零行。
	livenessDead = "dead"
	// livenessQuiet：两侧都零行。什么都没证明。
	livenessQuiet = "quiet"
)

// v1WriteLivenessInput 是判定所需的全部输入。
//
// 刻意做成结构体而不是四个位置参数：位置传错会把 livenessAlive 变成 livenessDead，
// 而这四个值全是 int64，编译器不会吭声。
type v1WriteLivenessInput struct {
	V1HotRows     int64
	V1ParentRows  int64
	TurnsHotRows  int64
	TurnsParent   int64
	ThresholdRows int64 // 窗口内多少行算「活着」；<=0 用默认 1
}

func (in v1WriteLivenessInput) threshold() int64 {
	if in.ThresholdRows <= 0 {
		return 1
	}
	return in.ThresholdRows
}

// ClassifyV1WriteLiveness 是纯函数，便于单测与变异检验。
func ClassifyV1WriteLiveness(in v1WriteLivenessInput) string {
	v1 := in.V1HotRows + in.V1ParentRows
	turns := in.TurnsHotRows + in.TurnsParent
	switch {
	case v1 >= in.threshold():
		return livenessAlive
	case turns >= in.threshold():
		// session 侧在写、v1 侧零行 ⇒ 写腿死了。
		return livenessDead
	default:
		return livenessQuiet
	}
}

// v1WriteLiveness 跑一次读数并给出判决。
func v1WriteLiveness(ctx context.Context, q dualReadQuerier, window time.Duration) (v1WriteLivenessInput, string, error) {
	var in v1WriteLivenessInput
	in.ThresholdRows = 1
	err := q.QueryRow(ctx, v1WriteLivenessSQL, time.Now().Add(-window)).
		Scan(&in.V1HotRows, &in.V1ParentRows, &in.TurnsHotRows, &in.TurnsParent)
	if err != nil {
		return in, "", err
	}
	return in, ClassifyV1WriteLiveness(in), nil
}
