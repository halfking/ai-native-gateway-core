# 158 / R89-BT：附件清理按 mtime 删文件，而去重命中从不更新 mtime —— 单个 blob 平均被引用 133 次

> 日期：2026-10-01
> 目标：objective 覆盖总览第 2 项「上传文件解析、存储、版本管理」（台账长期标「部分」，证据只有 69/70 号）
> 结论：**内容寻址去重 + 按 mtime 清理 = 会删掉仍被近期请求引用的 blob。机制已从代码坐实，爆炸半径已量化。**
> 定级：**新增 P2 / 待裁决 64**（潜伏：机制成立 ✅、本库今天不发生 ❌）。**未改动任何生产代码或数据库。**

---

## 一、机制（三段链条，每段都已在代码里坐实）

### ① 去重命中**从不更新 mtime**

`domains/attachments/storage.go:314-327`：

```go
// 去重：检查文件是否已存在
exists, err := backend.Exists(ctx, relPath)
...
if exists {
	// 文件已存在，跳过写入
	deduped = true          // ← 直接返回，没有任何 touch
} else {
	backend.Save(ctx, relPath, content)
}
```

**全包 `Chtimes` / `utimes` 命中数 = 0**（已 grep 复核）。
⇒ **blob 的 mtime 永远等于「首次写入时间」，与最后一次被引用无关。**

### ② 清理端点**纯按 mtime 删，零引用检查**

`admin/data_lifecycle_attachments_filesystem.go:219-249`：

```go
err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
	...
	// 按修改时间判断
	if info.ModTime().Before(cutoffTime) {
		...
		if rmErr := os.Remove(path); rmErr == nil { ... }
	}
	return nil
})
```

**没有任何一处 join/查询 `request_attachments`**（该表正是记录「哪个请求引用了哪个 hash」的地方）。
注释 `:198-201` 自己也承认：*「We don't have a per-file tenant mapping (files are content-addressed by hash)」*。

### ③ 阈值**没有来自数据的下界**

`:170` 唯一的守卫是 `if req.OlderThanDays <= 0 { ... }`。
**管理员可以传 1**——而本库实测的引用跨度是 **14 天**（见 §二）。

### ⇒ 组合后果

> 请求 A（第 0 天）写入 blob H（mtime = 第 0 天）→ 请求 B…N 在第 0–14 天持续复用 H（去重命中，mtime 不动）
> → 管理员在第 8 天以 `OlderThanDays=7` 执行清理 → **H 被删除**
> → **第 8–14 天的请求仍持有指向 H 的 attachment 元数据，出站取附件必然失败。**

⚠️ 这是**内容寻址去重的经典反面**：去重让「一份文件」与「N 个引用者」解耦，
而 mtime 清理仍假设「文件老 = 没人要了」。

---

## 二、爆炸半径（真库实测，§33 先量化再定级）

```
request_attachments  10,652 行
distinct hash            80 个   ← 去重后只剩 80 份文件
平均复用           133.15 倍/份
最大单份引用数      1,155 次   (835b5448…, 2026-09-10 07:26 → 23:40)
引用跨度 > 0 的 blob    2 个，其中 1 个跨度达 14 天
```

⚠️ **测量用的是 `request_attachments.created_at`（每次引用的行创建时间），
不是文件 mtime** —— 这是**正确的尺子**（§46）：
它独立于 mtime，直接证明「这份文件在 14 天里一直被人引用」。

⇒ **删掉任意 1 个 blob ⇒ 平均 133 个请求的附件引用变悬空，最坏 1,155 个。**
⇒ **1 个 blob 的引用跨度（14 天）已超过任何合理的 `OlderThanDays` 默认值**（该参数无默认值，只能由管理员现填）。

---

## 三、为什么是 P2 而不是 P1：**潜伏，两栏写**

| | 事实 |
|---|---|
| ✅ **机制成立** | 上述三段链条全部在代码中坐实，含「无 touch」「无引用检查」「无下界」三个可复核点 |
| ✅ **规模已量化** | 133× 平均复用 / 1,155× 最坏 / 14 天跨度 |
| ❌ **本库今天不发生** | `audit_attachments_filesystem_cleanup` **实测 0 行** ⇒ **该清理端点在本环境从未被执行过** |
| ❌ **无法在本机证实悬空行** | `./data/attachments` 在本机为**空目录**（本机无网关进程，真实文件在远端实例，§44③ 归属边界） |

⇒ **不报 P1**（§33：后果机制已证但**实际发生未证**）。
⇒ **报 P2 的理由不是「可能出事」，而是「护栏缺失且补起来很便宜」**——
`OlderThanDays` 只要设成 1 就会立刻触发，而当前守卫只挡 `<= 0`。

---

## 四、与已登记的 R87-j-1 的关系：**这是它未被守护的镜像**

`domains/attachments/cleanup_wiring_test.go`（**2026-10-01 07:57 修改**）里有一道**主动守护**：

- `TestDeleteOlderThanHasNoProductionCallerYet` —— 断言 `Repository.DeleteOlderThan` **零生产调用方**，
  且失败信息写着 `FIXME(R87-j-1) 已失效：DeleteOlderThan 现在有生产调用方`。
  ⇒ **这是「有人把它接上去我就报警」的绊线**，属 §41 桶④ 标杆形态。

| | 行侧（`request_attachments`） | **文件侧（blob 文件）** |
|---|---|---|
| 缺口 | 零调用方 ⇒ 行表无界增长 | **mtime 清理会删掉仍被引用的文件** |
| 严重性 | 容量/性能 | **数据丢失（引用悬空）** |
| 是否有守护 | ✅ 有绊线测试 | ❌ **无任何测试或注释提示** |

⚠️ **同一个子系统里，危害较小的那一侧被主动守护，危害更大的这一侧完全没人管。**
这是本轮最值得记录的一条。

---

## 五、诚实边界

- **未改动任何生产代码或数据库**（全部只读；真库只跑 SELECT）。
- ⚠️ **悬空行（行指向已删文件）在本机无法证实也无法证伪** —— 文件在远端实例。
  **需要运维在远端执行一次只读核对**：
  `SELECT storage_path FROM request_attachments WHERE created_at > now() - interval '7 days'`
  与远端附件目录求差集。**在那之前，本文只主张「机制成立 + 规模已量化」，不主张「已经发生」。**
- ⚠️ **未逐个核对 4 个存储后端**（local / oss / s3 / cloudreve）的清理路径——
  本轮只读了 **local**（因为 `os.Remove` + `ModTime` 只在 local 路径）；
  **OSS/S3/Cloudreve 的生命周期策略未核**（它们可能用各自的对象存储 TTL，语义不同）。
- 真库归属（§44③）：本机无网关进程，所有真库数字只对本库成立。
- **未核**：出站取附件时 blob 缺失的实际降级行为（是整请求失败，还是退化为占位符）——**这决定后果的严重度**，本轮如实登记为待查。

---

## 六、建议（待裁决 64，P2）

**按「先便宜后完整」排序，三件都只改运维护栏，不改客户端观感：**

1. **给 `OlderThanDays` 加数据下界**（最小改动，直接堵住最危险的用法）：
   拒绝小于「`request_attachments` 实际最大引用跨度」的取值，或要求管理员显式二次确认。
2. **加一道引用检查**（治本）：删除前把候选 `storage_path` 与
   `SELECT DISTINCT storage_path FROM request_attachments WHERE created_at > cutoff` 求差集，只删真无引用的。
   ⇒ 顺带**把行侧也一起判掉**，两件事一次解决。
3. **补一道守护测试**（防回归）：仿照 `cleanup_wiring_test.go` 的形态，
   断言清理路径**必须**先做引用差集；有人删掉引用检查就红。

⚠️ **修法 1、2 会改变清理的触发条件 ⇒ 需要产品/运维先答「附件保留期应该以『最后引用时间』还是『首次写入时间』为准」**——
**这是产品口径问题，不是实现问题，按纪律不擅自动手。**
