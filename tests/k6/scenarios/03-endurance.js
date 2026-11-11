import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const errorRate = new Rate('errors');

export const options = {
  stages: [
    { duration: '1m', target: 20 },  // 预热
    { duration: '5m', target: 50 },  // 持续负载
    { duration: '30s', target: 0 },  // 冷却
  ],
  thresholds: {
    http_req_duration: ['p(95)<2000'],
    errors: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.APP_URL || 'http://localhost:8080';

export default function () {
  const res = http.get(`${BASE_URL}/`);
  
  const ok = check(res, {
    '状态正常': (r) => r.status === 200,
    '无内存泄漏迹象': (r) => r.timings.duration < 3000,
  });
  
  errorRate.add(!ok);
  sleep(2);
}

export function handleSummary(data) {
  const duration = data.state.testRunDurationMs / 1000;
  const reqCount = data.metrics.http_reqs.values.count;
  const throughput = reqCount / duration;
  
  console.log(`
=== 耐久性测试总结 ===
测试时长: ${duration.toFixed(0)}秒
总请求数: ${reqCount}
吞吐量: ${throughput.toFixed(2)} req/s
平均响应: ${data.metrics.http_req_duration.values.avg.toFixed(2)}ms
P95响应: ${data.metrics.http_req_duration.values['p(95)'].toFixed(2)}ms
错误率: ${(data.metrics.errors.values.rate * 100).toFixed(2)}%
  `);
  
  return {
    'stdout': JSON.stringify(data, null, 2),
  };
}
