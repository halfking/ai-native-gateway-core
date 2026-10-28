/**
 * DP口径 — 对齐 2026-09-07 gateway-result.txt
 * GET /v1/models (+ optional /health). NOT G1 added-latency.
 * Caller: k6 run …; requires local GW_BASE. lib.sh blacklist via run wrapper.
 */
import http from 'k6/http';
import { check, sleep } from 'k6';

const GW = __ENV.GW_BASE || 'http://127.0.0.1:8782';

export const options = {
  thresholds: {
    // Historical threshold was p(95)<500ms — keep as soft documentation, not G1.
    http_req_duration: ['p(95)<500'],
  },
  stages: [
    { duration: '20s', target: 10 },
    { duration: '30s', target: 50 },
    { duration: '30s', target: 100 },
    { duration: '10s', target: 0 },
  ],
};

export default function () {
  const h = http.get(`${GW}/health`);
  check(h, { 'health 2xx': (r) => r.status >= 200 && r.status < 300 });
  const m = http.get(`${GW}/v1/models`);
  check(m, { 'models 2xx': (r) => r.status >= 200 && r.status < 300 });
  sleep(0.1);
}
