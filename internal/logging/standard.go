// Package logging — rule 71 统一日志标准的字段注入与脱敏层
//
// -----------------------------------------------------------------------------
// Purpose:        在 llm-gateway-go 既有的 slog 管线之上，叠加 rule 71 要求的
//
//	规范字段与脱敏能力：
//	① 8 个必填字段（timestamp/severity_text/severity_number/msg/
//	   service/version/env/instance_id）自动注入；
//	② 扁平 JSONL 输出（单层，无嵌套）；
//	③ 敏感信息在 **handler 层**统一脱敏（调用点不会漏）。
//
// Replaces:       (无)
// Replaced-by:    (无)
// Status:         active
//
// Changelog:
//
//	2026-11-09  v1.0  首版。随 vibe-coding rule 71 落地。
//
// -----------------------------------------------------------------------------
//
// 与 logging.go 的关系：
//
//	logging.go 负责「写到哪、怎么轮转」；本文件负责「写什么、长什么样」。
//	两者正交组合，不互相替代。
//
// 背景：本仓此前同一服务存在**两种日志格式**——
//
//	cmd/gateway、cmd/quality-service 用 JSONHandler，
//	cmd/mcp-gateway、cmd/gateway-v2、cmd/llm-gw-annotator 等用 TextHandler。
//	AI 消费同一服务的日志需要两套解析分支，这是 rule 71 要消灭的首要问题。
//
// 参考实现与踩坑记录：vibe-coding/templates/logging/README.md
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 规范级别文本与 OTel SeverityNumber 数值表（rule 71 §2 / §4.1）。
// TRACE 1-4 | DEBUG 5-8 | INFO 9-12 | WARN 13-16 | ERROR 17-20 | FATAL 21-24
// 实现取区间下界，便于数值比较（severity_number >= 17 即 ERROR 及以上）。

// severityNumber 把 slog 级别映射为 OTel SeverityNumber。
func severityNumber(l slog.Level) int {
	switch {
	case l < slog.LevelDebug:
		return 1 // TRACE
	case l < slog.LevelInfo:
		return 5 // DEBUG
	case l < slog.LevelWarn:
		return 9 // INFO
	case l < slog.LevelError:
		return 13 // WARN
	case l < slog.LevelError+4:
		return 17 // ERROR
	default:
		return 21 // FATAL
	}
}

// severityText 把 slog 级别映射为规范级别文本。
func severityText(l slog.Level) string {
	switch {
	case l < slog.LevelDebug:
		return "TRACE"
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	case l < slog.LevelError+4:
		return "ERROR"
	default:
		return "FATAL"
	}
}

// RedactedPlaceholder 是敏感值的统一替换文本。
const RedactedPlaceholder = "[REDACTED]"

// sensitiveKeys 是需要脱敏的键名（小写比较）。
//
// 注意：**不要**把 "env" 列入——本规范的 env 是部署环境名，不是环境变量。
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true,
	"secret": true, "token": true, "access_token": true, "refresh_token": true,
	"api_key": true, "apikey": true, "authorization": true,
	"cookie": true, "session_id": true, "private_key": true,
	"credential": true, "credential_id": true, "handoff_token": true,
}

// sensitivePatterns 是按**值**匹配的敏感模式（命中即替换整个值）。
var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`),                           // Bearer token
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`), // JWT
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                        // AWS access key
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`),                                  // OpenAI 风格 key
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),                                    // 长十六进制（疑似密钥）
}

// maxValueLength 是单字段值的长度上限（rule 71 §5.1）。
const maxValueLength = 2048

// isSensitiveKey 判断键名是否需要脱敏。
func isSensitiveKey(key string) bool {
	return sensitiveKeys[strings.ToLower(key)]
}

// redactString 对字符串值做模式脱敏 + 截断。
func redactString(s string) string {
	for _, re := range sensitivePatterns {
		if re.MatchString(s) {
			return RedactedPlaceholder
		}
	}
	if len(s) > maxValueLength {
		return s[:maxValueLength] + "…[truncated]"
	}
	return s
}

// Resource 持有四个进程级常量字段（rule 71 §4.1）。
//
// 必须为常量——同一进程生命周期内不变，这是它们能升为 Loki 标签的前提。
type Resource struct {
	Service    string
	Version    string
	Env        string
	InstanceID string
}

// StandardOptions 是规范字段的初始化参数。
type StandardOptions struct {
	// Service 是服务标识，须与 deploy/platform/services.json 的键一致。
	Service string
	// Version 是服务版本，必须由构建期注入，禁止硬编码。
	Version string
	// Env 是部署环境：local / dev / test / staging / prod。
	Env string
	// Level 是最低日志级别。
	Level slog.Level
	// InstanceID 可选；留空则自动生成 hostname-pid-启动时刻。
	InstanceID string
	// DisableRedact 关闭脱敏。**仅允许在测试中使用**。
	DisableRedact bool
}

// validEnvs 是允许的环境取值（rule 71 §4.1）。
var validEnvs = map[string]bool{
	"local": true, "dev": true, "test": true, "staging": true, "prod": true,
}

// EnvNames 返回允许的环境取值（错误信息与文档用）。
func EnvNames() []string {
	return []string{"local", "dev", "test", "staging", "prod"}
}

// NewResource 构造资源字段，校验参数合法性。
func NewResource(opts StandardOptions) (Resource, error) {
	if opts.Service == "" {
		return Resource{}, fmt.Errorf("logging: Service 不能为空")
	}
	if opts.Version == "" {
		return Resource{}, fmt.Errorf("logging: Version 不能为空（须构建期注入）")
	}
	if opts.Env == "" {
		return Resource{}, fmt.Errorf("logging: Env 不能为空")
	}
	if !validEnvs[opts.Env] {
		return Resource{}, fmt.Errorf("logging: Env=%q 非法，允许 %v", opts.Env, EnvNames())
	}
	instanceID := opts.InstanceID
	if instanceID == "" {
		instanceID = InstanceID()
	}
	return Resource{
		Service:    opts.Service,
		Version:    opts.Version,
		Env:        opts.Env,
		InstanceID: instanceID,
	}, nil
}

// buildInstanceID 生成实例唯一标识：hostname-pid-启动时刻。
func buildInstanceID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().Unix())
}

// processInstanceID 缓存进程级实例 ID。
//
// 为什么必须缓存：规范层在一个进程里会被装**多次**
// （InstallEarly 早装 → Init 全装 → Reconfigure 热加载），
// 若每次装都重新生成 instance_id，同一进程的不同日志行会带不同的
// instance_id ⇒「按 instance_id 聚合一个实例的全部日志」这条查询不可靠。
//
// 规则 §4.1 也明确 instance_id 是进程级常量（能升为 Loki 标签的前提）。
var (
	processInstanceIDOnce sync.Once
	processInstanceID     string
)

// InstanceID 返回进程级实例 ID（进程内恒定）。
//
// 调用方（cmd/gateway）应使用本函数，而不是自己再算一份——
// 否则主日志与持久化日志的 instance_id 会不一致。
func InstanceID() string {
	processInstanceIDOnce.Do(func() {
		processInstanceID = buildInstanceID()
	})
	return processInstanceID
}

// reservedNames 是规范层的**保留名**集合——由规范层独占写入，业务字段不得占用。
//
// 为什么必须强制：业务侧若传了同名 attr，同一行 JSON 里就会出现**两个同名字段**。
// 而 JSON 重复键的取值行为取决于解析器——Python json / jq 取最后一个，
// 某些工具取第一个或直接报 parse error ⇒ AI 侧解析结果不确定，
// 同一份日志在不同解析器里「服务版本」可能变成业务版本。
//
// 特别说明为什么 "time"/"msg"/"message" 也在内：
// 内层 handler 的 ReplaceAttr 会把 slog 内建的 time/msg **改名**为
// timestamp/msg。业务侧若另传同名 attr，改名后就会撞上规范层产出的字段。
//
// 冲突不静默丢弃：被剥离的键名会记入 reserved_name_conflicts 字段，
// AI 侧可直接据此定位「哪个调用点占了保留名」。
var reservedNames = map[string]bool{
	"time": true, "timestamp": true,
	"msg": true, "message": true,
	"level": true, "severity_text": true, "severity_number": true,
	"service": true, "version": true, "env": true, "instance_id": true,
	// 诊断字段自身也是保留名，否则业务侧可以把它顶掉，冲突就查不出来了。
	"reserved_name_conflicts": true,
}

// ReservedNames 返回保留名集合（测试与文档用）。
func ReservedNames() []string {
	out := make([]string, 0, len(reservedNames))
	for k := range reservedNames {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filterReserved 过滤单个属性：命中保留名则丢弃并记冲突键。
//
// group 属性会被**递归展开**（规范要求扁平单层），
// 否则 group 内的保留名会在展开后撞上规范层产出的字段。
func filterReserved(a slog.Attr, conflicts *[]string) []slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		var out []slog.Attr
		for _, sub := range a.Value.Group() {
			out = append(out, filterReserved(sub, conflicts)...)
		}
		return out
	}
	if reservedNames[a.Key] {
		*conflicts = append(*conflicts, a.Key)
		return nil
	}
	return []slog.Attr{a}
}

// stripReserved 剥离 record 中与保留名同名的业务属性。
//
// 返回 (冲突键名去重排序, 剥离后的 record)。
func stripReserved(r slog.Record) ([]string, slog.Record) {
	var conflicts []string
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(filterReserved(a, &conflicts)...)
		return true
	})
	return dedupSorted(conflicts), out
}

// dedupSorted 去重并排序，保证诊断字段稳定可测。
func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// standardHandler 在 JSON handler 之上注入规范字段并做脱敏。
//
// 为什么不用 slog.HandlerOptions.ReplaceAttr 注入多个字段？
// ReplaceAttr 每次只能返回**一个** Attr，无法同时产出
// severity_text + level + severity_number 三个顶层字段。
// 用 slog.Group 会产生嵌套对象，违反「扁平单层」约束。
// 因此这里包装一个 handler，在 Handle() 里向 record 追加属性。
type standardHandler struct {
	inner  slog.Handler
	res    Resource
	redact bool

	// conflicts 累积 WithAttrs 阶段发现的保留名冲突。
	//
	// 为什么 With 阶段也要管：With 传的 attr 会落到**内层** handler 的
	// 预格式化区，Handle 阶段看不到它们 ⇒ 不在这里剥离的话，
	// 它们会绕过规范层直接写进 JSON，与规范字段重复。
	conflicts []string
}

// Enabled 透传内层 handler 的级别判断。
func (h *standardHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle 注入规范字段后交给内层 handler。
func (h *standardHandler) Handle(ctx context.Context, r slog.Record) error {
	text := severityText(r.Level)

	// 剥离业务侧占用的保留名（见 reservedNames 注释）。
	// 必须在注入规范字段**之前**做，否则会出现重复键。
	conflicts, r := stripReserved(r)
	conflicts = dedupSorted(append(h.conflicts, conflicts...))

	r.AddAttrs(
		slog.String("severity_text", text),
		// level 是**小写**别名（ECS log.level 惯例），与 severity_text 大小写
		// 不同是刻意的：两者各取所长，避免下游 LogQL（level=~"error|fatal"）
		// 因大小写不匹配漏掉所有错误日志。
		slog.String("level", strings.ToLower(text)),
		slog.Int("severity_number", severityNumber(r.Level)),
	)

	// 保留名优先：4 个资源字段是 rule 71 的**必填契约**，
	// 业务字段不得覆盖它们——否则「version 到底是服务版本还是插件版本」
	// 会变成 AI 无法判定的问题（实测：mcp-gateway 的业务 version 覆盖了
	// 服务版本，导致日志缺必填字段）。
	//
	// 业务侧若需要同名语义，应改用无歧义的键名（plugin_version 等）。
	r.AddAttrs(slog.String("service", h.res.Service))
	r.AddAttrs(slog.String("version", h.res.Version))
	r.AddAttrs(slog.String("env", h.res.Env))
	r.AddAttrs(slog.String("instance_id", h.res.InstanceID))

	// 冲突可见化：剥离掉的保留名记在这里。
	//
	// 不是「静默丢弃」——AI 侧可以按 reserved_name_conflicts 非空
	// 直接定位到违规调用点，再推动业务侧改用无歧义键名。
	if len(conflicts) > 0 {
		r.AddAttrs(slog.Any("reserved_name_conflicts", conflicts))
	}

	if h.redact {
		r = redactRecord(r)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs 透传并保持资源字段注入能力。
//
// 同时在此处剥离保留名：With 传的 attr 会落进内层 handler 的预格式化区，
// Handle 阶段已经看不到，不在这里剥离就会绕过规范层直接写进 JSON。
func (h *standardHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var conflicts []string
	kept := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		kept = append(kept, filterReserved(a, &conflicts)...)
	}
	return &standardHandler{
		inner:     h.inner.WithAttrs(kept),
		res:       h.res,
		redact:    h.redact,
		conflicts: dedupSorted(append(h.conflicts, conflicts...)),
	}
}

// WithGroup 透传。规范要求扁平单层，group 一律拍平——这里直接返回自身。
func (h *standardHandler) WithGroup(name string) slog.Handler {
	return h
}

// redactRecord 对 record 的消息与所有属性做脱敏。
func redactRecord(r slog.Record) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, redactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return out
}

// redactAttr 脱敏单个属性。
func redactAttr(a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedPlaceholder)
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, redactString(a.Value.String()))
	}
	return a
}

// replaceAttr 把 slog 默认字段名换成规范名，并做值脱敏。
//
// 级别字段由 standardHandler 产出（需要产出多个顶层字段）。
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return slog.Attr{}
	}
	switch a.Key {
	case slog.TimeKey:
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String("timestamp", t.UTC().Format(time.RFC3339Nano))
		}
	case slog.LevelKey:
		// slog.LevelKey 的字面值就是 "level"，与我们要补的 ECS 别名同名。
		// 因此必须同时确认值的类型是 slog.Level（slog 内建属性的形态），
		// standardHandler 补的别名值是 string，会正常通过。
		if _, isLevel := a.Value.Any().(slog.Level); isLevel {
			return slog.Attr{}
		}
	case slog.MessageKey:
		return slog.String("msg", redactString(a.Value.String()))
	}
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedPlaceholder)
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, redactString(a.Value.String()))
	}
	return a
}

// replaceAttrNoRedact 是关闭脱敏时的变体（仅测试用）。
func replaceAttrNoRedact(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return slog.Attr{}
	}
	switch a.Key {
	case slog.TimeKey:
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String("timestamp", t.UTC().Format(time.RFC3339Nano))
		}
	case slog.LevelKey:
		if _, isLevel := a.Value.Any().(slog.Level); isLevel {
			return slog.Attr{}
		}
	case slog.MessageKey:
		return slog.String("msg", a.Value.String())
	}
	return a
}

// NewStandardHandler 返回符合 rule 71 的 slog.Handler（新建 JSON 输出）。
func NewStandardHandler(w interface{ Write([]byte) (int, error) }, opts StandardOptions) (slog.Handler, error) {
	res, err := NewResource(opts)
	if err != nil {
		return nil, err
	}
	attrFn := replaceAttr
	if opts.DisableRedact {
		attrFn = replaceAttrNoRedact
	}
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       opts.Level,
		ReplaceAttr: attrFn,
	})
	return &standardHandler{
		inner:  inner,
		res:    res,
		redact: !opts.DisableRedact,
	}, nil
}

// NewStderrHandler 是 NewStandardHandler 的 stderr 快捷方式。
func NewStderrHandler(opts StandardOptions) (slog.Handler, error) {
	return NewStandardHandler(os.Stderr, opts)
}

// NewStdoutHandler 是 NewStandardHandler 的 stdout 快捷方式。
func NewStdoutHandler(opts StandardOptions) (slog.Handler, error) {
	return NewStandardHandler(os.Stdout, opts)
}

// WrapStandardHandler 在既有 handler 之上叠加规范字段与脱敏。
//
// 前置条件：inner 必须是 JSON handler，否则输出仍是纯文本，
// 违反 rule 71 的 JSONL 要求（logging.go 的 Init 已满足）。
func WrapStandardHandler(inner slog.Handler, opts StandardOptions) (slog.Handler, error) {
	if inner == nil {
		return nil, fmt.Errorf("logging: 内层 handler 不能为空")
	}
	res, err := NewResource(opts)
	if err != nil {
		return nil, err
	}
	return &standardHandler{
		inner:  inner,
		res:    res,
		redact: !opts.DisableRedact,
	}, nil
}
