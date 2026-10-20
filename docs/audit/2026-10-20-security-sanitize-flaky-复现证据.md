# `security/sanitize` 不稳定 —— 复现证据（2026-10-20）

> 本文件是**证据落盘**，供下一轮复核用；结论与处置见
> `docs/96小时内修订审计-20261007-1350.md` §十二 §12.5-11 与
> `docs/audit/2026-10-19-96h-审计轮-分支裁决与慢SQL.md` §3.13。

## 一句话

**失败用例名**：`TestSanitizeOffsetsAcrossMiddlewareInstances`
（`security/sanitize/offset_lock_test.go:48`，失败断言在 `:78`）。
**不是脱敏逻辑坏了** —— 是「并发测试的断言」与「实现侧 fail-closed 契约」**不匹配**。

## 复现命令（**全量落文件，不要 `tail`**）

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2
for i in $(seq 1 20); do
  go test ./security/sanitize/ -count=1 -v > /tmp/sanitize-flake/run-$i.log 2>&1
  ec=$?
  grep -c '^\s*--- FAIL' /tmp/sanitize-flake/run-$i.log
  [ $ec -ne 0 ] && { echo "抓到：run $i"; break; }
done
```

⚠ **别用 `tail -N` 截断** —— 上一轮正是因此 7 轮都没抓到失败名。

## 决定性片段（取自 run 12，原始日志 146 KB，此处只留必要部分）

伴随日志：

```
2026/10/20 12:50:30 WARN sanitize_middleware: sanitize failed component=sanitize_middleware error="acquire sanitize offsets: sanitize offsets lock busy" session_id=session-two-instances
```

断言输出：

```
        	            	expected: []int{200, 200}
        	            	actual  : []int{200, -503}
        	            	
        	            	Diff:
        	            	--- Expected
        	            	+++ Actual
        	            	@@ -2,3 +2,3 @@
        	            	  (int) 200,
        	            	- (int) 200
        	            	+ (int) -503
        	            	 }
        	Test:       	TestSanitizeOffsetsAcrossMiddlewareInstances
--- FAIL: TestSanitizeOffsetsAcrossMiddlewareInstances (0.96s)
=== RUN   TestSanitizeOffsetsBusyOrRedisFaultStopsDispatch
```

## 读法

- 期望 `[]int{200, 200}`，实得 `[]int{200, -503}`。
- `-503` 的来历：测试里 `results[i] = -status`（`!called || 前向体含明文` 时取负号），
  而 **status 本身是 503**（`http.StatusServiceUnavailable`）⇒ **实现侧是 fail-closed「停止分发」**。
- `acquire sanitize offsets: sanitize offsets lock busy` ⇒ **Redis offsets 锁没抢到**。

## 机理（读实现坐实，非推测）

| 事实 | 位置 | 读数 |
|---|---|---|
| 抢锁 | `security/sanitize/smart_sani_guard.go:426` `acquireOffsetsLock` | `SetNX(key, tok, leaseTTL)` |
| 租约 | `defaultOffsetLeaseTTL` | **15 s** |
| 失败重试 | 同函数 | **3 次 × 50 ms ≈ 100 ms 等待窗口** |
| 抢不到 | 同函数末行 | `errors.New("sanitize offsets lock busy")` |
| 测试形态 | `security/sanitize/offset_lock_test.go:48` | 两个 middleware 实例**并发处理同一 tenant/session**，连跑 12 轮，各抢**同一把**锁 |

⇒ 持锁者若未在 100 ms 内释放，等待方就**必然**拿到 `lock busy` ⇒ 503。

**旁证（实现是对的）**：`TestSanitizeOffsetsBusyOrRedisFaultStopsDispatch` 明确断言
「锁被占 ⇒ 503 且不转发、且不落映射」⇒ **"lock busy ⇒ 503" 是被另一个测试钉住的契约**。

⇒ **真缺陷在「测试与实现的契约不匹配」**：
① 实现侧等待窗口（100 ms）相对租约（15 s）严重不成比例；
② 测试侧断言「并发两实例**必然**都 200」隐含假设「持锁 < 100 ms」，没给重试窗口留余量。

## 复现率

本轮 **12 次整包连跑，第 12 次红**（前 11 次全绿）⇒ **≈ 1/12**；
抓取期间宿主 **load 200~400** ⇒ 负载越高、持锁越久、越容易撞上 100 ms 窗口。

## 处置

**与本轮改动零重叠**（`security/sanitize` 不在本轮 15 个文件内，
且 `git diff <上次合并点> origin/main -- security/` 输出为空）⇒ **本轮不擅自改**。
两条待拍板的修法见 §3.13：(a) 实现侧等待窗口与租约按比例挂钩；(b) 测试侧显式承认 fail-closed 契约。

## ⚠ 别误读成红的东西

该包整包输出里另有大量 `t.Log` 诊断，**它们不是失败**：
- `encoded_output_blindspot_test.go`：**已登记**的编码串盲区（对照臂正常遮蔽 ⇒ 不是夹具坏了）；
- `deployment_pattern_config_test.go`：**decision 107 待定**的镜像内无 `configs/`（`/api/admin/sensitive-words/reload` 只能答 500）。
