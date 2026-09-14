# 存储优化 v2 —— S2→S4 停写 gate 观察台账（7 天零漂移）

> 依据：storage-optimization-plan.md §4-S2「对账 7 天零漂移」、§8-C/D。
> 观察起点：**2026-09-15**（本地网关 2.5.4.2113 / git 6622249e，dual-read 端点含 NULL↔0 归一化）。
> 每日一轮：`scripts/audit/storage_observation_round.sh`（端点抽样 + gate 分类），结果逐轮追加到本文末尾。

## gate 定义（单轮 PASS 判据）

硬指标（抽样会话内全部须为 0）：

| # | 指标 | 口径 |
|---|---|---|
| G1 | 字段漂移 | matched request_id 上 tokens/cost/success/credits 归一化（NULL↔0）不等，即端点 `token/cost/success/credits_drift_count` |
| G2 | 终态 v1 行缺镜像 | `only_in_v1` 且 v1 侧 `is_final_success=true` |
| G3 | 近期 turns 单侧行 | `only_in_v2` 且 ts ≥ 采样时刻−48h |

已知例外类（不计 gate，逐轮归因登记）：

| # | 类别 | 依据 |
|---|---|---|
| E1 | `only_in_v1` 非终态行 | 镜像链只入终态条目（mirror hook 设计），v1 的 `is_final_success=false` 行属预期缺失 |
| E2 | `only_in_v2` 历史行 | 观察起点之前的历史窗口差异（本机登记：09-10/09-11 时代行，v1 侧今日已无从对照；request_logs 无全局 TTL，成因为回填时代产物/历史清理，不属当前镜像漂移） |
| E3 | sys 合成会话 `v1_rows=0` | D4 设计：基表 request_logs 中探针行 `gw_session_id=NULL`，按 `sys:%` 基表对账天然失配；兼容视图已做 `sys:%`→NULL 保真。sys 会话对账走 §8-D 计费合计口径 |

S2→S4 停写 gate：**连续 7 个自然日每日一轮 PASS**（用户类 G1=G2=G3=0；sys 类按 E3 豁免 G2/G3、只查 G1）+ §8-D credits 合计等值。

## 逐轮记录

### Round 1 —— 2026-09-15 00:55 (+08)，gateway 2.5.4.2113 (git 6622249e)，**PASS**

分层抽样 10 会话（业务多轮 4 / 回环单轮 3 / sys 合成 3），端点 `GET /api/admin/sessions/{id}/dual-read?limit=5`：

| 会话 | 类 | v1/v2 行 | only_v1 | only_v2 | G1 字段漂移 | 归因 |
|---|---|---|---|---|---|---|
| gw_7a19bfa5… | biz_multi | 583/703 | 31 (E1) | 151 (E2) | 0/0/0/0 | only_v1 全部 is_final_success=false；only_v2 全部 09-10/09-11 时代 |
| gs_gw_7a19bfa5… | biz_multi | 172/177 | 31 (E1) | 36 (E2) | 0/0/0/0 | 同上 |
| gw_98bfbafe… | biz_multi | 73/68 | 5 (E1) | 0 | 0/0/0/0 | only_v1 全部 is_final_success=false |
| gw_8dd3d88c… | biz_multi | 54/50 | 4 (E1) | 0 | 0/0/0/0 | 同上 |
| gw_c843cea2… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| gw_fb953e57… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| gw_24fb3e8f… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| sys:probe:cred35:20260914 | sys | 0/1181 | 0 | 1181 (E3) | 0/0/0/0 | D4 设计失配 |
| sys:probe:cred11:20260914 | sys | 0/1090 | 0 | 1090 (E3) | 0/0/0/0 | 同上 |
| sys:probe:cred50:20260914 | sys | 0/358 | 0 | 358 (E3) | 0/0/0/0 | 同上 |

- G1=G2=G3=0 → **PASS**（7 天计第 1 天）
- 备注：①本日早些时候在旧二进制（2110，16:28 构建）上首调抓到的 cost/credits"漂移"为占位零值伪影——NULL↔0 归一化修复（a789a05ad，16:48 提交）晚于该镜像构建，重部署 2113 后归零；②outbound_body 停写复测（S1b）：2026-09-14 16:32:24 开关 PUT 后父表+hot 违规均 0（§6 的 1.8GB/月收益计入，详见 plan §4.2-10 闭环）。

#### Round 1 官方脚本输出（storage_observation_round.sh，2026-09-15 01:06 +08 / 17:06Z，10/10 PASS）

```
sid|class|v1|v2|only_v1|only_v2|G1(tok,cost,succ,cred)|G2_final_missing|G3_recent_v2only|verdict
gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|589|708|32|151|0,0,0,0|0|0|PASS
gs_gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|173|178|31|36|0,0,0,0|0|0|PASS
gw_98bfbafe-3a03-48e5-a747-2e4b1ccfbc34|biz_multi|73|68|5|0|0,0,0,0|0|0|PASS
gw_8dd3d88c-1cca-4a40-b47d-801c7026a7bd|biz_multi|54|50|4|0|0,0,0,0|0|0|PASS
gw_24fb3e8f-f370-4a35-ae44-863f327137d1|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_c843cea2-354d-42eb-bd1a-324204c79afe|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_fb953e57-c9f2-409c-ab05-42110d6508bc|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
sys:probe:cred35:20260914|sys|0|1185|0|1185|0,0,0,0|0|1185|PASS
sys:probe:cred11:20260914|sys|0|1092|0|1092|0,0,0,0|0|1092|PASS
sys:probe:cred50:20260914|sys|0|362|0|362|0,0,0,0|0|362|PASS
ROUND_RESULT|sessions=10|fail=0|verdict=PASS|at=2026-09-14T17:06:20Z
```

