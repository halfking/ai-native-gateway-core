# 245泳道修复验证计划

**修复版本**: seq=1251, commit=ff28c65d
**部署时间**: 2026-07-21
**验证网站**: https://llmgo.kxpms.cn
**登录凭证**: admin / Veritrans&9527

---

## 一、验证目标

验证修复是否解决了"泳道滚动"问题：
- ✅ 泳道数据不再无故跳变
- ✅ 没有新请求时，stats.total保持稳定
- ✅ request_id集合不再在连续快照间"滑动"

---

## 二、手动验证步骤

### 步骤1: 登录仪表盘

1. 访问 https://llmgo.kxpms.cn
2. 使用 admin / Veritrans&9527 登录
3. 进入"实时请求流"页面

### 步骤2: 浏览器Console监控（5-10分钟）

打开浏览器开发者工具（F12），在Console中粘贴以下代码：

```javascript
// ===== 泳道滚动检测脚本 =====
(function() {
    console.log('%c🔬 开始监控泳道稳定性...', 'color: #3fb950; font-size: 14px; font-weight: bold;');

    let lastSnapshot = {};
    let stats = {
        normalAdds: 0,
        rollingCount: 0,
        messageCount: 0,
        startTime: Date.now()
    };

    // 拦截SSE消息
    const originalAddEventListener = EventSource.prototype.addEventListener;
    EventSource.prototype.addEventListener = function(type, listener, options) {
        if (type === 'message') {
            const wrappedListener = function(event) {
                stats.messageCount++;

                try {
                    const msg = JSON.parse(event.data);

                    if (msg.type === 'initial_data' && msg.snapshot?.dimensions) {
                        console.log('%c📊 收到initial_data', 'color: #58a6ff;', {
                            请求数: msg.requests?.length || 0,
                            泳道数: Object.values(msg.snapshot.dimensions).flat().length
                        });

                        // 初始化快照
                        for (const [dim, lanes] of Object.entries(msg.snapshot.dimensions)) {
                            for (const lane of lanes) {
                                const key = `${dim}:${lane.id}`;
                                lastSnapshot[key] = {
                                    ids: new Set(lane.requests.map(r => r.request_id)),
                                    total: lane.stats.total,
                                    name: lane.name
                                };
                            }
                        }
                    } else if (msg.type === 'request' && msg.delta?.changed_lanes) {
                        // 分析delta变化
                        for (const [dim, lanes] of Object.entries(msg.delta.changed_lanes)) {
                            for (const lane of lanes) {
                                const key = `${dim}:${lane.id}`;
                                const currIds = new Set(lane.requests.map(r => r.request_id));
                                const last = lastSnapshot[key];

                                if (last) {
                                    const added = [...currIds].filter(id => !last.ids.has(id));
                                    const removed = [...last.ids].filter(id => !currIds.has(id));

                                    if (removed.length > 0 && added.length > 0) {
                                        // 🚨 检测到窗口滚动
                                        stats.rollingCount++;
                                        console.log('%c🚨 窗口滚动！', 'color: #f85149; font-weight: bold;', {
                                            维度: dim,
                                            泳道: lane.name,
                                            移除: removed.length,
                                            新增: added.length,
                                            移除示例: removed.slice(0, 3),
                                            新增示例: added.slice(0, 3),
                                            旧total: last.total,
                                            新total: lane.stats.total
                                        });
                                    } else if (added.length > 0) {
                                        // ✅ 正常新增
                                        stats.normalAdds++;
                                        if (added.length > 5) {
                                            console.log('%c✅ 正常新增', 'color: #3fb950;', {
                                                维度: dim,
                                                泳道: lane.name,
                                                新增: added.length
                                            });
                                        }
                                    }
                                }

                                lastSnapshot[key] = {
                                    ids: currIds,
                                    total: lane.stats.total,
                                    name: lane.name
                                };
                            }
                        }
                    } else if (msg.type === 'idle_marker') {
                        const elapsed = Math.floor((Date.now() - stats.startTime) / 1000);
                        console.log(`%c⏱️ idle_marker (运行${elapsed}s)`, 'color: #d29922;', {
                            泳道数: msg.lane_ids?.length || 0
                        });
                    }
                } catch (err) {
                    console.error('解析消息失败:', err);
                }

                // 每30条消息输出统计
                if (stats.messageCount % 30 === 0) {
                    const elapsed = Math.floor((Date.now() - stats.startTime) / 1000);
                    console.log('%c📈 统计报告', 'color: #58a6ff; font-weight: bold;', {
                        运行时间: `${Math.floor(elapsed/60)}分${elapsed%60}秒`,
                        总消息数: stats.messageCount,
                        正常新增: stats.normalAdds,
                        窗口滚动: stats.rollingCount,
                        滚动率: stats.rollingCount > 0
                            ? `${(stats.rollingCount/(stats.normalAdds+stats.rollingCount)*100).toFixed(1)}%`
                            : '0%'
                    });
                }

                return listener.call(this, event);
            };
            return originalAddEventListener.call(this, type, wrappedListener, options);
        }
        return originalAddEventListener.call(this, type, listener, options);
    };

    // 10分钟后输出最终报告
    setTimeout(() => {
        const elapsed = Math.floor((Date.now() - stats.startTime) / 1000);
        console.log('%c' + '='.repeat(60), 'color: #58a6ff;');
        console.log('%c🏁 最终验证报告', 'color: #3fb950; font-size: 16px; font-weight: bold;');
        console.log('%c' + '='.repeat(60), 'color: #58a6ff;');
        console.log('%c运行时间:', 'font-weight: bold;', `${Math.floor(elapsed/60)}分${elapsed%60}秒`);
        console.log('%c总消息数:', 'font-weight: bold;', stats.messageCount);
        console.log('%c正常新增:', 'font-weight: bold; color: #3fb950;', stats.normalAdds);
        console.log('%c窗口滚动:', 'font-weight: bold; color: ' + (stats.rollingCount > 0 ? '#f85149' : '#3fb950') + ';', stats.rollingCount);

        if (stats.rollingCount === 0) {
            console.log('%c\n✅ 修复成功！未检测到窗口滚动现象', 'color: #3fb950; font-size: 14px; font-weight: bold;');
        } else {
            console.log('%c\n⚠️ 仍存在窗口滚动，需进一步分析', 'color: #f85149; font-size: 14px; font-weight: bold;');
        }
        console.log('%c' + '='.repeat(60), 'color: #58a6ff;');
    }, 10 * 60 * 1000);

    console.log('%c✓ 监控脚本已启动，10分钟后输出最终报告', 'color: #3fb950;');
})();
```

### 步骤3: 观察指标（运行期间）

监控Console输出，注意：
- **正常情况**: 只有"✅ 正常新增"日志
- **异常情况**: 出现"🚨 窗口滚动"日志

**判断标准**:
- 窗口滚动次数 = 0 → ✅ 修复成功
- 窗口滚动次数 > 0 → ⚠️ 需进一步分析

---

## 三、服务器日志验证

### SSH到245服务器

```bash
ssh -p 25022 root@8.136.114.245
```

### 检查1: 服务版本

```bash
cat /opt/llm-gateway-go/releases/current/version.json
# 期望: "seq": "1251", "commit": "b5927030" 或 "ff28c65d"
```

### 检查2: 排序函数是否被调用

```bash
# 查找sortKeysByActivity相关日志
docker logs llm-gateway-go --since 30m 2>&1 | grep -i "sort.*activity"

# 如果修复生效，应该看到排序逻辑执行（可能无显式日志，但不应有错误）
```

### 检查3: 是否有维度队列读取

```bash
# 查看维度队列扫描
docker logs llm-gateway-go --since 30m 2>&1 | grep -i "dimension.*queue\|discover.*dimension"
```

### 检查4: 检查错误日志

```bash
# 查找可能的错误
docker logs llm-gateway-go --since 30m 2>&1 | grep -i "error.*sort\|failed.*activity\|pipeline.*exec"
```

**期望结果**:
- ❌ 没有 "pipeline exec failed" 错误
- ❌ 没有 "failed to sort by activity" 警告
- ✅ 如果有fallback，说明Pipeline失败，但系统已回退到字母排序（仍然稳定）

---

## 四、代码层面验证

### 检查1: 确认修复代码已部署

```bash
# 在245服务器上
cd /opt/llm-gateway-go/releases/current
strings llm-gateway-go | grep -i "sortKeysByActivity"

# 如果找到该字符串，说明新代码已包含
```

### 检查2: Redis维度队列验证

```bash
# 连接到Redis
docker exec -it pg-252-pg17 redis-cli -h 172.16.2.210

# 查看维度队列
SCAN 0 MATCH llmgw:live:dimension:* COUNT 100

# 检查某个队列的最新时间戳（score）
ZREVRANGE llmgw:live:dimension:vendor:minimax 0 0 WITHSCORES
```

**期望**:
- 能看到多个维度队列
- 每个队列有正常的timestamp score

---

## 五、预期结果对比

### 修复前（问题现象）

```
Console输出示例:
🚨 窗口滚动！{维度: "vendor", 泳道: "minimax", 移除: 8, 新增: 12}
🚨 窗口滚动！{维度: "provider", 泳道: "NVIDIA", 移除: 5, 新增: 7}
...
📈 统计报告 {窗口滚动: 15, 滚动率: "23.4%"}
```

### 修复后（预期）

```
Console输出示例:
✅ 正常新增 {维度: "vendor", 泳道: "minimax", 新增: 2}
✅ 正常新增 {维度: "model", 泳道: "gpt-4", 新增: 1}
...
📈 统计报告 {窗口滚动: 0, 滚动率: "0%"}
🏁 最终验证报告
✅ 修复成功！未检测到窗口滚动现象
```

---

## 六、如果仍有问题

### 场景A: 仍然检测到窗口滚动

可能原因：
1. **Pipeline失败** - Redis连接问题，fallback到字母排序但仍不稳定
2. **SCAN结果集太大** - 排序后的顺序在某些情况下仍不一致
3. **并发写入冲突** - 读取时恰好有新请求写入

**诊断步骤**:
```bash
# 查看是否有fallback日志
docker logs llm-gateway-go --since 30m 2>&1 | grep "lexicographic order"

# 检查Redis Pipeline性能
docker logs llm-gateway-go --since 30m 2>&1 | grep -i "pipeline\|timeout"
```

### 场景B: 排序导致性能问题

症状：CPU使用率上升，响应变慢

**诊断**:
```bash
# 查看服务资源使用
docker stats llm-gateway-go --no-stream
```

**解决方案**:
- 减少Pipeline batch size
- 添加排序结果缓存

---

## 七、回滚方案

如果修复未生效或引入新问题：

```bash
# 回滚到上一个版本
ssh -p 25022 root@8.136.114.245
cd /opt/llm-gateway-go
bash scripts/deploy-seamless.sh rollback 245
```

---

## 八、验证清单

- [ ] 登录245仪表盘成功
- [ ] 运行Console监控脚本（至少10分钟）
- [ ] 检查服务器版本为1251
- [ ] 查看服务器日志无错误
- [ ] 验证Redis维度队列存在
- [ ] 确认"窗口滚动"次数为0
- [ ] 观察stats.total数字稳定
- [ ] 记录最终验证结果

---

**验证负责人**: ___________
**验证时间**: ___________
**验证结果**: [ ] ✅ 通过  [ ] ⚠️ 部分通过  [ ] ❌ 失败
