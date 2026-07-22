# 修复泳道跳变 - 延长全量快照推送间隔

**修复方案**: B - 后端延长推送间隔
**优先级**: 快速修复，立即生效
**影响**: 跳变频率从每 30 分钟降低到每 2 小时

---

## 修改内容

**文件**: `admin/live_stream_sse.go`

**行号**: 238-239

**修改前**:
```go
if c.SnapshotRefreshInterval <= 0 {
    c.SnapshotRefreshInterval = 30 * time.Minute  // ← 每 30 分钟推送全量快照
}
```

**修改后**:
```go
if c.SnapshotRefreshInterval <= 0 {
    // 2026-07-19: 从 30 分钟延长到 2 小时，减少前端泳道跳变频率。
    // 全量快照推送会导致前端重建所有泳道，视觉上出现跳变。
    // 2 小时与 Redis TTL 对齐，在数据同步和用户体验间取得平衡。
    // 可通过环境变量 LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_REFRESH_INTERVAL 覆盖。
    c.SnapshotRefreshInterval = 2 * time.Hour  // ← 改为 2 小时
}
```

---

## 理由

1. **对齐 Redis TTL**: Redis ZSET 保留 2 小时数据，推送间隔也设为 2 小时，避免数据不一致
2. **减少跳变**: 从每 30 分钟跳变一次降低到每 2 小时
3. **可配置**: 保留环境变量覆盖能力，紧急情况可临时调整
4. **零风险**: 只是延长间隔，不改变推送逻辑

---

## 预期效果

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| 跳变频率 | 每 30 分钟 | 每 2 小时 |
| 用户感知 | 频繁跳变，体验差 | 偶尔跳变，可接受 |
| 数据同步 | 实时 | 最多延迟 2 小时 |

---

## 验证方法

### 部署后验证

```javascript
// 在前端 Console 运行，观察 2 小时
const es = new EventSource('/api/admin/live-stream');
let lastRefresh = Date.now();
es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'snapshot_refresh') {
        const now = Date.now();
        const interval = (now - lastRefresh) / 60000;  // 分钟
        console.log(`🔄 全量快照推送，距上次 ${interval.toFixed(1)} 分钟`);
        lastRefresh = now;
    }
};
```

**期望结果**: 第二次 `🔄` 出现时，间隔约为 **120 分钟**

---

## 回滚方案

如果需要回滚（极低概率）:

```go
// 改回原值
c.SnapshotRefreshInterval = 30 * time.Minute
```

或通过环境变量临时覆盖:
```bash
export LLM_GATEWAY_LIVE_STREAM_SNAPSHOT_REFRESH_INTERVAL=30m
systemctl restart llm-gateway-go
```

---

## 后续优化

本修复是**临时缓解**，长期应实施：
- **方案 A**: 前端平滑合并（彻底解决跳变）
- **方案 C**: 后端智能推送（只在必要时推送全量）

详见：`docs/fixes/2026-07-19-swimlane-jump-analysis.md`
