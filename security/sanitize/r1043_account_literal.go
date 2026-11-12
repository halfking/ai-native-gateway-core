package sanitize

import (
	"regexp"
	"strings"
)

// ──────────────────────────────────────────────────────────────────────────
// r1043（2026-11-13，211 request 594aed87…，ZCode/3.18.0 glm-5.3-flash 事故轮）
//
// 生产取证（Redis db5 session:37a8eec1ce19687d:gw_ae47d6bb…:sanitize，事故会话
// 的占位符字典逐字段核对）：account 族在一个会话里铸造了 **38 个**占位符，其中
// 绝大多数是毒值——
//
//	{SENSITIVE:account:6}  → [REDACTED:account]                ← 遮蔽标记本身被当「值」
//	{SENSITIVE:account:36} → \[REDACTED\]…                      ← 转义形态的标记
//	{SENSITIVE:account:34} → {SENSITIVE')+20)+']', 'SSH_USER=   ← 模型修补占位符的 Python 代码片段
//	{SENSITIVE:account:15} → \$(docker                          ← shell 命令片段
//	{SENSITIVE:account:22} →  in l or l.startswith('[ -n …      ← Python 比较表达式片段
//	{SENSITIVE:account:28} → $(                                ← 命令替换残片
//
// 三条根因链（每条都有生产实证）：
//
//  D1 标记投毒：`SSH_USER="[REDACTED:account]"` 被 account_declaration 规则当
//     「账号赋值」命中，**遮蔽标记本身**被铸成占位符的值。此后模型每次回显
//     {SENSITIVE:account:6}，还原器都「忠实地」把遮蔽标记写回输出——标记成了
//     会话词汇表的永久成员，与真值不可区分（作业执行失败的直接来源之一）。
//
//  D2 代码片段投毒：account_declaration/account_assignment 的值文法接受任意
//     引号串与 token 串，模型代码里的 `'SSH_USER=' in l or l.startswith('…')`
//     （Python 比较，不是赋值）整段被当值铸造；更糟的是**输入侧脱敏把命中的
//     span（含成对引号的一侧）原样挖走换成占位符**——模型收到的代码本身已经
//     语法残缺，模型按残缺理解重写文件，bash -n 从此再也没通过过。
//
//  D3 知名系统账号触发：`SSH_USER="root"` / `USER=root` 被占位符化。root 是
//     公开默认账号（与 r1024「身份不是机密」、r1039 D7「示例值不是泄漏」同一
//     判据族），遮蔽它保护不了任何东西，却是模型发现「有个过滤器在改我的
//     文件」、开始写修补代码、进而引爆 D1/D2 的**触发器**。
//
// 本文件实现 D2/D3 的引擎级守卫（对所有规则来源同口径）；D1 的检测免疫挂在
// detectionExemptSpans（detector.go），字典侧清洗挂在 smart_sani_guard.go。
// ──────────────────────────────────────────────────────────────────────────

// accountLiteralPattern 是「账号字面量」的形状：字母/数字开头，仅含字母（任意
// 文字系统）、数字、`_`、`.`、`@`、`-`，≤64 个 rune。
//
// D2 的判据是**值必须长得像账号**，而不是「看起来不像代码」的枚举——枚举壳
// （`$(`、`${`）永远追不上模型写出的新形状；「账号字符集」是从值本身的定义
// 出发的：真实账号（zhangsan_ops_2024 / kaixuan.admin / 张三）全部落在字形内，
// 含空白/引号/圆括号/花括号/美元符的片段全部落在外面。
//
// 已知边界（登记）：带空格的引号值（`username: "john smith"`）会被豁免——
// 真实账号极少含空格，而本轮事故的毒值恰恰全部来自「引号串里的代码」
// （`' in l or l.startswith('`），接受这个交换。
var accountLiteralPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_.@\-]{0,63}$`)

// wellKnownAccountLiterals 是「公开默认账号」封闭集（比较前小写化）。
//
// 判据（与 dummyCredentialValues / r1024 一脉）：这些值**定义上不构成泄漏**——
// 每台 Linux 都有 root，每个发行版文档都写着 ubuntu/ec2-user，它们不是用户
// 特有的 PII，遮蔽它们没有防护收益，只会（实测如此）把 coding agent 的任务
// 引爆成「过滤器捉迷藏」。真实人名/工号账号（zhangsan_ops_2024）不在集内，
// 照常脱敏。
//
// 刻意**不收录** deploy/operator/ops：它们是组织惯用名而非系统公开默认，
// r1024 的钉测（TestR1024AccountRuleCoversUserKeyForms）与输出面钉测
// （TestOutputSensitiveMasksNewValuesAndRestoresKnownValues）都把它们钉在
// 「必须检出」一侧——本清单只收「任何一台默认安装都存在」的名字。
//
// 封闭集 + 默认不豁免：未收录的账号值（哪怕同样「眼熟」）保持原行为；要加
// 名字就在这里加一行，判据（公开默认、无用户特异性）写进注释。
var wellKnownAccountLiterals = map[string]bool{
	// POSIX 系统账号
	"root": true, "daemon": true, "bin": true, "sys": true, "nobody": true,
	// 常见管理默认名
	"admin": true, "administrator": true, "user": true, "guest": true, "test": true,
	// 云/发行版默认登录名
	"ubuntu": true, "ec2-user": true, "centos": true, "debian": true,
	// 服务默认账号（DSN/连接串里的用户名；口令由 secret 族另行把守）
	"postgres": true, "mysql": true, "nginx": true, "www-data": true, "redis": true,
	// 协议服务账号
	"git": true,
}

// isWellKnownAccountLiteral 报告 value（大小写不敏感）是否为公开默认账号。
func isWellKnownAccountLiteral(value string) bool {
	return wellKnownAccountLiterals[strings.ToLower(strings.TrimSpace(value))]
}

// accountDeclarationKind 报告 kind 是否为「账号声明/赋值」族——D2/D3 守卫的
// 作用面。
//
// 刻意不含 credential_pair：成对声明（`帐号 root / 密码 xxx`）的账号臂与密码臂
// 是**同一条规则**的两个捕获组，单独豁免账号臂会改变配对语义；真实成对声明
// 里的秘密（密码臂）由 secret 族照常把守，账号臂的毒值风险（值文法更紧）登记
// 不修。
func accountDeclarationKind(kind string) bool {
	return kind == "account_declaration" || kind == "account_assignment"
}

// accountValueIsExempt 对账号声明/赋值族做 D2（字面量形状）与 D3（公开默认）
// 判定。真阳性对照（zhangsan_ops_2024 / kaixuan.admin）必须继续返回 false。
func accountValueIsExempt(kind, value string) bool {
	if !accountDeclarationKind(kind) {
		return false
	}
	// D3：公开默认账号——任何账号声明形态（含 credential_pair 之外的一切
	// declaration/assignment）都不遮蔽。
	if isWellKnownAccountLiteral(value) {
		return true
	}
	// D2：值必须是账号字面量形状；代码片段/引号串散文（' in l or …'）豁免。
	return !accountLiteralPattern.MatchString(value)
}
