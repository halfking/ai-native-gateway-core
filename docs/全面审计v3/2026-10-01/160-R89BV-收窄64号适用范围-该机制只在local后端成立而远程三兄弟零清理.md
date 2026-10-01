# 160 / R89-BV：收窄 64 号的适用范围 —— 该机制只在 local 后端成立，而远程三兄弟零清理

> 日期：2026-10-01
> 承接 158 号 §六 明确登记的待核项：**「未核 OSS/S3/Cloudreve 三个后端的生命周期策略」**
> 结论：**① 64 号的机制只在 `local` 后端成立（范围收窄）；② 远程三后端零生命周期配置 ⇒ 附件无限累积；
> ③ 清理端点无后端守卫 ⇒ 远程后端下「静默成功报 0」，构成运维误判风险。**
> 定级：**64 号维持 P2 但范围收窄；新增 P3 / 待裁决 65（③）**。**未改动任何生产代码或数据库。**

---

## 一、范围收窄：158 号的机制**只在 `local` 后端成立**

**依据（两条可复核）**：

1. **清理端点的实现是纯本地文件系统语义**：
   `admin/data_lifecycle_attachments_filesystem.go:176-249`
   —— `EffectiveAttachmentDir()` → `filepath.Abs` → `filepath.WalkDir` → **`os.Remove`** → 判据是 **`info.ModTime()`**。
   `os.Remove` 与 `ModTime` 都是本地文件系统概念，**在对象存储上不存在对应语义**。
2. **OSS / S3 / Cloudreve 三个后端没有任何生命周期代码**：
   对 `storage_backend_{oss,s3,cloudreve}.go` 检索
   `Lifecycle|ExpirationDays|TTL|expire|Prefix.*00/|SetBucket|retain|清理` ⇒ **命中 0**。

⇒ **「去重命中不更新 mtime + 按 mtime 删 + 无引用检查」这条链，
只在 `local` 后端上闭合。**
⇒ **64 号的受影响面 = 使用 `local` 后端的部署**（默认单机/本地开发形态）；
**对象存储形态下该缺陷不成立。**

⚠️ **这次收窄与 153 号同族**：**158 号报的是「机制成立」，但没问「它在哪种部署形态下成立」**——
**「代码里存在」与「该路径在目标部署上会被走到」是两件事**（§30 的部署形态版本）。

---

## 二、镜像缺口：远程三后端**零清理**

把 §51 的「发现一侧缺口要问镜像面」用在这里，得到的是**方向相反的另一个缺口**：

| 后端 | 附件清理 | 后果 |
|---|---|---|
| `local` | ✅ 有（`handleAttachmentFilesystemCleanup`） | ⚠️ **会误删仍被引用的文件（64 号）** |
| `oss` / `s3` / `cloudreve` | ❌ **零生命周期配置、零清理代码** | ⚠️ **附件无限累积，成本无上界** |

⇒ **两边都有问题，方向相反：一个是删太多，一个是永不删。**
⇒ ⚠️ **本轮只证到「网关侧没有清理代码」**；
**⚠️ 部署侧是否配置了对象存储自己的 bucket lifecycle（OSS/S3 规则、Cloudreve 保留策略）本轮未核**——
**若部署侧配了，第二个缺口就不成立。** 如实登记为待查，不发条目。

---

## 三、新发现：清理端点**无后端守卫**，远程后端下静默成功

`data_lifecycle_attachments_filesystem.go` 全文**没有任何一处检查当前激活的是哪个存储后端**：

```go
attachmentDir := EffectiveAttachmentDir()      // :176  不问后端
absDir, err := filepath.Abs(attachmentDir)     // :178
...
err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return nil        // :220-221  ← 把「目录不存在」这个错误整个吞掉
	}
	...
})                                            // :249
```

**当后端是 OSS/S3/Cloudreve 时**（附件不在本地目录里）：

1. 目录通常不存在 ⇒ `WalkDir` 调用一次回调并传入 err；
2. 回调 `:221 return nil` ⇒ **`WalkDir` 自身返回 `nil`**；
3. 于是 `err == nil` ⇒ `:297` 的 `resp.Error` 字段**保持为空**；
4. `:301 writeJSON(w, http.StatusOK, resp)` ⇒ **返回 200 + `{"files_deleted":0, "error":""}`**。

⇒ **管理员看到的是「清理完成，0 个文件被删」，而不是「这个端点管不到你现在的后端」。**

⚠️ 这是一个**运维误判风险**：`0` 在两种完全不同的情形下长得一模一样——
「确实没有过期文件」与「根本没检查到任何文件」。
**这正是本会话反复出现的「未定义不可编码进 0/NULL」家族在 HTTP 响应上的形态**（§43/§36 同源）。

---

## 四、诚实边界

- **未改动任何生产代码或数据库**（真库只跑过 SELECT，本轮未查库）。
- ⚠️ **未核部署侧的对象存储生命周期配置**（OSS/S3 bucket rules、Cloudreve 保留策略）——
  **§二 的第二个缺口在部署侧配置了的前提下不成立**，本轮无法从仓内判断。
- ⚠️ **未核 `EffectiveAttachmentDir()` 在远程后端下返回什么**——
  它是否指向一个**存在但为空**的目录（那 WalkDir 连回调都不触发，结果同样是 0），本轮未读该函数。
  ⚠️ 两种情形的外部表现相同，**结论不受影响**，但登记为未核。
- **真库归属（§44③）**：本机无网关进程，本轮结论只对代码成立。

---

## 五、建议（待裁决 65，P3）

针对 §三 的静默成功，**最小改动**是让响应能区分两种 0：

1. 响应增加 `backend` 与 `scanned: true/false` 字段；
   若当前后端非 `local` ⇒ 直接返回 4xx 或在 `error` 字段写明「该端点只适用于 local 后端」；
2. 或最省事：入口处判 `if 活跃后端 != local { writeError(400, "filesystem cleanup only applies to local backend") ; return }`。

⚠️ **不擅自动手的原因**：改的是**管理端响应契约**，可能已有运维脚本依赖当前字段形状
⇒ 需产品/运维确认后再动。**这属于对外可见的行为变更，按纪律登记待裁决。**

**对 64 号的建议不变**（见 158 号 §六），但**适用范围应补一句「仅 local 后端」**。
