# R56 · D09 节点状态自检 · 48h 审计结论

> 时间：2026-09-23 · 与 D08 同批审计（同子代理覆盖）

## 结论
B2 老化（2h 无证据→recovering）恢复路径在位（recoveringSweeper 30min/批 10 或 legacy consensus 重验 3 ok 回 healthy）；闪断双确认（2s+2s 落 2~5s 窗、15s 预算、双败才降级、per-(cred,model) 去重、WithoutCancel+45s+recover）；demote 只动 model_probe_state 不动 cmb.unavailable_recover_at——与 404 三级冷却/绑定量级降权无冲突；actor 串（node-probe-worker）与用量扫描排除面一致。

## 遗留
ProbeConfirm/ProbeSync 并发闸两项随 D08 登记推进。
