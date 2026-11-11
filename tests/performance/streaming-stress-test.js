import ws from 'k6/ws';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// 自定义指标
const errorRate = new Rate('stream_errors');
const firstByteTime = new Trend('time_to_first_byte');
const connectionSuccessRate = new Rate('connection_success');

export const options = {
  stages: [
    { duration: '2m', target: 20 },   // 2分钟内逐步增加到20并发
    { duration: '3m', target: 50 },   // 3分钟内增加到50并发
    { duration: '5m', target: 50 },   // 保持50并发5分钟
    { duration: '3m', target: 100 },  // 3分钟内增加到100并发
    { duration: '5m', target: 100 },  // 保持100并发5分钟
    { duration: '2m', target: 0 },    // 2分钟内降到0
  ],
  thresholds: {
    stream_errors: ['rate<0.005'],              // 流错误率<0.5%
    time_to_first_byte: ['p(95)<1000'],        // 95%首字节<1秒
    connection_success: ['rate>0.99'],          // 连接成功率>99%
    ws_session_duration: ['p(95)<30000'],      // 95%会话<30秒
  },
};

const BASE_URL = __ENV.BASE_URL || 'ws://localhost:8780';
const API_KEY = __ENV.API_KEY || 'test-api-key';

export default function() {
  const url = `${BASE_URL}/v1/chat/completions`;
  
  const payload = JSON.stringify({
    model: 'gpt-3.5-turbo',
    messages: [
      { 
        role: 'user', 
        content: 'Write a short story about a robot learning to paint. Make it about 200 words.' 
      }
    ],
    stream: true,
    max_tokens: 300,
  });

  const params = {
    headers: {
      'Authorization': `Bearer ${API_KEY}`,
      'Content-Type': 'application/json',
    },
  };

  let firstByte = null;
  let startTime = Date.now();
  let chunkCount = 0;
  let connected = false;

  const res = ws.connect(url, params, function(socket) {
    connected = true;
    connectionSuccessRate.add(1);

    socket.on('open', function() {
      socket.send(payload);
    });

    socket.on('message', function(data) {
      if (firstByte === null) {
        firstByte = Date.now();
        firstByteTime.add(firstByte - startTime);
      }

      chunkCount++;

      // 检查消息格式
      try {
        const message = JSON.parse(data);
        check(message, {
          'has choices': (m) => m.choices && m.choices.length > 0,
          'has delta': (m) => m.choices[0].delta !== undefined,
        }) || errorRate.add(1);

        // 检测流结束
        if (message.choices[0].finish_reason === 'stop') {
          socket.close();
        }
      } catch (e) {
        errorRate.add(1);
      }
    });

    socket.on('error', function(e) {
      errorRate.add(1);
      console.error('WebSocket error:', e);
    });

    socket.on('close', function() {
      check(chunkCount, {
        'received chunks': (c) => c > 0,
        'received enough data': (c) => c > 5,
      }) || errorRate.add(1);
    });

    // 设置超时
    socket.setTimeout(function() {
      console.warn('Stream timeout after 30s');
      errorRate.add(1);
      socket.close();
    }, 30000);
  });

  if (!connected) {
    connectionSuccessRate.add(0);
  }

  check(res, {
    'status is 101': (r) => r && r.status === 101,
  });
}
