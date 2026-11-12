import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const errorRate = new Rate('errors');

export const options = {
  stages: [
    { duration: '10s', target: 10 },   // 正常负载
    { duration: '10s', target: 200 },  // 突发尖峰
    { duration: '30s', target: 200 },  // 持续尖峰
    { duration: '10s', target: 10 },   // 恢复正常
    { duration: '10s', target: 0 },    // 停止
  ],
  thresholds: {
    http_req_duration: ['p(95)<3000'], // 尖峰时允许3秒
    errors: ['rate<0.05'],              // 尖峰时允许5%错误
  },
};

const BASE_URL = __ENV.APP_URL || 'http://localhost:8080';

export default function () {
  const res = http.get(`${BASE_URL}/`);
  
  const ok = check(res, {
    '状态码2xx/3xx': (r) => r.status >= 200 && r.status < 400,
    '响应时间<5s': (r) => r.timings.duration < 5000,
  });
  
  errorRate.add(!ok);
  sleep(0.5);
}

export function handleSummary(data) {
  console.log(`
=== 尖峰测试总结 ===
总请求数: ${data.metrics.http_reqs.values.count}
平均响应时间: ${data.metrics.http_req_duration.values.avg.toFixed(2)}ms
P95响应时间: ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms
P99响应时间: ${data.metrics.http_req_duration.values['p(99)'].toFixed(2)}ms
错误率: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%
  `);
  
  return {
    'stdout': JSON.stringify(data, null, 2),
  };
}
