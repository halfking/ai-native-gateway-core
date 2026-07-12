// tests/k6/llm-gateway-soak.js
// 长时间稳定性（soak）测试 — 50 RPS 持续 5 分钟，用于探测内存泄漏 / 连接泄漏。
// 用法：
//   TARGET_RPS=50 DURATION=5m k6 run llm-gateway-soak.js

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";
import { textSummary } from "https://jslib.k6.io/k6-summary/0.0.3/index.js";

const TARGET_RPS  = parseInt(__ENV.TARGET_RPS  || "50", 10);
const DURATION    = __ENV.DURATION            || "5m";
const GATEWAY_URL = __ENV.GATEWAY_URL         || "http://localhost:58781";
const API_KEY     = __ENV.API_KEY             || "sk-loc-1234567890abcdef";

export const options = {
    scenarios: {
        soak: {
            executor: "constant-arrival-rate",
            rate: TARGET_RPS,
            timeUnit: "1s",
            duration: DURATION,
            preAllocatedVUs: 50,
            maxVUs: 200,
        },
    },
    thresholds: {
        http_req_failed: ["rate<0.02"],
        http_req_duration: ["p(99)<1500"],
        checks: ["rate>0.93"],
    },
};

const errRate = new Rate("errors");
const p50 = new Trend("p50_latency");
const p99 = new Trend("p99_latency");

const MODELS = ["minimax-m3", "gpt-5.6-luna", "gpt-5.6-terra", "claude-sonnet-5"];

export default function () {
    const model = MODELS[Math.floor(Math.random() * MODELS.length)];
    const res = http.post(
        `${GATEWAY_URL}/v1/chat/completions`,
        JSON.stringify({
            model,
            messages: [
                { role: "user", content: `soak ${__VU} ${__ITER} ${Date.now()}` },
            ],
            max_tokens: 25,
            stream: false,
        }),
        {
            headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${API_KEY}`,
            },
            timeout: "10s",
        },
    );
    p50.add(res.timings.duration);
    p99.add(res.timings.duration);
    errRate.add(res.status !== 200);
    check(res, { "ok": (r) => r.status === 200 });
}

export function handleSummary(data) {
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    return {
        stdout: textSummary(data, { indent: " ", enableColors: true }),
        [`reports/soak-summary-${stamp}.json`]: JSON.stringify(data, null, 2),
    };
}
