package sanitizerules

// 硬阻断清单（HardBlockAllow）——「哪些规则允许终止回合」的**显式清单**。
//
// ## 为什么要有这个清单
//
// 2026-10-20（211 生产，r1037）：zcode 用 minimax-m3 干活，模型要 curl 下载
// Android SDK 镜像（Bash 参数里有个公网主机），防外传守卫把**整条工具调用**
// 掐掉 → 流被终止 → 客户端只看到「模型未返回任何内容」（empty_model_response）。
//
// 根因不是某条规则写错了，而是守卫的**判据形态**：它按「形状」判
// （参数里出现公网主机 / 公网 IP / `password=` 字面量 / 地址样文本），
// 命中就终止整个回合。而 coding agent 的日常操作恰好全是这些形状：
// 装依赖、拉镜像、clone 仓库、grep 一个 URL、cat 一个带 password 的配置。
//
// ## 口径（owner 裁决 2026-10-20）
//
//   - **默认不硬阻断**。形状匹配降级为「记录」（egress）或「遮蔽」（output），
//     回合继续走完，客户端拿到的是被遮蔽/被记录的内容，而不是空响应。
//   - **请求侧（input）不动**：请求本身带明文凭据仍然 fail-closed。
//     （不能扩大范围 = 不新增阻断面，不是把已有的收紧。）
//   - **要拦就用清单显式开**。清单可配置（环境变量
//     `LLM_GATEWAY_HARD_BLOCK_RULES`），改一次即全网生效，不必改代码发版。
//
// ## 清单格式
//
// 逗号分隔的 `scope:rule`，scope ∈ {input, output, egress}：
//
//	LLM_GATEWAY_HARD_BLOCK_RULES="egress:builtin.secret_field.egress,egress:secret.*"
//
// rule 支持三种写法：
//
//	*        该 scope 下全部规则
//	精确 ID  单条规则（如 builtin.network_destination）
//	前缀.*   同前缀的一族规则（如 secret.*）
//
// 未在清单里的规则**不允许**硬阻断；被允许的规则保持它原有的 action。
//
// ## egress scope 的额外限制（r1039，2026-10-20）
//
// egress scope **只接受逐条的内建规则 ID**（`builtin.` 前缀）：
//
//	egress:*                  → 拒绝
//	egress:secret.*           → 拒绝（不接受前缀族）
//	egress:pii.email.egress   → 拒绝（PII/secret 分类器不是外传判定）
//	egress:builtin.egress_sink → 接受
//
// 成因（实测）：`egress:*` 会把 PII/secret 分类器的 .egress 副本一并变成
// 阻断规则，而那些规则的语义是**分类与遮蔽**、不是「外传判定」。于是
//
//	ssh deploy@prod.example.com 'kubectl get pods'   → 拦
//	scp file.txt backup@files.example.com:/tmp/x     → 拦
//	echo contact ops@example.com                     → 拦
//	一个手机号 / 一个身份证 / `发货地址 建国路88号`     → 拦
//
// 全部是**正常操作**被拦——正是 owner 说的「不要放大范围，导致正常的工具
// 执行或脚本执行失败」。⇒ egress 只能逐条点名内建规则。
//
// 限制的**执行点是解析层**（ParseHardBlockAllows / LoadHardBlockAllowsFromEnv），
// 也就是运维唯一能碰的那张表；程序内调用 SetHardBlockAllows 不受此限
// （测试基线要能表达「全部可阻断」）。
//
// ## 环境变量的三条语义
//
//	未设置 → 出厂默认（只有 input 允许硬阻断）
//	有值   → **出厂默认 + 配置项**（累加，见 LoadHardBlockAllowsFromEnv）
//	"-"    → 显式清空（唯一能关掉请求侧底线的写法）

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// HardBlockAllowEnv 是配置清单的环境变量名。
const HardBlockAllowEnv = "LLM_GATEWAY_HARD_BLOCK_RULES"

// HardBlockAllow 描述一条允许硬阻断的规则。
type HardBlockAllow struct {
	Scope string `json:"scope"` // input | output | egress
	Rule  string `json:"rule"`  // 规则 ID；"*" 全量；"prefix.*" 前缀族
}

// hardBlockAllows 是进程级清单。原子替换，读侧无锁。
var hardBlockAllows atomic.Pointer[[]HardBlockAllow]

func init() {
	// 默认清单：只有请求侧允许硬阻断。output/egress 一律不终止回合。
	hardBlockAllows.Store(&[]HardBlockAllow{{Scope: "input", Rule: "*"}})
}

// DefaultHardBlockAllows 返回默认清单（副本）。
func DefaultHardBlockAllows() []HardBlockAllow {
	src := hardBlockAllows.Load()
	if src == nil {
		return nil
	}
	out := make([]HardBlockAllow, len(*src))
	copy(out, *src)
	return out
}

// SetHardBlockAllows 覆盖进程级清单。空切片表示「任何 scope 都不允许硬阻断」。
func SetHardBlockAllows(allows []HardBlockAllow) {
	hardBlockAllows.Store(&allows)
}

// HardBlockAllows 返回当前清单（副本）。
func HardBlockAllows() []HardBlockAllow {
	src := hardBlockAllows.Load()
	if src == nil {
		return nil
	}
	out := make([]HardBlockAllow, len(*src))
	copy(out, *src)
	return out
}

// HardBlockAllowed 报告 scope 下的规则 id 是否允许硬阻断。
//
// id 为空（没有规则 ID 的形态，例如非托管路径的内建检测）时按「不允许」处理：
// 拿不出规则名 = 说不出「因为什么规则被拦」，而 owner 要求拦截必须能说清规则。
func HardBlockAllowed(scope, id string) bool {
	if id == "" {
		return false
	}
	src := hardBlockAllows.Load()
	if src == nil {
		return false
	}
	for _, a := range *src {
		if a.Scope != scope {
			continue
		}
		if a.Rule == "*" || a.Rule == id {
			return true
		}
		if strings.HasSuffix(a.Rule, ".*") {
			if strings.HasPrefix(id, strings.TrimSuffix(a.Rule, "*")) {
				return true
			}
		}
	}
	return false
}

// EgressScopeRulePrefix 是 egress scope 允许的规则 ID 前缀。
//
// 只有内建规则（builtin.*）能被选成 egress 的硬阻断规则：它们是
// 「外传目的地 / 凭据字段 / 方向判定」这一族；PII 与 secret 分类器的
// .egress 副本（pii.email.egress / secret.password.egress …）语义是
// **分类与遮蔽**，把它们当成外传判定会把正常工具调用整条掐断。
const EgressScopeRulePrefix = "builtin."

// ValidateHardBlockAllow 报告一条清单项是否合规。
//
// egress scope 的限制在这里（r1039）：只接受逐条的内建规则 ID。
// 解析层与将来的管理面都走这一个入口，避免两处口径漂移。
func ValidateHardBlockAllow(scope, rule string) error {
	if scope != "egress" {
		return nil
	}
	switch {
	case rule == "*":
		return fmt.Errorf("egress scope 不接受通配 %q：会把 PII/secret 分类器变成阻断规则", rule)
	case strings.HasSuffix(rule, ".*"):
		return fmt.Errorf("egress scope 只接受逐条规则 ID，不接受前缀族 %q", rule)
	case !strings.HasPrefix(rule, EgressScopeRulePrefix):
		return fmt.Errorf("egress scope 只接受 %s 前缀的内建规则，%q 是分类器规则", EgressScopeRulePrefix, rule)
	}
	return nil
}

// ParseHardBlockAllows 解析 `scope:rule` 列表。
func ParseHardBlockAllows(raw string) ([]HardBlockAllow, error) {
	var out []HardBlockAllow
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		scope, rule, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("hard block allow %q: want scope:rule", item)
		}
		scope = strings.ToLower(strings.TrimSpace(scope))
		rule = strings.TrimSpace(rule)
		switch scope {
		case "input", "output", "egress":
		default:
			return nil, fmt.Errorf("hard block allow %q: unknown scope", item)
		}
		if rule == "" {
			return nil, fmt.Errorf("hard block allow %q: empty rule", item)
		}
		if err := ValidateHardBlockAllow(scope, rule); err != nil {
			return nil, fmt.Errorf("hard block allow %q: %w", item, err)
		}
		out = append(out, HardBlockAllow{Scope: scope, Rule: rule})
	}
	return out, nil
}

// LoadHardBlockAllowsFromEnv 从环境变量装载清单。
//
// 语义（三条，别搞混）：
//
//	未设置  → **保持出厂默认**（只有 input 允许硬阻断）。
//	          「忘了配」≠「配成全放开」，也不≠「配成全关」。
//	有值    → **出厂默认 + 配置项**（**累加**，不是替换）。
//	          理由：出厂默认里的 `input:*` 是请求侧 fail-closed 的底线；
//	          若配置项替换掉它，运维只想开一条 egress 规则就会**静默**把
//	          请求侧的明文凭据放行（211 首轮部署实测踩到：设
//	          `egress:builtin.egress_sink` 后启动日志的 allows 里
//	          input 消失 ⇒ 请求侧不再 fail-closed）。
//	"-"     → 显式清空（任何 scope 都不允许硬阻断）。这是唯一能关掉请求侧
//	          底线的写法，必须显式。
func LoadHardBlockAllowsFromEnv() ([]HardBlockAllow, error) {
	raw, ok := os.LookupEnv(HardBlockAllowEnv)
	if !ok {
		return DefaultHardBlockAllows(), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "-" {
		SetHardBlockAllows(nil)
		return nil, nil
	}
	adds, err := ParseHardBlockAllows(raw)
	if err != nil {
		return nil, err
	}
	// 累加：出厂默认（input:*）+ 配置项。重复项去重（配置项里显式写了
	// input:* 也只留一条，避免启动日志里出现两条一样的）。
	merged := DefaultHardBlockAllows()
	seen := make(map[HardBlockAllow]bool, len(merged)+len(adds))
	for _, a := range merged {
		seen[a] = true
	}
	for _, a := range adds {
		if seen[a] {
			continue
		}
		seen[a] = true
		merged = append(merged, a)
	}
	SetHardBlockAllows(merged)
	return merged, nil
}

// AddHardBlockAllow 向进程级清单追加一条 `scope:rule`（幂等：已存在即 no-op）。
// 校验走 ParseHardBlockAllows（egress 只接受逐条内建规则的 r1039 限制在此生效）。
//
// 2026-10-29 拦截持久化轮：单条规则的「同步开启」入口；落库由调用方
// SaveHardBlockStatePersistent 完成（本函数只动进程内清单）。
func AddHardBlockAllow(scope, rule string) error {
	adds, err := ParseHardBlockAllows(scope + ":" + rule)
	if err != nil {
		return err
	}
	current := HardBlockAllows()
	for _, a := range current {
		if a == adds[0] {
			return nil
		}
	}
	SetHardBlockAllows(append(current, adds[0]))
	return nil
}

// RemoveHardBlockAllow 从进程级清单移除一条精确 `scope:rule`，返回是否命中。
// 只删**精确同形**条目：`output:*` 由 "output:*" 删除，不影响逐条规则；
// 前缀族同理（管理面展示的就是清单本身，删的是看得见的那一行）。
func RemoveHardBlockAllow(scope, rule string) bool {
	current := HardBlockAllows()
	out := make([]HardBlockAllow, 0, len(current))
	removed := false
	for _, a := range current {
		if a.Scope == scope && a.Rule == rule {
			removed = true
			continue
		}
		out = append(out, a)
	}
	if removed {
		SetHardBlockAllows(out)
	}
	return removed
}
