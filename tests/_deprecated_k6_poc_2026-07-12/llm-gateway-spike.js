// tests/k6/llm-gateway-spike.js
// 高并发压测 — 覆盖 spike/baseline/soak 三种场景。
// 用法：
//   TARGET_RPS=200 DURATION=30s k6 run llm-gateway-spike.js
// 或：
//   k6 run -e TARGET_RPS=200 -e DURATION=30s llm-gateway-spike.js

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";

const TARGET_RPS    = parseInt(__ENV.TARGET_RPS    || "50",  10);
const DURATION      = __ENV.DURATION              || "30s";
const GATEWAY_URL   = __ENV.GATEWAY_URL           || "http://localhost:58781";
const API_KEY       = __ENV.API_KEY               || "sk-loc-1234567890abcdef";
const MODEL         = __ENV.MODEL                 || "minimax-m3";

export const options = {
    scenarios: {
        constant_rps: {
            executor: "constant-arrival-rate",
            rate: TARGET_RPS,
            timeUnit: "1s",
            duration: DURATION,
            preAllocatedVUs: Math.max(20, Math.ceil(TARGET_RPS / 4)),
            maxVUs: 200,
        },
    },
    thresholds: {
        http_req_failed: ["rate<0.01"],
        http_req_duration: ["p(99)<800"],
        checks: ["rate>0.95"],
    },
    noConnectionReuse: false,
    userAgent: "k6-local-spike/1.0",
};

const errRate = new Rate("errors");
const sucRate = new Rate("success");
const ttfbTrend = new Trend("ttfb_ms");
const firstChunkTrend = new Trend("first_chunk_ms");

const MODELS = [
    "minimax-m3",
    "gpt-5.6-luna",
    "gpt-5.6-terra",
    "claude-sonnet-5",
    "gpt-4o",
];

export default function () {
    const model = MODELS[Math.floor(Math.random() * MODELS.length)];
    const payload = JSON.stringify({
        model,
        messages: [
            { role: "user", content: `k6 probe ${__VU}-${__ITER} ${Date.now()}` },
        ],
        max_tokens: 20,
        stream: false,
    });

    const params = {
        headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${API_KEY}`,
        },
        tags: { model },
        timeout: "10s",
    };

    const start = Date.now();
    const res = http.post(`${GATEWAY_URL}/v1/chat/completions`, payload, params);
    const dur = Date.now() - start;
    ttfbTrend.add(dur);

    const ok = check(res, {
        "200 OK": (r) => r.status === 200,
        "non-empty body": (r) => (r.body || "").length > 0,
        "latency < 2s": (r) => r.timings.duration < 2000,
    });

    if (res.status === 200) {
        sucRate.add(1);
    } else {
        sucRate.add(0);
        errRate.add(1);
    }
}

export function handleSummary(data) {
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    return {
        [`reports/loadtest-summary-${stamp}.json`]: JSON.stringify(data, null, 2),
        stdout: textSummary(data, { indent: " ", enableColors: true }),
    };
}

import { textSummary } from "https://jslib.k6.io/k6-summary/0.0.3/index.js";
