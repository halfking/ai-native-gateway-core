package db

// ddl_definition_guard.go —— R60 修正轮（S6-1, 2026-09-23）boot 链
// POLICY/TRIGGER 守卫的"定义感知"比较层。
//
// 背景：boot 链上 ~26 处 CREATE POLICY 与 ~7 处 CREATE TRIGGER 每次启动
// 无条件 DROP+CREATE，每条都要排目标表的 ACCESS EXCLUSIVE 锁队列；共享库
// 持续写流下锁等待超角色级 statement_timeout=30s 即被 57014 击杀（boot 烧点
// 家族 7b5627e6e..0a5ad6b20 已实证并收口了列/索引/约束家族，本文件把同一
// 纪律带到 POLICY/TRIGGER 家族）。
//
// 与既有"存在性守卫"（只查 conname/tgname 是否存在）不同，本层是**定义感知**
// 的：只有当存储定义与期望定义完全等价时才允许调用方跳过 DDL；任何定义漂移
// （策略演进、trigger WHEN/列清单变化、手工改动）都必须回落到原 DROP+CREATE
// 路径，保证策略演进能落到存量安装，且 SKIP 与执行的最终库态等价。
//
// 分层（约束 2）：
//   - 纯函数（本文件大部分）：期望 DDL 解析（parsePolicyDDL/parseTriggerDDL）、
//     存储定义规范化（canonicalizeExpr / parseTriggerDDL 同一解析器）、
//     等价比较（policyMatches / triggerDefEqual）、DROP 语句生成
//     （dropPolicySQL/dropTriggerSQL）。全部可脱离真库单测。
//   - 薄 SQL 层（文件底部 *DB 方法）：把 catalog 读数喂给纯函数，出错一律
//     fail-open（返回 false，调用方执行原 DDL，等价于守卫不存在）。
//
// 规范化规则（对照 PG17 pg_get_expr/pg_get_triggerdef 实测渲染，见
// ddl_definition_guard_test.go 用例）：
//   - 关键字/未加引号标识符统一小写；字符串字面量原样保留；
//   - 函数名/类型名的 public. 与 pg_catalog. 前缀剥除（ruleutils 按
//     search_path 省略限定符：public.get_current_tenant() → get_current_tenant()）；
//   - 文本族 no-op 强制转换剥除（::text / ::varchar / ::character varying /
//     ::pg_catalog.*）：ruleutils 会给 unknown 字面量补 'x'::text、丢掉
//     text→text 同型转换，varchar→text 的 RelabelType 是否保留随上下文，
//     两侧统一剥除后才是可比较的规范形；非文本族的转换保留（语义敏感）；
//   - 括号按 PG 运算符优先级最小化重渲染（ruleutils 对每个操作数全加括号）；
//   - TRIGGER 事件按 ruleutils 固定顺序规范（INSERT, DELETE, UPDATE, TRUNCATE）。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ─────────────────────────── tokenizer ───────────────────────────

type ddlTokKind int

const (
	tkIdent  ddlTokKind = iota // 未加引号标识符/关键字（规范化为小写）
	tkQIdent                   // "quoted identifier"（保留大小写）
	tkString                   // 'string literal'（含引号原样）
	tkNumber                   // 数字字面量
	tkOp                       // 运算符/标点
)

type ddlToken struct {
	kind ddlTokKind
	text string // 规范化文本：tkIdent 小写；tkString 含两侧单引号；tkOp 原样
}

var ddlErrParse = errors.New("ddl definition parse error")

// tokenizeDDL 把 SQL 片段切成 token 流。支持 -- 行注释与 '...'（” 转义）、
// "..."（"" 转义）。任何非法输入返回 error（调用方 fail-open）。
func tokenizeDDL(sql string) ([]ddlToken, error) {
	var toks []ddlToken
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return nil, ddlErrParse
			}
			i += end + 4
		case c == '\'':
			j := i + 1
			var sb strings.Builder
			sb.WriteByte('\'')
			for {
				if j >= n {
					return nil, ddlErrParse
				}
				if sql[j] == '\'' {
					if j+1 < n && sql[j+1] == '\'' {
						sb.WriteString("''")
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(sql[j])
				j++
			}
			sb.WriteByte('\'')
			toks = append(toks, ddlToken{kind: tkString, text: sb.String()})
			i = j + 1
		case c == '"':
			j := i + 1
			var sb strings.Builder
			sb.WriteByte('"')
			for {
				if j >= n {
					return nil, ddlErrParse
				}
				if sql[j] == '"' {
					if j+1 < n && sql[j+1] == '"' {
						sb.WriteString(`""`)
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(sql[j])
				j++
			}
			sb.WriteByte('"')
			toks = append(toks, ddlToken{kind: tkQIdent, text: sb.String()})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < n && ((sql[j] >= '0' && sql[j] <= '9') || sql[j] == '.') {
				j++
			}
			toks = append(toks, ddlToken{kind: tkNumber, text: sql[i:j]})
			i = j
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i
			for j < n {
				d := sql[j]
				if d == '_' || d == '$' || (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9') {
					j++
					continue
				}
				break
			}
			toks = append(toks, ddlToken{kind: tkIdent, text: strings.ToLower(sql[i:j])})
			i = j
		default:
			// 多字符运算符优先
			if i+1 < n {
				two := sql[i : i+2]
				switch two {
				case "::", "<=", ">=", "<>", "!=", "||":
					toks = append(toks, ddlToken{kind: tkOp, text: two})
					i += 2
					continue
				}
			}
			switch c {
			case '=', '<', '>', '+', '-', '*', '/', '%', '(', ')', ',', '.', ';':
				toks = append(toks, ddlToken{kind: tkOp, text: string(c)})
				i++
			default:
				return nil, fmt.Errorf("%w: unexpected byte %q at offset %d", ddlErrParse, c, i)
			}
		}
	}
	return toks, nil
}

// ─────────────────────────── expression AST ───────────────────────────

// PG 运算符优先级（高→低）：:: > 一元-> */% > +- > 比较 > IS > NOT > AND > OR。
const (
	precOr      = 1
	precAnd     = 2
	precNot     = 3
	precIs      = 4
	precCmp     = 5
	precAdd     = 6
	precMul     = 7
	precUnary   = 8
	precCast    = 9
	precPrimary = 10
)

type exprNode interface {
	// render 按最小括号规范形渲染；minPrec 为当前上下文要求的最低优先级，
	// 子节点优先级低于它时加括号。
	render(minPrec int) string
}

type litNode struct{ text string }

func (n litNode) render(int) string { return n.text }

type identParts struct{ parts []string }

func (n identParts) render(int) string { return strings.Join(n.parts, ".") }

type starNode struct{}

func (starNode) render(int) string { return "*" }

type recordStarNode struct{ parts []string }

func (n recordStarNode) render(minPrec int) string {
	return parenthesize(strings.Join(n.parts, ".")+".*", precPrimary, minPrec)
}

type funcCallNode struct {
	name string // 限定符已剥除的规范名
	args []exprNode
}

func (n funcCallNode) render(int) string {
	args := make([]string, len(n.args))
	for i, a := range n.args {
		args[i] = a.render(precOr)
	}
	return n.name + "(" + strings.Join(args, ", ") + ")"
}

type castNode struct {
	operand  exprNode
	typeName []string // 规范化（小写、剥限定符）的类型名 token 序列
}

// noiseCastTypes —— ruleutils 机械引入/丢弃的文本族转换，比较时两侧剥除。
var noiseCastTypes = map[string]bool{
	"text":              true,
	"varchar":           true,
	"character varying": true,
	"character":         true,
	"bpchar":            true,
}

func (n castNode) typeNameString() string { return strings.Join(n.typeName, " ") }

func (n castNode) render(minPrec int) string {
	if noiseCastTypes[n.typeNameString()] {
		// no-op 文本族转换：透明化（等价于 PG 丢弃同型转换的行为）
		return n.operand.render(minPrec)
	}
	return parenthesize(n.operand.render(precCast+1)+"::"+n.typeNameString(), precCast, minPrec)
}

type unaryNode struct {
	op      string // "not" / "-"
	operand exprNode
}

func (n unaryNode) render(minPrec int) string {
	p := precNot
	if n.op == "-" {
		p = precUnary
	}
	return parenthesize(n.op+" "+n.operand.render(p+1), p, minPrec)
}

type isNullNode struct {
	operand exprNode
	negated bool
}

func (n isNullNode) render(minPrec int) string {
	kw := "is null"
	if n.negated {
		kw = "is not null"
	}
	return parenthesize(n.operand.render(precIs+1)+" "+kw, precIs, minPrec)
}

type binaryNode struct {
	op   string // or/and/=/<>/</>/<=/>=/+/-/*/'/'/'%'/'is distinct from'/'is not distinct from'
	prec int
	l, r exprNode
}

// render 统一左结合：左操作数允许同级（链式 or/and 扁平化），右操作数要求
// 更高优先级；两侧经同一渲染规则，比较对称。
func (n binaryNode) render(minPrec int) string {
	return parenthesize(n.l.render(n.prec)+" "+n.op+" "+n.r.render(n.prec+1), n.prec, minPrec)
}

func parenthesize(s string, prec, minPrec int) string {
	if prec < minPrec {
		return "(" + s + ")"
	}
	return s
}

// ─────────────────────── expression parser ───────────────────────

type exprParser struct {
	toks []ddlToken
	pos  int
}

func (p *exprParser) peek() (ddlToken, bool) {
	if p.pos >= len(p.toks) {
		return ddlToken{}, false
	}
	return p.toks[p.pos], true
}

func (p *exprParser) next() (ddlToken, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func (p *exprParser) isOp(text string) bool {
	t, ok := p.peek()
	return ok && t.kind == tkOp && t.text == text
}

func (p *exprParser) isIdent(text string) bool {
	t, ok := p.peek()
	return ok && t.kind == tkIdent && t.text == text
}

func (p *exprParser) eatOp(text string) bool {
	if p.isOp(text) {
		p.pos++
		return true
	}
	return false
}

func (p *exprParser) eatIdent(text string) bool {
	if p.isIdent(text) {
		p.pos++
		return true
	}
	return false
}

// parseExpression 解析一个完整表达式（在匹配的右括号/语句边界处自然停止）。
func parseExpression(toks []ddlToken) (exprNode, error) {
	p := &exprParser{toks: toks}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if _, more := p.peek(); more {
		return nil, fmt.Errorf("%w: trailing tokens after expression", ddlErrParse)
	}
	return node, nil
}

func (p *exprParser) parseOr() (exprNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.eatIdent("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: "or", prec: precOr, l: left, r: right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (exprNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.eatIdent("and") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: "and", prec: precAnd, l: left, r: right}
	}
	return left, nil
}

func (p *exprParser) parseNot() (exprNode, error) {
	if p.isIdent("not") {
		p.pos++
		operand, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return unaryNode{op: "not", operand: operand}, nil
	}
	return p.parseIS()
}

func (p *exprParser) parseIS() (exprNode, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for {
		if !p.isIdent("is") {
			return left, nil
		}
		p.pos++
		negated := p.eatIdent("not")
		if p.eatIdent("distinct") {
			if !p.eatIdent("from") {
				return nil, fmt.Errorf("%w: IS [NOT] DISTINCT FROM missing FROM", ddlErrParse)
			}
			right, err := p.parseCmp()
			if err != nil {
				return nil, err
			}
			kw := "is distinct from"
			if negated {
				kw = "is not distinct from"
			}
			left = binaryNode{op: kw, prec: precIs, l: left, r: right}
			continue
		}
		if !negated && !p.eatIdent("null") {
			return nil, fmt.Errorf("%w: unsupported IS form", ddlErrParse)
		}
		if negated && !p.eatIdent("null") {
			return nil, fmt.Errorf("%w: IS NOT missing NULL", ddlErrParse)
		}
		left = isNullNode{operand: left, negated: negated}
	}
}

var cmpOps = map[string]bool{"=": true, "<>": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true}

func (p *exprParser) parseCmp() (exprNode, error) {
	left, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tkOp || !cmpOps[t.text] {
			return left, nil
		}
		p.pos++
		right, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		op := t.text
		if op == "!=" {
			op = "<>"
		}
		left = binaryNode{op: op, prec: precCmp, l: left, r: right}
	}
}

func (p *exprParser) parseAdd() (exprNode, error) {
	left, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tkOp || (t.text != "+" && t.text != "-") {
			return left, nil
		}
		p.pos++
		right, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: t.text, prec: precAdd, l: left, r: right}
	}
}

func (p *exprParser) parseMul() (exprNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tkOp || (t.text != "*" && t.text != "/" && t.text != "%") {
			return left, nil
		}
		// "*" 可能是 count(*) 的裸星——由 parsePrimary 内部消化；走到这里
		// 说明左侧已是一个完整操作数，按乘法处理是正确的歧义消解。
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: t.text, prec: precMul, l: left, r: right}
	}
}

func (p *exprParser) parseUnary() (exprNode, error) {
	if p.isOp("-") {
		p.pos++
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return unaryNode{op: "-", operand: operand}, nil
	}
	return p.parsePostfix()
}

func (p *exprParser) parsePostfix() (exprNode, error) {
	node, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.isOp("::") {
		p.pos++
		typeName, err := p.parseTypeChain()
		if err != nil {
			return nil, err
		}
		node = castNode{operand: node, typeName: typeName}
	}
	return node, nil
}

// parseTypeChain 解析类型名：ident[.ident...] 或 multi-word（character varying）
// 及带 typmod 的 numeric(10,2) 形式。
func (p *exprParser) parseTypeChain() ([]string, error) {
	var parts []string
	for {
		t, ok := p.next()
		if !ok || (t.kind != tkIdent && t.kind != tkQIdent) {
			return nil, fmt.Errorf("%w: bad type name", ddlErrParse)
		}
		parts = append(parts, stripNoiseQualifier(t.text))
		if p.isOp("(") { // typmod，如 numeric(10,2)
			depth := 0
			for {
				tt, ok := p.next()
				if !ok {
					return nil, ddlErrParse
				}
				if tt.kind == tkOp && tt.text == "(" {
					depth++
				}
				if tt.kind == tkOp && tt.text == ")" {
					depth--
					if depth == 0 {
						break
					}
				}
				parts = append(parts, tt.text)
			}
		}
		if p.isOp(".") {
			p.pos++
			continue
		}
		// multi-word 类型名（character varying / double precision / timestamp
		// with time zone 等）：后续 ident 且非关键字边界时并续。为保守起见只
		// 并续白名单里的第二个词。
		if t2, ok := p.peek(); ok && t2.kind == tkIdent && multiWordTypeSecond[t2.text] {
			p.pos++
			parts = append(parts, t2.text)
			if t3, ok := p.peek(); ok && t3.kind == tkIdent && (t3.text == "varying" || t3.text == "zone" || t3.text == "precision") {
				p.pos++
				parts = append(parts, t3.text)
			}
		}
		return parts, nil
	}
}

var multiWordTypeSecond = map[string]bool{
	"varying":   true,
	"precision": true,
	"with":      true,
	"without":   true,
}

// stripNoiseQualifier 剥除 public./pg_catalog. 限定符（ruleutils 按 search_path
// 省略它们，两侧必须归一）。
func stripNoiseQualifier(name string) string {
	for _, q := range []string{"public.", "pg_catalog."} {
		if strings.HasPrefix(name, q) {
			return strings.TrimPrefix(name, q)
		}
	}
	return name
}

func (p *exprParser) parsePrimary() (exprNode, error) {
	t, ok := p.next()
	if !ok {
		return nil, ddlErrParse
	}
	switch t.kind {
	case tkString, tkNumber:
		return litNode{text: t.text}, nil
	case tkIdent:
		switch t.text {
		case "true", "false", "null":
			return litNode{text: t.text}, nil
		case "case", "coalesce_has_no_case":
			// CASE 表达式不在守卫语法内（现有策略/WHEN 均未使用）→ fail-open
			return nil, fmt.Errorf("%w: unsupported expression keyword %q", ddlErrParse, t.text)
		case "in", "between", "like", "ilike", "similar":
			return nil, fmt.Errorf("%w: unsupported predicate %q", ddlErrParse, t.text)
		}
		return p.parseIdentTail([]string{stripNoiseQualifier(t.text)})
	case tkQIdent:
		return p.parseIdentTail([]string{t.text})
	case tkOp:
		if t.text == "(" {
			inner, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.eatOp(")") {
				return nil, fmt.Errorf("%w: missing closing paren", ddlErrParse)
			}
			return inner, nil
		}
		if t.text == "*" {
			return starNode{}, nil
		}
		return nil, fmt.Errorf("%w: unexpected operator %q", ddlErrParse, t.text)
	default:
		return nil, ddlErrParse
	}
}

// parseIdentTail 处理 ident 后续：a.b（限定列）、a.*（record-star）、f(args)。
func (p *exprParser) parseIdentTail(parts []string) (exprNode, error) {
	for {
		if p.isOp(".") {
			// 前瞻："." 后跟 "*" → record-star（OLD.* / NEW.*）
			if p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tkOp && p.toks[p.pos+1].text == "*" {
				p.pos += 2
				return recordStarNode{parts: parts}, nil
			}
			p.pos++
			t, ok := p.next()
			if !ok || (t.kind != tkIdent && t.kind != tkQIdent) {
				return nil, fmt.Errorf("%w: bad qualified name", ddlErrParse)
			}
			parts = append(parts, stripNoiseQualifier(t.text))
			continue
		}
		if p.isOp("(") {
			p.pos++
			var args []exprNode
			if !p.isOp(")") {
				for {
					a, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.eatOp(",") {
						continue
					}
					break
				}
			}
			if !p.eatOp(")") {
				return nil, fmt.Errorf("%w: missing closing paren in call", ddlErrParse)
			}
			name := parts[len(parts)-1]
			return funcCallNode{name: name, args: args}, nil
		}
		return identParts{parts: parts}, nil
	}
}

// canonicalizeExpr 把表达式文本（期望 SQL 体内文或 pg_get_expr 输出）规范为
// 可比较字符串。解析失败返回 error（调用方按漂移处理，fail-open）。
func canonicalizeExpr(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", nil
	}
	toks, err := tokenizeDDL(trimmed)
	if err != nil {
		return "", err
	}
	node, err := parseExpression(toks)
	if err != nil {
		return "", err
	}
	return node.render(0), nil
}

// ─────────────────────────── POLICY 定义 ───────────────────────────

// policyDef 是 CREATE POLICY 的规范化期望定义。
type policyDef struct {
	Name       string   // 小写
	Table      string   // 裸表名（限定符剥除）
	Schema     string   // 省略或 public 时为 "public"
	Cmd        string   // ALL/SELECT/INSERT/UPDATE/DELETE（默认 ALL）
	Roles      []string // 小写、排序（默认 {public}）
	Permissive string   // PERMISSIVE/RESTRICTIVE（默认 PERMISSIVE）
	Using      string   // USING 体规范形；无 USING 为 ""
	WithCheck  string   // WITH CHECK 体规范形；缺省为 ""
}

// parsePolicyDDL 解析一条 CREATE POLICY 语句为规范化定义。语句可以带
// DROP POLICY 前后缀以外的任意空白/注释；分号可有可无。
func parsePolicyDDL(sql string) (policyDef, error) {
	toks, err := tokenizeDDL(sql)
	if err != nil {
		return policyDef{}, err
	}
	p := &exprParser{toks: toks}
	def := policyDef{Schema: "public", Cmd: "ALL", Permissive: "PERMISSIVE"}
	if !p.eatIdent("create") {
		return policyDef{}, fmt.Errorf("%w: expected CREATE", ddlErrParse)
	}
	if p.eatIdent("or") {
		if !p.eatIdent("replace") {
			return policyDef{}, ddlErrParse
		}
	}
	if !p.eatIdent("policy") {
		return policyDef{}, fmt.Errorf("%w: expected POLICY", ddlErrParse)
	}
	nameTok, ok := p.next()
	if !ok || (nameTok.kind != tkIdent && nameTok.kind != tkQIdent) {
		return policyDef{}, fmt.Errorf("%w: bad policy name", ddlErrParse)
	}
	def.Name = nameTok.text
	if !p.eatIdent("on") {
		return policyDef{}, fmt.Errorf("%w: expected ON", ddlErrParse)
	}
	schema := "public"
	t, ok := p.next()
	if !ok || (t.kind != tkIdent && t.kind != tkQIdent) {
		return policyDef{}, fmt.Errorf("%w: bad table name", ddlErrParse)
	}
	if p.isOp(".") {
		p.pos++
		t2, ok := p.next()
		if !ok || (t2.kind != tkIdent && t2.kind != tkQIdent) {
			return policyDef{}, fmt.Errorf("%w: bad qualified table", ddlErrParse)
		}
		schema = t.text
		t = t2
	}
	def.Schema = schema
	def.Table = t.text

	if p.eatIdent("as") {
		t, ok := p.next()
		if !ok || t.kind != tkIdent || (t.text != "permissive" && t.text != "restrictive") {
			return policyDef{}, fmt.Errorf("%w: bad AS clause", ddlErrParse)
		}
		def.Permissive = strings.ToUpper(t.text)
	}
	if p.eatIdent("for") {
		t, ok := p.next()
		if !ok || t.kind != tkIdent {
			return policyDef{}, ddlErrParse
		}
		switch t.text {
		case "all", "select", "insert", "update", "delete":
			def.Cmd = strings.ToUpper(t.text)
		default:
			return policyDef{}, fmt.Errorf("%w: bad FOR cmd %q", ddlErrParse, t.text)
		}
	}
	if p.eatIdent("to") {
		var roles []string
		for {
			rt, ok := p.next()
			if !ok || (rt.kind != tkIdent && rt.kind != tkQIdent && rt.kind != tkString) {
				return policyDef{}, fmt.Errorf("%w: bad TO role", ddlErrParse)
			}
			roles = append(roles, rt.text)
			if p.eatOp(",") {
				continue
			}
			break
		}
		def.Roles = roles
	} else {
		def.Roles = []string{"public"}
	}
	if p.eatIdent("using") {
		q, err := p.parseParenExpr()
		if err != nil {
			return policyDef{}, err
		}
		def.Using = q
	}
	if p.eatIdent("with") {
		if !p.eatIdent("check") {
			return policyDef{}, fmt.Errorf("%w: WITH CHECK expected", ddlErrParse)
		}
		wc, err := p.parseParenExpr()
		if err != nil {
			return policyDef{}, err
		}
		def.WithCheck = wc
	}
	p.eatOp(";")
	if _, more := p.peek(); more {
		return policyDef{}, fmt.Errorf("%w: trailing tokens after CREATE POLICY", ddlErrParse)
	}
	sort.Strings(def.Roles)
	return def, nil
}

// parseParenExpr 消费 "( expr )" 并返回 expr 的规范形。
func (p *exprParser) parseParenExpr() (string, error) {
	if !p.eatOp("(") {
		return "", fmt.Errorf("%w: expected ( after USING/WITH CHECK/WHEN", ddlErrParse)
	}
	node, err := p.parseOr()
	if err != nil {
		return "", err
	}
	if !p.eatOp(")") {
		return "", fmt.Errorf("%w: missing closing paren", ddlErrParse)
	}
	return node.render(0), nil
}

// storedPolicyRow 对应 pg_policies 的一行读数。
type storedPolicyRow struct {
	Cmd        string
	Permissive string
	Roles      []string
	Qual       *string // pg_policies.qual（NULL → nil）
	WithCheck  *string // pg_policies.with_check（NULL → nil）
}

// policyMatches 判定存储行是否与期望定义等价。任何规范化失败按不等价处理
// （fail-open：调用方重放 DDL，最终态与执行路径一致）。
func policyMatches(def policyDef, stored storedPolicyRow) bool {
	if def.Cmd != strings.ToUpper(stored.Cmd) {
		return false
	}
	if def.Permissive != strings.ToUpper(stored.Permissive) {
		return false
	}
	if len(def.Roles) != len(stored.Roles) {
		return false
	}
	storedRoles := make([]string, len(stored.Roles))
	for i, r := range stored.Roles {
		storedRoles[i] = strings.ToLower(r)
	}
	sort.Strings(storedRoles)
	for i := range def.Roles {
		if def.Roles[i] != storedRoles[i] {
			return false
		}
	}
	storedQual := ""
	if stored.Qual != nil {
		storedQual = *stored.Qual
	}
	gotQual, err := canonicalizeExpr(storedQual)
	if err != nil || gotQual != def.Using {
		return false
	}
	storedWC := ""
	if stored.WithCheck != nil {
		storedWC = *stored.WithCheck
	}
	gotWC, err := canonicalizeExpr(storedWC)
	if err != nil || gotWC != def.WithCheck {
		return false
	}
	return true
}

// dropPolicySQL 由期望定义生成 DROP 语句（守卫未命中时的回收路径，与原
// 内联 `DROP POLICY IF EXISTS ... ON ...` 语义一致）。
func dropPolicySQL(def policyDef) string {
	return fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s.%s", def.Name, def.Schema, def.Table)
}

// ─────────────────────────── TRIGGER 定义 ───────────────────────────

type triggerEvent struct {
	Kind string   // insert/delete/update/truncate（规范序：insert,delete,update,truncate）
	Cols []string // UPDATE OF 列清单（保序）
}

// triggerDef 是 CREATE TRIGGER 的规范化期望定义。
type triggerDef struct {
	Name     string
	Timing   string // BEFORE/AFTER/INSTEAD OF
	Events   []triggerEvent
	Table    string   // 裸表名
	Schema   string   // 省略或 public 时为 "public"
	ForEach  string   // ROW/STATEMENT（默认 STATEMENT）
	When     string   // WHEN 体规范形；无 WHEN 为 ""
	Function string   // 限定符剥除的函数名
	Args     []string // 规范化参数 token 文本
}

var triggerEventOrder = map[string]int{"insert": 0, "delete": 1, "update": 2, "truncate": 3}

// parseTriggerDDL 既能解析源码中的 CREATE TRIGGER 期望语句，也能解析
// pg_get_triggerdef(oid) 的存储渲染（同一语法，两侧同一解析器 → 比较对称）。
func parseTriggerDDL(sql string) (triggerDef, error) {
	toks, err := tokenizeDDL(sql)
	if err != nil {
		return triggerDef{}, err
	}
	p := &exprParser{toks: toks}
	def := triggerDef{Schema: "public", ForEach: "STATEMENT"}
	if !p.eatIdent("create") {
		return triggerDef{}, fmt.Errorf("%w: expected CREATE", ddlErrParse)
	}
	if p.eatIdent("or") {
		if !p.eatIdent("replace") {
			return triggerDef{}, ddlErrParse
		}
	}
	if !p.eatIdent("trigger") {
		return triggerDef{}, fmt.Errorf("%w: expected TRIGGER", ddlErrParse)
	}
	nameTok, ok := p.next()
	if !ok || (nameTok.kind != tkIdent && nameTok.kind != tkQIdent) {
		return triggerDef{}, fmt.Errorf("%w: bad trigger name", ddlErrParse)
	}
	def.Name = nameTok.text

	switch {
	case p.eatIdent("before"):
		def.Timing = "BEFORE"
	case p.eatIdent("after"):
		def.Timing = "AFTER"
	case p.eatIdent("instead"):
		if !p.eatIdent("of") {
			return triggerDef{}, ddlErrParse
		}
		def.Timing = "INSTEAD OF"
	default:
		return triggerDef{}, fmt.Errorf("%w: bad trigger timing", ddlErrParse)
	}

	var events []triggerEvent
	for {
		t, ok := p.next()
		if !ok || t.kind != tkIdent {
			return triggerDef{}, fmt.Errorf("%w: bad trigger event", ddlErrParse)
		}
		ev := triggerEvent{Kind: t.text}
		switch t.text {
		case "insert", "delete", "truncate":
		case "update":
			if p.eatIdent("of") {
				for {
					ct, ok := p.next()
					if !ok || (ct.kind != tkIdent && ct.kind != tkQIdent) {
						return triggerDef{}, fmt.Errorf("%w: bad UPDATE OF column", ddlErrParse)
					}
					ev.Cols = append(ev.Cols, ct.text)
					if p.eatOp(",") {
						continue
					}
					break
				}
			}
		default:
			return triggerDef{}, fmt.Errorf("%w: unknown event %q", ddlErrParse, t.text)
		}
		events = append(events, ev)
		if p.eatIdent("or") {
			continue
		}
		break
	}
	sort.SliceStable(events, func(i, j int) bool {
		return triggerEventOrder[events[i].Kind] < triggerEventOrder[events[j].Kind]
	})
	def.Events = events

	if !p.eatIdent("on") {
		return triggerDef{}, fmt.Errorf("%w: expected ON", ddlErrParse)
	}
	schema := "public"
	t, ok := p.next()
	if !ok || (t.kind != tkIdent && t.kind != tkQIdent) {
		return triggerDef{}, fmt.Errorf("%w: bad trigger table", ddlErrParse)
	}
	if p.isOp(".") {
		p.pos++
		t2, ok := p.next()
		if !ok || (t2.kind != tkIdent && t2.kind != tkQIdent) {
			return triggerDef{}, fmt.Errorf("%w: bad qualified trigger table", ddlErrParse)
		}
		schema = t.text
		t = t2
	}
	def.Schema = schema
	def.Table = t.text

	if p.eatIdent("from") {
		// 继承父表（分区触发器）——当前未使用，容错解析
		if _, ok := p.next(); !ok {
			return triggerDef{}, ddlErrParse
		}
	}
	if p.eatIdent("referencing") {
		return triggerDef{}, fmt.Errorf("%w: REFERENCING clause unsupported", ddlErrParse)
	}
	if p.eatIdent("for") {
		p.eatIdent("each")
		t, ok := p.next()
		if !ok || t.kind != tkIdent || (t.text != "row" && t.text != "statement") {
			return triggerDef{}, fmt.Errorf("%w: bad FOR [EACH] clause", ddlErrParse)
		}
		def.ForEach = strings.ToUpper(t.text)
	}
	if p.eatIdent("when") {
		w, err := p.parseParenExpr()
		if err != nil {
			return triggerDef{}, err
		}
		def.When = w
	}
	if !(p.eatIdent("execute") && (p.eatIdent("function") || p.eatIdent("procedure"))) {
		return triggerDef{}, fmt.Errorf("%w: expected EXECUTE FUNCTION/PROCEDURE", ddlErrParse)
	}
	fnTok, ok := p.next()
	if !ok || (fnTok.kind != tkIdent && fnTok.kind != tkQIdent) {
		return triggerDef{}, fmt.Errorf("%w: bad trigger function name", ddlErrParse)
	}
	fnName := fnTok.text
	if p.isOp(".") {
		p.pos++
		t2, ok := p.next()
		if !ok || (t2.kind != tkIdent && t2.kind != tkQIdent) {
			return triggerDef{}, fmt.Errorf("%w: bad qualified function name", ddlErrParse)
		}
		fnName = t2.text
	}
	def.Function = stripNoiseQualifier(fnName)
	if !p.eatOp("(") {
		return triggerDef{}, fmt.Errorf("%w: expected ( after trigger function", ddlErrParse)
	}
	for !p.isOp(")") {
		arg, err := p.parseTriggerArg()
		if err != nil {
			return triggerDef{}, err
		}
		def.Args = append(def.Args, arg)
		if !p.eatOp(",") {
			break
		}
	}
	if !p.eatOp(")") {
		return triggerDef{}, fmt.Errorf("%w: missing closing paren in EXECUTE args", ddlErrParse)
	}
	p.eatOp(";")
	if _, more := p.peek(); more {
		return triggerDef{}, fmt.Errorf("%w: trailing tokens after CREATE TRIGGER", ddlErrParse)
	}
	return def, nil
}

// parseTriggerArg 规范化单个 EXECUTE 参数（通常是字符串字面量）。
func (p *exprParser) parseTriggerArg() (string, error) {
	start := p.pos
	if _, err := p.parseOr(); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i := start; i < p.pos; i++ {
		if i > start {
			sb.WriteByte(' ')
		}
		sb.WriteString(p.toks[i].text)
	}
	return sb.String(), nil
}

// triggerDefEqual 判定两个规范化定义是否等价。
func triggerDefEqual(a, b triggerDef) bool {
	if a.Name != b.Name || a.Timing != b.Timing || a.Table != b.Table ||
		a.Schema != b.Schema || a.ForEach != b.ForEach || a.When != b.When ||
		a.Function != b.Function || len(a.Events) != len(b.Events) || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Events {
		if a.Events[i].Kind != b.Events[i].Kind || len(a.Events[i].Cols) != len(b.Events[i].Cols) {
			return false
		}
		for j := range a.Events[i].Cols {
			if a.Events[i].Cols[j] != b.Events[i].Cols[j] {
				return false
			}
		}
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	return true
}

// dropTriggerSQL 由期望定义生成 DROP 语句。
func dropTriggerSQL(def triggerDef) string {
	return fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s.%s", def.Name, def.Schema, def.Table)
}

// ─────────────────────── 薄 SQL 层（fail-open） ───────────────────────

// parsePolicyDDLs 批量解析期望 DDL（纯函数入口的批量包装）。
func parsePolicyDDLs(ddls ...string) ([]policyDef, error) {
	defs := make([]policyDef, 0, len(ddls))
	for _, s := range ddls {
		def, err := parsePolicyDDL(s)
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// policiesCurrent 报告 table 上每个期望 policy 是否都存在且定义等价，且
// absentNames 列出的策略名都不存在（用于 omnifree legacy 名回收检查）。
// 任何错误（含解析失败）都返回 false —— 调用方执行原 DDL。
func (d *DB) policiesCurrent(ctx context.Context, table string, policyDDLs []string, absentNames ...string) bool {
	if d == nil || d.pool == nil || len(policyDDLs) == 0 {
		return false
	}
	defs, err := parsePolicyDDLs(policyDDLs...)
	if err != nil || len(defs) == 0 {
		return false
	}
	for _, def := range defs {
		if def.Schema != "public" {
			return false
		}
		if def.Table != table {
			// 期望 DDL 与调用方声明的表不一致：视为漂移，fail-open
			return false
		}
	}
	names := make([]string, 0, len(defs)+len(absentNames))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	names = append(names, absentNames...)
	rows, err := d.pool.Query(ctx, `
		SELECT policyname, cmd, permissive, roles, qual, with_check
		FROM pg_policies
		WHERE schemaname = 'public' AND tablename = $1 AND policyname = ANY($2)
	`, table, names)
	if err != nil {
		return false
	}
	defer rows.Close()
	stored := map[string]storedPolicyRow{}
	for rows.Next() {
		var name string
		var row storedPolicyRow
		if err := rows.Scan(&name, &row.Cmd, &row.Permissive, &row.Roles, &row.Qual, &row.WithCheck); err != nil {
			return false
		}
		stored[name] = row
	}
	if err := rows.Err(); err != nil {
		return false
	}
	for _, def := range defs {
		row, ok := stored[def.Name]
		if !ok || !policyMatches(def, row) {
			return false
		}
	}
	for _, absent := range absentNames {
		if _, ok := stored[absent]; ok {
			return false
		}
	}
	return true
}

// triggerCurrent 报告 table 上的单个 trigger 是否存在且 pg_get_triggerdef
// 渲染与期望定义等价。任何错误返回 false（fail-open）。
func (d *DB) triggerCurrent(ctx context.Context, table string, triggerDDL string) bool {
	defs, err := parseTriggerDDLs(triggerDDL)
	if err != nil || len(defs) != 1 {
		return false
	}
	def := defs[0]
	if def.Schema != "public" || def.Table != table {
		return false
	}
	var storedDef *string
	if err := d.pool.QueryRow(ctx, `
		SELECT pg_get_triggerdef(oid)::text
		FROM pg_trigger
		WHERE tgrelid = to_regclass('public.' || $1::text)
		  AND NOT tgisinternal
		  AND tgname = $2
	`, table, def.Name).Scan(&storedDef); err != nil {
		return false
	}
	if storedDef == nil {
		return false
	}
	stored, err := parseTriggerDDL(*storedDef)
	if err != nil {
		return false
	}
	return triggerDefEqual(def, stored)
}

// triggersCurrent 批量版本：全部 trigger 等价才返回 true。
func (d *DB) triggersCurrent(ctx context.Context, table string, triggerDDLs []string) bool {
	if d == nil || d.pool == nil || len(triggerDDLs) == 0 {
		return false
	}
	for _, ddl := range triggerDDLs {
		if !d.triggerCurrent(ctx, table, ddl) {
			return false
		}
	}
	return true
}

// parseTriggerDDLs 批量解析期望 trigger DDL。
func parseTriggerDDLs(ddls ...string) ([]triggerDef, error) {
	defs := make([]triggerDef, 0, len(ddls))
	for _, s := range ddls {
		def, err := parseTriggerDDL(s)
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// rlsFlagsCurrent 报告 public.table 的 relrowsecurity（以及可选的
// relforcerowsecurity）是否已置位。表不存在 / 查询出错 → false（fail-open）。
func (d *DB) rlsFlagsCurrent(ctx context.Context, table string, needForce bool) bool {
	if d == nil || d.pool == nil {
		return false
	}
	var enabled, forced bool
	err := d.pool.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class
		WHERE oid = to_regclass('public.' || $1::text)
	`, table).Scan(&enabled, &forced)
	if err != nil {
		return false
	}
	if !enabled {
		return false
	}
	if needForce && !forced {
		return false
	}
	return true
}

// rlsPoliciesCurrent —— 组合守卫：RLS（可选 FORCE）已启用 且 每个期望
// policy 定义与 catalog 等价（absentNames 需不存在）。命中即可整段跳过
// ENABLE[/FORCE] + DROP + CREATE POLICY 块；未命中执行原语句，最终态与
// 每-boot 无条件执行完全一致。
func (d *DB) rlsPoliciesCurrent(ctx context.Context, table string, needForce bool, policyDDLs []string, absentNames ...string) bool {
	if !d.rlsFlagsCurrent(ctx, table, needForce) {
		return false
	}
	return d.policiesCurrent(ctx, table, policyDDLs, absentNames...)
}
