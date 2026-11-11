import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const errorRate = new Rate('errors');

export const options = {
  stages: [
    { duration: '30s', target: 10 },  // 预热
    { duration: '1m', target: 50 },   // 增加负载
    { duration: '2m', target: 100 },  // 高负载
    { duration: '30s', target: 0 },   // 冷却
  ],
  thresholds: {
    http_req_duration: ['p(95)<2000'], // 95%请求 < 2秒
    errors: ['rate<0.01'],              // 错误率 < 1%
  },
};

const BASE_URL = __ENV.APP_URL || 'http://localhost:8080';

export default function () {
  // 健康检查
  const healthRes = http.get(`${BASE_URL}/`);
  const healthOk = check(healthRes, {
    '健康检查成功': (r) => r.status === 200,
  });
  errorRate.add(!healthOk);

  sleep(1);

  // API请求（如果有API密钥）
  const apiKey = __ENV.API_KEY;
  if (apiKey) {
    const apiRes = http.post(
      `${BASE_URL}/v1/chat/completions`,
      JSON.stringify({
        model: 'gpt-3.5-turbo',
        messages: [{ role: 'user', content: 'Hello' }],
      }),
      {
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${apiKey}`,
        },
      }
    );
    
    const apiOk = check(apiRes, {
      'API请求成功': (r) => r.status === 200 || r.status === 401,
      'API响应时间<2s': (r) => r.timings.duration < 2000,
    });
    errorRate.add(!apiOk);
  }

  sleep(1);
}

export function handleSummary(data) {
  return {
    'stdout': JSON.stringify(data, null, 2),
  };
}
