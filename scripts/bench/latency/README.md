# Latency Bench Harness（附加延迟 / 数据面口径）

> 包 06 · 2026-10-28 · **禁止**对 184/71/252/154 等生产机压测。
> 口径报告：`llm-gateway/docs/核实-延迟口径与压测方案-2026-10-28.md`

## 两个不可混用的口径

| 代号 | 测什么 | 历史数字 | 可否对拍 G1（附加延迟 P95≤50ms） |
|---|---|---|---|
| **DP** 数据面 | `GET /v1/models`（或 `/health`）完整中间件栈，**不含上游推理** | **P95=89.73ms**（2026-09-07，阶梯 10→100VU） | **否** |
| **AL** 附加延迟 | 同 payload：`via_gateway − direct_upstream`（流式优先看 TTFB 差） | 尚无正式报告 | **是**（G1 唯一合法口径） |

## 快速开始（本地 mock 冒烟，不碰生产）

```bash
bash scripts/bench/latency/run_smoke.sh
```

冒烟只起本地 Python mock + 本地「伪网关」转发，验证：
1. 生产主机黑名单生效；
2. AL 公式能算出接近注入开销的附加延迟；
3. 报告 JSON 落盘。

## 复现历史 DP 口径（需本地网关，默认 :8782）

```bash
export GW_BASE=http://127.0.0.1:8782   # 禁止写成生产 IP
k6 run scripts/bench/latency/k6/data_plane_baseline.js
```

## G1 附加延迟（需：本地网关 + 本地 mock 上游）

```bash
export GW_BASE=http://127.0.0.1:8080
export UPSTREAM_BASE=http://127.0.0.1:19080
export GATEWAY_API_KEY=sk-local
export UPSTREAM_API_KEY=mock-A
python3 scripts/bench/latency/measure_added_latency.py --concurrency 10 --requests 100
```

## 文件

| 路径 | 作用 |
|---|---|
| `lib.sh` | 生产主机黑名单 + 公共校验 |
| `mock_pair.py` | 冒烟用：固定延迟上游 + 可注入开销的转发层 |
| `measure_added_latency.py` | AL 口径测量（direct vs via） |
| `run_smoke.sh` | 一键本地冒烟 |
| `k6/data_plane_baseline.js` | DP 口径（对齐 2026-09-07） |
| `k6/chat_stream_g1.js` | G1 流式场景模板（需真实 GW） |
| `scenarios.json` | 场景矩阵 |
