# 162 / R89-BX：收掉 160 号最后一条待核 —— `EffectiveAttachmentDir()` 与后端无关，65 号结论确认成立

> 日期：2026-10-01
> 承接 160 号 §四 登记的未核项：**「未核 `EffectiveAttachmentDir()` 在远程后端下返回什么」**
> 结论：**它完全不看当前是哪个存储后端** ⇒ 远程后端下必然指向一个与附件无关的本地目录
  ⇒ **待裁决 65 的结论确认成立，不是修正。** **本轮不新增待裁决。**
> **未改动任何生产代码或数据库。**

---

## 一、函数本体（`admin/storage_config.go:470-482`）

```go
// EffectiveAttachmentDir 返回当前生效的附件目录。
// 优先级：DB override > env > 默认 ./data/attachments
// 2026-07-02: 导出供 data_lifecycle_attachments_filesystem 等管理端复用，
// 收口分散的 os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR") 读取。
func EffectiveAttachmentDir() string {
	if override := readStringSetting("storage.attachment_dir_override"); override != "" {
		return override
	}
	if env := os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR"); env != "" {
		return env
	}
	return "./data/attachments"
}
```

**三行取值来源里，没有一行涉及 `oss` / `s3` / `cloudreve` / `local`。**
它返回的**永远是本地路径字符串**。

---

## 二、由此可以把 160 号的表述说得更准

160 号当时谨慎地写：「若当前后端是远程，目录通常不存在 ⇒ WalkDir 调一次回调并吞掉 err ⇒ 200 + 0」，
并登记「**未核该函数在远程后端下返回什么**」。

**现在可以确定**：

| 部署形态 | `EffectiveAttachmentDir()` 返回 | 附件实际在哪 | 清理端点的行为 |
|---|---|---|---|
| `local` | 本地目录（默认 `./data/attachments`） | **就在那里** | ✅ 真的在删（⇒ 64 号的误删风险成立） |
| `oss`/`s3`/`cloudreve` | **仍然是本地目录字符串**（该函数不看后端） | 对象存储里 | ⚠️ **必然指向一个与附件无关的目录** |

⇒ **两种可能，且都与 160 号的结论一致**：
- 目录**不存在** ⇒ `:221 return nil` 吞掉 WalkDir 的首个错误 ⇒ `err == nil` ⇒ `200 {"files_deleted":0,"error":""}`；
- 目录**存在但为空/无关** ⇒ WalkDir 正常走完、删 0 个 ⇒ **响应形状完全相同**。

⚠️ **这两种情形对运维来说都表现为同一句「清理完成，0 个」**，
**而实际上这个端点从头到尾没有看过一个真实的附件。**

**⇒ 待裁决 65 的结论不仅成立，而且从「可能」升级为「必然」**（只要后端不是 local）。

---

## 三、这次核实顺带暴露的一处**设计不对称**

`EffectiveAttachmentDir()` 的注释写着它被导出是为了
「**收口分散的 `os.Getenv("LLM_GATEWAY_ATTACHMENT_DIR")` 读取**」——
**它收口的是「配置来源的分散」，不是「后端形态的分散」**。

⇒ 而 `data_lifecycle_attachments_filesystem.go` 的两个端点
（`stats` / `cleanup`）**都是纯本地文件系统语义**，
**它们与「后端可切换」这个前提之间，没有任何一层显式的一致性检查。**

⚠️ **这与 160 号 §二 那个镜像缺口是同一件事的两面**：
- 面向 `local` 时，清理会**删太多**（64 号）；
- 面向远程三后端时，清理**根本够不着**，而网关侧也没有替代路径（160 号 §二）。

⇒ **「切换存储后端」这个动作本身，没有对数据生命周期路径做任何检查。**
⚠️ **本轮只坐实了 attachments 这一条路径**；**其它随存储后端切换而语义变化的路径本轮未查**，
如实登记为待查（不外推）。

---

## 四、诚实边界

- **未改动任何生产代码或数据库**（本轮纯代码阅读 + 一次 grep）。
- ⚠️ **未核 `storage.attachment_dir_override` 是否会在配置远程后端时被自动写入** ——
  即「管理员选了 OSS，系统会不会顺手把 attachment_dir_override 指到某个本地镜像目录」。
  **这不影响本轮结论**（无论写不写，返回值都是本地路径），但**可能影响误判的具体形态**，登记为未核。
- ⚠️ **§三 的「其它随存储后端切换而语义变化的路径」本轮未查**——**本条只对 attachments 成立，不外推**。
- 真库本轮未查（本轮不需要）。
- **§二 表格里 `local` 一行的「64 号误删风险成立」沿用 158/159/160 号的结论，本轮未重新核实。**
