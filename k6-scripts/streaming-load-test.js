// k6 流式接口负载测试脚本
// 测试目标：验证SSE流式传输的性能和稳定性

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Counter } from 'k6/metrics';

const errorRate = new Rate('errors');
const streamChunks = new Counter('stream_chunks');

export let options = {
  stages: [
    { duration: '1m', target: 20 },    // 1分钟升到20并发
    { duration: '3m', target: 20 },    // 维持20并发3分钟
    { duration: '1m', target: 50 },    // 1分钟升到50并发
    { duration: '3m', target: 50 },    // 维持50并发3分钟
    { duration: '1m', target: 0 },     // 1分钟降到0
  ],
  thresholds: {
    'http_req_duration': ['p(95)<2000'],  // 95%请求<2s
    'http_req_failed': ['rate<0.05'],
    'errors': ['rate<0.05'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8782';
const API_KEY = [REDACTED:secret] || 'test-key-placeholder';

export default function () {
  const url = `${BASE_URL}/v1/chat/completions`;
  
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { 
        role: 'user', 
        content: 'Write a short story about a robot learning to paint. Keep it under 100 words.' 
      }
    ],
    stream: true,
    max_tokens: 150,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `[REDACTED:secret]`,
      'Accept': 'text/event-stream',
    },
    timeout: '60s',
  };

  const res = http.post(url, payload, params);
  
  // 统计SSE数据块数量
  const dataLines = (res.body.match(/^data: /gm) || []).length;
  streamChunks.add(dataLines);
  
  const success = check(res, {
    'status is 200': (r) => r.status === 200,
    'content-type is event-stream': (r) => 
      r.headers['Content-Type'] && r.headers['Content-Type'].includes('event-stream'),
    'has SSE data': (r) => r.body.includes('data:'),
    'has done marker': (r) => r.body.includes('[DONE]') || r.body.includes('data: [DONE]'),
    'received chunks': () => dataLines > 0,
  });

  errorRate.add(!success);

  sleep(2); // 流式请求间隔2秒
}

export function handleSummary(data) {
  let summary = '\n=== Streaming Load Test Summary ===\n';
  summary += `Total Requests: ${data.metrics.http_reqs.values.count}\n`;
  summary += `Failed Requests: ${data.metrics.http_req_failed.values.passes}\n`;
  summary += `Request Rate: ${data.metrics.http_reqs.values.rate.toFixed(2)} req/s\n`;
  summary += `Response Time (p50): ${data.metrics.http_req_duration.values['p(50)'].toFixed(2)}ms\n`;
  summary += `Response Time (p95): ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms\n`;
  summary += `Total Stream Chunks: ${data.metrics.stream_chunks.values.count}\n`;
  summary += `Avg Chunks per Request: ${(data.metrics.stream_chunks.values.count / data.metrics.http_reqs.values.count).toFixed(2)}\n`;
  summary += `Error Rate: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%\n`;
  
  return {
    'stdout': summary,
    'streaming-summary.json': JSON.stringify(data),
  };
}
