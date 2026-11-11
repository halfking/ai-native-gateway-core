// k6 API负载测试脚本
// 测试目标：验证常规API调用的性能和稳定性

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('errors');

export let options = {
  stages: [
    { duration: '2m', target: 50 },    // 2分钟内升到50并发用户
    { duration: '3m', target: 50 },    // 维持50并发3分钟
    { duration: '2m', target: 100 },   // 2分钟内升到100并发
    { duration: '3m', target: 100 },   // 维持100并发3分钟
    { duration: '2m', target: 0 },     // 2分钟内降到0
  ],
  thresholds: {
    'http_req_duration': ['p(95)<1000', 'p(99)<2000'],  // 95%请求<1s, 99%<2s
    'http_req_failed': ['rate<0.05'],                    // 错误率<5%
    'errors': ['rate<0.05'],                             // 自定义错误率<5%
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8782';
const API_KEY = __ENV.API_KEY || 'test-key-placeholder';

export default function () {
  const url = `${BASE_URL}/v1/chat/completions`;
  
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { 
        role: 'user', 
        content: 'Hello, this is a load test message. Please respond briefly.' 
      }
    ],
    stream: false,
    max_tokens: 50,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${API_KEY}`,
    },
    timeout: '30s',
  };

  const res = http.post(url, payload, params);
  
  const success = check(res, {
    'status is 200': (r) => r.status === 200,
    'response time < 1000ms': (r) => r.timings.duration < 1000,
    'has response body': (r) => r.body && r.body.length > 0,
    'valid json': (r) => {
      try {
        JSON.parse(r.body);
        return true;
      } catch (e) {
        return false;
      }
    },
  });

  errorRate.add(!success);

  sleep(1); // 每个用户请求间隔1秒
}

export function handleSummary(data) {
  return {
    'stdout': textSummary(data, { indent: ' ', enableColors: true }),
    'summary.json': JSON.stringify(data),
  };
}

function textSummary(data, options) {
  const indent = options.indent || '';
  const enableColors = options.enableColors || false;
  
  let summary = '\n' + indent + '=== Load Test Summary ===\n';
  summary += indent + `Total Requests: ${data.metrics.http_reqs.values.count}\n`;
  summary += indent + `Failed Requests: ${data.metrics.http_req_failed.values.passes}\n`;
  summary += indent + `Request Rate: ${data.metrics.http_reqs.values.rate.toFixed(2)} req/s\n`;
  summary += indent + `Response Time (p50): ${data.metrics.http_req_duration.values['p(50)'].toFixed(2)}ms\n`;
  summary += indent + `Response Time (p95): ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms\n`;
  summary += indent + `Response Time (p99): ${data.metrics.http_req_duration.values['p(99)'].toFixed(2)}ms\n`;
  summary += indent + `Error Rate: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%\n`;
  
  return summary;
}
