# D03 IR 与协议转换横向不变量 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
无。零条候选发现。

## 二、核实为健康的面
1. IR 核心路径零改动：internal/ir/、internal/irconv/、internal/paramreg/、domains/transformation/ 均无窗口 diff。
2. R45 四恢复点文件全部不在窗口改动面（source_reasoning_context.go:107/:141、executor_chat.go:1662/1834/1879、executor_anthropic.go:562-568、inline_validation.go:49、handler_gemini.go:226-227）。
3. Responses integrity 终态链未触（responses_bridge.go:273 及 719-1357 调用点、survival_coordinator.go:1107、stream.go:544）。
4. executors 窗口改动确认为纯路由 side-effect（sticky 绑定/负载窗口，不触任何序列化符号）。
5. provider/client.go LATERAL 只影响取价（消费面 router_scoring.go:575-584、admin 读面），请求体构造链未触碰。

## 三、未覆盖项
流式端到端真机回归（需真机凭据）；sticky_load 行为深审属路由域。
