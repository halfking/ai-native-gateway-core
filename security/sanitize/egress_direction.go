package sanitize

// r1037（2026-10-20，211 生产事故）：防外传守卫的「公网目的地」判定必须区分**方向**。
//
// ## 事故
//
// 211（llm.itestu.cn）上 zcode 用 minimax-m3 干活，模型要 curl 下载 Android SDK
// 系统镜像，Bash 参数整条被 `output_policy_blocked` 拦掉：
//
//	output_compliance: blocked stream event label=__tool_json@Bash field_bytes=352
//	sanitize_rule_events: builtin.network_destination|egress|block|custom|command
//	                              2026-10-20 04:49:03 / 04:49:28 / 04:49:34
//
// 客户端同一秒报错（zcode 日志）：reason=empty_model_response，
// "Model returned no text, no tool calls, and no usage before completing the turn."。
// 模型当时的 thinking 原文：「OK let me actually fire the curl now … for the curl
// download」——被拦的是一次**下载**。
//
// ## 根因（类别错误）
//
// `builtin.network_destination` 的 pattern 是 `(?s).+` —— **通配**。它自己不带任何
// 判别力，命中与否完全取决于 `hasPublicHostDestination`，也就是「这段文本里提到
// 了公网主机」。
//
// 「提到公网主机」≠「把数据送到公网主机」。curl 下个 SDK、git clone 一个仓库、
// wget 拉个镜像，数据方向是**取回**；把 /etc/passwd POST 出去才是**送出**。
// 守卫把两者当成同一件事，于是把 coding agent 最日常的操作整条打死。
//
// 这是本守卫第三次因同一种「形态误伤」收窄：
//
//	r1033（2026-10-17，154）：Bash 参数含版本四元组 jdk-17.0.20.1+1
//	r1035（2026-10-17，154）：Bash 参数含中文地址样路径
//	r1037（2026-10-20，211）：Bash 参数含下载 URL
//
// ## 口径（owner 裁决 2026-10-20）
//
// 「公网目的地」有两个档位，各有各的清单键：
//
//	builtin.network_destination —— **形状**档：参数里**提到**公网主机。
//	                              旧语义（r1016/r1028 契约），覆盖面广，
//	                              但下载 / clone / 拉镜像也会命中 —— 211 事故
//	                              就是它造成的。默认不在清单里。
//	builtin.egress_sink        —— **方向**档：把本地数据**送往**公网。
//	                              纯读取（下载 / 克隆 / 拉取）不构成外传；
//	                              带数据外送仍拦：请求体、上传文件、自定义头、
//	                              把本地数据拼进请求（命令替换 / 变量插值）、
//	                              以及本身就是发送型的客户端（nc / socat / ssh /
//	                              scp / mail / git push …）。
//
// 两档的分工：**覆盖面由形状档兜底，精确性由方向档提供**。方向档只认
// 「已声明的网络客户端」——`echo "https://example.com"`、`cat notes.md`
// （正文含 URL）不构成外传，而把「认不出来」一律当发送，正是 r1033/r1035/
// r1037 三次形态误伤的共同成因。
//
// ## 形态枚举的边界
//
// **枚举命令，不枚举形态**（与 r1019 的 netcatFamilyVerbs 同一族口径）：
// 读取型客户端与发送型客户端各是一个**封闭集**。未列进集合的命令在方向档
// 下**不算**外传——但它仍然会被形状档按「提到了公网主机」命中，运维可以
// 按需开启对应的那一档。

import (
	"path"
	"regexp"
	"strings"
)

// readOnlyNetworkClients 是「只从目的地取回数据」的客户端调用（封闭集）。
//
// 键是**命令名**（动词），不含子命令——带子命令的（git）另走 gitReadOnlySubcommands。
var readOnlyNetworkClients = map[string]bool{
	"curl":   true,
	"wget":   true,
	"http":   true, // httpie
	"https":  true, // httpie
	"aria2c": true,
}

// sendNetworkClients 是**发送型**网络客户端（封闭集）。
//
// 这些命令的用途本身就是把数据送出去：连上对端就是把本地内容推过去。
// 与 readOnlyNetworkClients 一起构成「已声明的网络客户端」全集。
//
// 为什么是封闭集而不是「认不出来就当发送」：方向规则是给 coding agent 用的
// **精确档位**，`echo "https://example.com"` / `cat notes.md`（正文里有 URL）
// 这类命令不该被当成外传。**覆盖面由形状规则（builtin.network_destination）
// 兜底** —— 它认的是「提到了公网主机」，管住没被列进这个集合的客户端。
// 两档各管一件事，运维按需要选。
var sendNetworkClients = map[string]bool{
	"nc": true, "ncat": true, "netcat": true, "socat": true,
	"ssh": true, "scp": true, "sftp": true, "rsync": true,
	"telnet": true, "ftp": true, "lftp": true,
	"mail": true, "mailx": true, "sendmail": true, "smtp": true, "mutt": true,
	"aws": true, "gcloud": true, "az": true, "mc": true, "s3cmd": true, "ossutil": true,
}

// gitReadOnlySubcommands 是 git 里只读取远端的子命令。
//
// git 的有效动词是**子命令**而不是 git 本身：`git push` 与 `git clone` 方向相反，
// 必须分开。子命令在 `git` 之后的 token 里找（`git -C /path clone URL` 这种带
// 全局选项的写法也在内）。
var gitReadOnlySubcommands = map[string]bool{
	"clone":     true,
	"fetch":     true,
	"pull":      true,
	"ls-remote": true,
	"submodule": true,
}

// gitSendSubcommands 是 git 里把本地内容送到远端的子命令。
var gitSendSubcommands = map[string]bool{
	"push": true,
}

// outboundPayloadOptions 是「把本地数据放进请求」的客户端选项（封闭集）。
//
// 这些选项一出现，目的地就从「源」变成「汇」：
//
//	-d/--data*     请求体
//	-F/--form*     表单上传
//	-T/--upload-file 文件上传
//	-H/--header    自定义头（头里可以带凭据）
//	-b/--cookie    Cookie（本地文件或串）
var outboundPayloadOptions = map[string]bool{
	"-d": true, "--data": true, "--data-raw": true, "--data-binary": true, "--data-urlencode": true,
	"-F": true, "--form": true, "--form-string": true,
	"-T": true, "--upload-file": true,
	"-H": true, "--header": true,
	"-b": true, "--cookie": true,
	"--post-data": true, "--post-file": true,
}

// localDataInjection 是「把本地数据插进请求」的形态。
//
// 命令替换 `$(...)`、反引号、变量插值 `${...}` / `$VAR`——它们出现在 URL 或
// 请求串里时，本地内容会随请求一起发出去。
var localDataInjection = regexp.MustCompile("`|`|\\$\\(|\\$\\{|\\$[A-Za-z_][A-Za-z0-9_]*")

// hasPublicHostSink 报告 text 里是否存在**把本地数据送往公网主机**的目的地。
//
// 与 hasPublicHostDestination 的差别只有一个词，但结论相反：
//
//	hasPublicHostDestination：提到了公网主机（可能是取回，也可能是送出）
//	hasPublicHostSink：        会把本地数据送到公网主机
//
// 「取回」不构成外传：curl 下个 SDK、git clone 一个仓库、wget 拉个镜像，数据方向
// 是往本地流。挡住它等于把 coding agent 最日常的操作打死（r1037 的 211 事故）。
//
// 判定按**命令段**做：一条命令里既有下载又有上传时，上传那一段仍然要拦。
func hasPublicHostSink(text string) bool {
	for _, segment := range splitShellCommandSegments(text) {
		if !segmentMentionsPublicHost(segment) {
			continue
		}
		if segmentSendsLocalData(segment) {
			return true
		}
	}
	return false
}

// segmentMentionsPublicHost 报告该命令段里是否出现公网可达主机。
//
// 与 hasPublicHostDestination 的三条提取规则完全一致，只是作用域收窄到一段命令。
func segmentMentionsPublicHost(segment string) bool {
	for _, m := range egressHostFromURL.FindAllStringSubmatch(segment, -1) {
		if !isNonExfiltrationDestinationHost(m[1]) {
			return true
		}
	}
	for _, host := range hostPortDestinationsFromCommand(segment) {
		if !isNonExfiltrationDestinationHost(host) {
			return true
		}
	}
	if host, ok := bareHostPortFromNetcatCommand(segment); ok {
		if !isNonExfiltrationDestinationHost(host) {
			return true
		}
	}
	return false
}

// segmentSendsLocalData 判定该命令段是否把本地数据送出。
//
// 判定按「**有没有声明网络客户端**」分层：
//
//  1. 段里出现**发送型**客户端（nc / ssh / scp / mail / 上传类…）⇒ 发送。
//     这些命令的用途本身就是把数据推出去。
//  2. 段里只出现**读取型**客户端（curl / wget / http / git clone…）⇒
//     带外送载荷选项（-d/-F/-T/-H/-b…）或本地数据插值
//     （`$(...)` / 反引号 / `$VAR`）时才是发送。
//  3. 段里**没有任何已声明的网络客户端** ⇒ 不是外传汇。
//     `echo "https://example.com"`、`cat notes.md`（正文含 URL）这类命令
//     不构成外传；把「认不出来」一律当发送，正是 r1033/r1035/r1037 三次
//     形态误伤的共同成因。
//
// ⇒ 覆盖面由**形状规则**（builtin.network_destination，认「提到了公网主机」）
// 兜底，方向规则只负责「认得出来的客户端确实在送数据」这一档精确判断。
func segmentSendsLocalData(segment string) bool {
	tokens := commandTokens(segment)
	readOnly := false
	send := false
	for i, tok := range tokens {
		base := commandBaseName(tok)
		if base == "git" {
			if sub, ok := gitSubcommand(tokens[i:]); ok {
				if gitReadOnlySubcommands[sub] {
					readOnly = true
				}
				if gitSendSubcommands[sub] {
					send = true
				}
			}
			continue
		}
		if readOnlyNetworkClients[base] {
			readOnly = true
		}
		if sendNetworkClients[base] {
			send = true
		}
	}
	if send {
		return true
	}
	if !readOnly {
		return false
	}
	return segmentHasOutboundPayload(tokens) || localDataInjection.MatchString(segment)
}

// gitSubcommand 从 `git` 开始的 token 序列里找第一个已知子命令。
//
// 跳过 flag（`git -C /path clone URL` 里的 `-C` 与 `/path` 都不算子命令），
// 但**不**在第一个非 flag token 处停下——`git -C /path clone` 里的子命令在
// 全局选项之后，停下就永远认不出 clone。
func gitSubcommand(tokens []string) (string, bool) {
	for _, tok := range tokens[1:] {
		sub := strings.ToLower(strings.Trim(tok, "\"'`"))
		if strings.HasPrefix(sub, "-") {
			continue
		}
		if gitReadOnlySubcommands[sub] || gitSendSubcommands[sub] {
			return sub, true
		}
	}
	return "", false
}

// segmentHasOutboundPayload 报告命令里是否出现外送载荷选项。
//
// 只认**独立 token**形态（`-d @file`），不认粘连形态（`-d@file`）——后者是 curl
// 的合法写法，但粘连形态本来也会被 curl 解析成同一个选项，漏判方向是「少拦」。
// 这里保守：粘连形态也认。
func segmentHasOutboundPayload(tokens []string) bool {
	for _, tok := range tokens {
		trimmed := strings.Trim(tok, "\"'`")
		if trimmed == "" {
			continue
		}
		if outboundPayloadOptions[trimmed] {
			return true
		}
		for opt := range outboundPayloadOptions {
			if strings.HasPrefix(trimmed, opt) && len(trimmed) > len(opt) {
				return true
			}
		}
	}
	return false
}

// commandTokens 把命令段切成 token（按空白），去掉引号。
func commandTokens(segment string) []string {
	fields := strings.Fields(segment)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		tokens = append(tokens, strings.Trim(f, "\"'`"))
	}
	return tokens
}

// commandBaseName 取 token 的命令名（去掉路径与引号，小写）。
func commandBaseName(token string) string {
	return strings.ToLower(path.Base(strings.TrimSpace(token)))
}

// splitShellCommandSegments 把一段 shell 文本切成「命令段」。
//
// 切分点：`;`、`&&`、`||`、单 `|`、`&`（后台）与换行。
//
// 反斜杠续行（`\` + 换行）**不**切——它是同一条命令的续行。agent 写多行命令时
// 常把 URL 放到下一行，切开会让「动词在上一行、URL 在下一行」的下载命令失去动词，
// 被误判成发送型。
func splitShellCommandSegments(text string) []string {
	var segments []string
	var current strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; c {
		case '\\':
			if i+1 < len(runes) && runes[i+1] == '\n' {
				// 续行：与下一行属于同一条命令
				current.WriteRune(' ')
				i++
				continue
			}
			current.WriteRune(c)
		case ';', '\n':
			segments = append(segments, current.String())
			current.Reset()
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			}
			segments = append(segments, current.String())
			current.Reset()
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
			segments = append(segments, current.String())
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	segments = append(segments, current.String())
	return segments
}
