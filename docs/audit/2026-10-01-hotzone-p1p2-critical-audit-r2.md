# hotzone P1+P2 批判式审计第二轮（2026-10-01）

- 审计对象：docs/storage/2026-09-24-hotzone-dual-mode-plan.md 的 P1(H1)+P2(H4) 交付在当前 HEAD 的实况（首轮见 2026-09-30-hotzone-p1p2-critical-audit.md，彼后另有 21/20/36 轮加固与 P3/P4/P5 落地）
- 方法：通读 cache_v2_file.go / hot_zone_trimmer.go / storage_mode_init.go 接线全文（非锚点抽查），逐疑点回溯提交考古；发现即修、修必带测试
- 状态：4 项发现全部处置（3 修 1 订正注释），回归全绿，已推送

---

## 一、结论

P1+P2 交付主体成立（首轮审计的验收矩阵仍有效），但第二轮全文通读发现
**两个被测试或文档固化的行为缺陷**（G1/G2）与两处卫生问题（G3/G4）。
关键教训与首轮 F1 同型：**"测试钉死的行为"不等于"正确的行为"**——G1 正是
被 `TestHotZoneTrimmerTempFilesIgnored` 用过期 tmp 文件钉死的永久豁免。

## 二、发现与处置

| # | 严重度 | 发现 | 处置 |
|---|---|---|---|
| G1 | P2 | **孤儿临时文件在热区无界累积**。三重叠加：①FileCache 写入用 `.{sessionID}.tmp-{rand}`，崩溃后不自我回收（cache_v2_file.go 头注明言"交由运维处理"）；②HotZoneTrimmer `isTempFile`（contains ".tmp"）全量豁免临时文件；③临时文件不进 `onDiskBytes` 配额口径。结果：kill -9/断电遗留的 `.tmp` 既不进配额、永不过期，恰好在"磁盘压力泄压阀"里无界堆积。原测试用 -2h 的过期 tmp + 1h retention 把该行为**钉死为期望** | 修：临时文件豁免配额相不变（在飞写入不被盘压误删），但**参与过期清扫**（mtime 早于 cutoff = 确证孤儿，在飞写入存活毫秒级、spec Min retention=1h，安全裕量充分）；retention 未启用时同步跳过（fail-safe 一致）。`TestHotZoneTrimmerTempFilesIgnored` 改用新鲜 tmp 重写语义，新增 `TestHotZoneTrimmerStaleTempFilesSwept` 钉死孤儿回收 |
| G2 | P3 | **retention 热重载对 L1.5 读侧不生效**。full 热区装配把 FileCache TTL 与 retention 设为同参（构造期），但 reload closure 只回写 `ResizeMax(maxBytes)`——`ttl` 构造后钉死。运行期把 `storage.hotzone_retention_hours` 调大后：删侧（trimmer）按新 retention 保留，读侧（Get）仍按旧 TTL 自删（Get→removeExpiredLocked），扩容对缓存读路径无效。deployment-guide「= L1.5 TTL 同参数」的声明在此场景失真。缩容方向纯语义无损（读侧滞后=miss 回源），数据无损 | 修：FileCache 增 `SetTTL`（内部自持 fc.mu、d<=0 拒绝、nil-safe，与 ResizeMax 同款守卫），reload closure 在 `enabled && resizeInHotzone` 时同步调用；`TestInitStorageModeHotZoneResizeMaxWiring` 扩断言 TTL 收敛 7h；新增 `TestFileCacheSetTTLHotReload` |
| G3 | P4（卫生） | **fakeFileInfo/fileInfoShim 死代码**：Get 损坏 JSON 路径改为真 Stat 后（见该处注释），零调用点遗留；且 fileInfoShim 内嵌 nil `os.FileInfo` 是"少覆盖一个方法即 nil panic"的地雷 | 删除两个类型与构造函数，修正残留注释引用 |
| G4 | P4（卫生） | **注释互斥**：ResizeMax 注释（20 轮订正版）称"缩容交由 trimmer 执行描述了不存在的机制"，而 startHotZoneTrimmer 注释称"缩容交由本轮 trimmer 执行（H4 §改动3）"——两者都对但各说半截：20 轮订正时 P3 未接线故为真，P3 落地后机制已存在 | 订正为两段式口径（cache 子树在热区内=trimmer 配额相；热区外=写时淘汰/TTL），保留历史订正缘由 |
| — | 留档不修 | Get 索引过期分支在 `removeExpiredLocked` 复检磁盘"未过期"时仍摘索引（:230）——fail-open 自愈（下次 Get 走 Stat 回填），最坏多一次 Stat，不值得为它改锁内返回值签名 | 留档 |
| — | 留档不修 | Get 索引 miss 回填（:246）不累加 sizeUsed 的隐性不变量（该文件必经锁内 Set 记账，再加即双计）——行为正确但前提未成文，补注释固化（同提交） | 补注释 |
| — | 复核维持 | lastDeletedFiles/lastFreedBytes 只写不读：与 cache_trimmer/bodies_trimmer 三处一致的包内惯例，观测经 slog 承载，不为 hotzone 单独破例 | 维持 |

## 三、回归证据（本轮，主树定向跑 + 全量）

```
go test -race -count=1 ./bg/ -run "TestHotZoneTrimmer" -v
  → 14/14 PASS（含改写的 TempFilesIgnored、新增 StaleTempFilesSwept）
go test -race -count=1 ./domains/session/v2/ -run "TestFileCache" -count=1
  → ok（含新增 TestFileCacheSetTTLHotReload）
go test -race -count=1 ./cmd/gateway/ -run "TestInitStorageModeHotZoneResizeMaxWiring" -v
  → PASS（TTL 收敛 7h 断言入测）
9 项 lite 套件 + bg/config/v2/cmd 全量 + go vet：见提交序列回归记录，全绿
```

## 四、遗留风险与挂账（增量）

1. **G1 的配额盲区仍在**：孤儿临时文件过期前（默认 7h）不进配额口径——崩溃风暴
   场景下 7h 窗口内磁盘占用可超预算。接受理由：单 tmp ≤ 单条 state 体积，风暴
   概率低；配额相删除在飞 tmp 的风险不对称。运维侧如需立即回收可手动删 `.tmp`。
2. 首轮审计遗留全部维持：F5 全局钳制 owner 未决、索引外部删除漂移按键自愈、
   `hotzone_enabled=false` 运行期语义=停清不卸载、部署级验收挂账（245/154 显式
   STORAGE_MODE=full、F3/F4/F5 对账、R-9 蓝绿真机实证）。
3. **方法论**：本轮两处主发现（G1/G2）都藏在"跨文件协作缝隙"里——单文件视角的
   验收矩阵（首轮做法）抓不住，必须全文通读接线两端的契约是否互恰。三轮审计
   各有斩获的根因是：每一轮的审计方法都对上一轮有增量（锚点核对→全文通读→
   跨文件契约核对），而非重复同一动作。
