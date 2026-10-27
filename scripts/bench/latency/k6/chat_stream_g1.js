/**
 * G1 template — streaming chat via gateway (AL requires a paired direct run).
 * Prefer measure_added_latency.py for formal AL; this k6 file is load-shape only.
 * Refuses nothing by itself — export GW_BASE only after lib.sh check.
 */
import http from 'k6/http';
import { check, sleep } from 'k6';

const GW = __ENV.GW_BASE || 'http://127.0.0.1:8080';
const KEY = __ENV.GATEWAY_API_KEY || '';
const MODEL = __ENV.MODEL || 'mock-model';
const VUS = Number(__ENV.VUS || 10);

export const options = {
  vus: VUS,
  duration: __ENV.DURATION || '60s',
  thresholds: {
    // Placeholder: G1 is AL not absolute via-gateway latency.
    http_req_failed: ['rate<0.05'],
  },
};

export default function () {
  const payload = JSON.stringify({
    model: MODEL,
    stream: true,
    messages: [{ role: 'user', content: 'ping' }],
    max_tokens: 16,
  });
  const res = http.post(`${GW}/v1/chat/completions`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: KEY ? `Bearer ${KEY}` : '',
    },
    timeout: '60s',
  });
  check(res, { 'chat 2xx': (r) => r.status >= 200 && r.status < 300 });
  sleep(0.2);
}
