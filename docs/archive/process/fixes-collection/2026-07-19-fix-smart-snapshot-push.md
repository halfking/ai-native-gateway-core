# 修复泳道跳变 - 智能全量快照推送

**修复方案**: C - 后端智能推送
**优先级**: 中期优化，彻底解决
**影响**: 只在真正需要时推送全量快照，避免无意义的跳变

---

## 设计思路

**当前问题**:
- 每 2 小时无条件推送全量快照
- 即使数据没什么变化，也会触发前端重建泳道

**优化方案**:
- 推送前比对缓存快照和新快照
- 只有在**数据显著变化**时才推送全量快照
- 减少无意义的推送，降低前端重建频率

---

## 判断标准

满足以下**任一条件**才推送全量快照：

1. **首次推送**（缓存快照不存在）
2. **总请求数变化 > 20%**
3. **泳道数量变化**（新增或删除泳道）
4. **泳道顺序变化**（Top 5 泳道排序改变）

---

## 实现方案

### 1. 新增辅助函数

**文件**: `admin/live_stream_sse.go`

```go
// needsFullRefresh 判断是否需要推送全量快照。
// 只有在数据显著变化时才返回 true，避免无意义的推送导致前端跳变。
func needsFullRefresh(cached, fresh *LiveStreamSnapshot) bool {
	if cached == nil {
		return true // 首次推送
	}

	// 判断 1: 总请求数变化 > 20%
	oldTotal := cached.Summary.Total
	newTotal := fresh.Summary.Total
	if oldTotal > 0 {
		diff := float64(abs(newTotal - oldTotal))
		threshold := float64(oldTotal) * 0.2
		if diff > threshold {
			slog.Debug("snapshot refresh needed: total count changed",
				"old", oldTotal, "new", newTotal, "diff_pct", diff/float64(oldTotal)*100)
			return true
		}
	}

	// 判断 2: 泳道数量变化（任一维度）
	for _, dim := range []string{"vendor", "provider", "model"} {
		oldLanes := cached.Dimensions[dim]
		newLanes := fresh.Dimensions[dim]
		if len(oldLanes) != len(newLanes) {
			slog.Debug("snapshot refresh needed: lane count changed",
				"dimension", dim, "old_count", len(oldLanes), "new_count", len(newLanes))
			return true
		}
	}

	// 判断 3: Top 5 泳道顺序变化（任一维度）
	for _, dim := range []string{"vendor", "provider", "model"} {
		oldLanes := cached.Dimensions[dim]
		newLanes := fresh.Dimensions[dim]
		topN := 5
		if len(oldLanes) < topN {
			topN = len(oldLanes)
		}
		if len(newLanes) < topN {
			topN = len(newLanes)
		}

		for i := 0; i < topN; i++ {
			if oldLanes[i].ID != newLanes[i].ID {
				slog.Debug("snapshot refresh needed: top lane order changed",
					"dimension", dim, "position", i,
					"old_id", oldLanes[i].ID, "new_id", newLanes[i].ID)
				return true
			}
		}
	}

	// 数据变化不显著，跳过推送
	slog.Debug("snapshot refresh skipped: no significant changes")
	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
```

### 2. 修改 pushFullSnapshots

**文件**: `admin/live_stream_sse.go:548-603`

**修改前**:
```go
func (h *LiveStreamSSEHub) pushFullSnapshots() {
	// ... 读取 snapshot

	// 无条件推送
	env := LiveStreamEnvelope{
		Type:      "snapshot_refresh",
		Timestamp: time.Now(),
		Snapshot:  snapshot,
	}
	h.fanOut(env)
}
```

**修改后**:
```go
func (h *LiveStreamSSEHub) pushFullSnapshots() {
	// ... 读取 snapshot

	// 2026-07-19: 智能推送 - 只在数据显著变化时推送
	scope := newLiveStreamScope(entry.tenantID, entry.isSuper)
	h.cachedSnapshotMu.RLock()
	cached := h.cachedSnapshot[scope.cacheKey]
	h.cachedSnapshotMu.RUnlock()

	var oldSnapshot *LiveStreamSnapshot
	if cached != nil {
		oldSnapshot = cached.snapshot
	}

	if !needsFullRefresh(oldSnapshot, snapshot) {
		// 数据变化不显著，跳过推送
		continue
	}

	// 数据显著变化，推送全量快照
	h.cachedSnapshotMu.Lock()
	h.cachedSnapshot[scope.cacheKey] = &cachedSnapshotEntry{
		snapshot:    snapshot,
		lastRefresh: time.Now(),
	}
	h.cachedSnapshotMu.Unlock()

	env := LiveStreamEnvelope{
		Type:      "snapshot_refresh",
		Timestamp: time.Now(),
		Snapshot:  snapshot,
	}
	h.fanOut(env)
}
```

---

## 预期效果

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| 流量稳定期 | 每 2 小时推送 | **不推送**（跳过） |
| 流量突增/突降 | 每 2 小时推送 | **立即推送**（变化 > 20%） |
| 新泳道出现 | 每 2 小时推送 | **立即推送**（泳道数变化） |
| 泳道排序变化 | 每 2 小时推送 | **立即推送**（Top 5 变化） |

**实际跳变频率**: 从每 2 小时降低到**接近 0**（只在真正需要时）

---

## 测试方案

### 单元测试

```go
func TestNeedsFullRefresh(t *testing.T) {
	// 测试 1: 首次推送
	assert.True(t, needsFullRefresh(nil, &LiveStreamSnapshot{}))

	// 测试 2: 总请求数变化 < 20%
	old := &LiveStreamSnapshot{Summary: LiveStreamSummary{Total: 100}}
	new := &LiveStreamSnapshot{Summary: LiveStreamSummary{Total: 115}}
	assert.False(t, needsFullRefresh(old, new))

	// 测试 3: 总请求数变化 > 20%
	new = &LiveStreamSnapshot{Summary: LiveStreamSummary{Total: 130}}
	assert.True(t, needsFullRefresh(old, new))

	// 测试 4: 泳道数量变化
	old = &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"vendor": {{ID: "openai"}, {ID: "anthropic"}},
		},
	}
	new = &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"vendor": {{ID: "openai"}, {ID: "anthropic"}, {ID: "google"}},
		},
	}
	assert.True(t, needsFullRefresh(old, new))

	// 测试 5: Top 5 顺序变化
	old = &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"vendor": {{ID: "openai"}, {ID: "anthropic"}},
		},
	}
	new = &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"vendor": {{ID: "anthropic"}, {ID: "openai"}},
		},
	}
	assert.True(t, needsFullRefresh(old, new))
}
```

### 集成测试

部署后在生产环境观察：

```bash
# 查看日志，确认智能推送生效
tail -f /var/log/llm-gateway-go/llm-gateway-go.log | grep "snapshot refresh"

# 期望看到：
# - "snapshot refresh skipped: no significant changes" （大部分时候）
# - "snapshot refresh needed: total count changed" （流量突变时）
# - "snapshot refresh needed: lane count changed" （新泳道出现时）
```

---

## 部署步骤

1. 编译测试
2. 部署到 154 生产服务器
3. 监控 24 小时
4. 确认跳变消失

---

## 回滚方案

如果出现问题，可以：

1. **临时禁用智能推送**（改为无条件推送）:
   ```go
   // 临时注释掉智能判断
   // if !needsFullRefresh(oldSnapshot, snapshot) {
   //     continue
   // }
   ```

2. **或者回滚到上一个版本**:
   ```bash
   git revert <commit-hash>
   bash scripts/deploy-154.sh
   ```

---

## 监控指标

新增以下日志监控：

- `snapshot refresh skipped` 计数（应该占大多数）
- `snapshot refresh needed` 计数及原因分布
- 前端跳变报告（用户反馈）

---

**结论**: 方案 C 结合方案 B，可以将泳道跳变频率降低到**接近 0**，同时保持数据实时同步。
