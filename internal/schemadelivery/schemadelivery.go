// Package schemadelivery 回答一个问题，给导入它的门用：
//
//	「生产代码的 SQL 引用的表和列，有没有一个【部署与新装真的会执行】的来源创建它？」
//
// # 为什么需要它（2026-11-26，同一类缺口第三次）
//
//	820fcc4f0 让 API Key 鉴权路径（domains/authentication 的 keystore_sync／verifier）
//	与 admin/keys 直接读写 api_keys.capabilities，而创建这一列的文件只在仓根散目录
//	migrations/ 里、不在任何投递通道 ⇒ 部署到没有这一列的库 ⇒ 42703 ⇒ 鉴权整体不可用。
//	相关包的测试全绿：它们不跑在「由投递迁移建出来的 schema」上。
//
//	前两次同形：973 的 raw_model_name 列在生产上不存在（统计管线静默丢数）、
//	055–058 团队族四表从来没被任何通道建过。那两次的文件在 sql/migrations/startup 里，
//	已有 TestStartupMigrationsStayInEveryDeliveryChannel 盯着；而文件一旦落在
//	那个目录之外，对既有门就完全不可见 ⇒ 本库补的是这一段。
//
// # 判据
//
//	认可的创建来源（五处）：
//	  ① 部署通道点名的迁移（scripts/apply-db-revision-sequence.sh 的 files=()）
//	  ② 安装器通道点名的迁移（sql/schema/installed_startup_migrations.tsv）
//	  ③ 认可投递位目录里的 SQL：新装基线 sql/schema、离线升级副本 installer/migrations
//	  ④ 生产 Go 代码里 SQL 字面量自带的 DDL（代码里 CREATE TABLE／ALTER TABLE … ADD COLUMN）
//	  ⑤ 生产 Go 代码 //go:embed 进来的 SQL 文件（由二进制自己在运行时执行）
//
//	「生产 Go」＝ 非 _test.go，且不在 tests/、scripts/ 下（测试工具与一次性脚本不随网关发版：
//	它们既不投递，其中的 SQL 也不算生产引用）。
//	候选来源 ＝ 仓里被 git 认的 .sql 文件里，不属于①②③、也不是⑤的那些。
//	未投递对象 ＝ 候选来源定义的表与列之中，①–⑤ 都没创建的。
//	引用 ＝ 生产 Go 文件的字符串字面量里，一段 SQL 形态的文本对该对象的**绑定引用**（见下）。
//
//	⇒ 没登记的引用 ⇒ 判红（Result.References）：要么把它搬进投递通道并登记，要么别引用它。
//	已知欠账登记在 known_undelivered.go，门对命中的登记不红，但两头核账——
//	登记了却不再被命中的条目也红（Result.Stale）。
//
// # 什么叫「引用」（词法层绑定，不是「表名和列名同时出现」）
//
//	第一版按「同一段 SQL 里既有表名、又有列名整词」判，在真实树上多报了三百多处：
//	`0::bigint AS total_requests`（输出别名）、SQL 注释里的表名、别表的裸列
//	`sensitive_words`、别名 `sam.payload`，全被算成引用 ⇒ 没人能用的门。
//	现在先做 SQL 词法分析（字符串、美元引号、注释里的词都不算），再只认四种绑定：
//
//	  a. 限定名：T.C、schema.T.C、别名.C（别名由本段 SQL 里「表的位置」上的「<表> [AS] <别名>」声明）；
//	  b. INSERT INTO T (c1, c2, …) 的列清单；
//	  c. UPDATE T SET c = …（含 ON CONFLICT … DO UPDATE SET）的赋值目标；
//	  d. 裸列，且整段 SQL 的表位上**只出现一张表**（认不认识的都算）⇒ 裸列只能属于它。
//
//	表对象则要求表名出现在「表的位置」：FROM／JOIN／INTO／UPDATE 等之后、FROM 列表的
//	逗号之后、字面量开头（FROM 子句片段）、或作限定符（T.C）。关键字永远不是表名；
//	别名声明处（`session_titles titles` 里的 titles）、本段声明的 CTE 名都不算。
//
// # 边界（如实登记）
//
//   - 定义只认 CREATE TABLE (…)、ALTER TABLE … ADD [COLUMN]（含一条语句连做几个动作）、
//     列改名与表改名（改名只增不删：新旧两个名字都算已创建）、分区（子表继承母表的列）；
//     索引、约束、函数不算对象。**不认 DROP COLUMN** ⇒ 「引用一个已被通道删掉的列」本库不报。
//     动态 SQL（format/EXECUTE 里的表名列名）与运行时按路径读的 SQL 看不见。
//   - 视图只记名字：已投递来源建成的视图，它的**列不判**（列来自 SELECT 列表，静态上说不清
//     哪个来源「创建」了它——基线里的 model_offers 是 cmb 之上的视图，而散文件 292 对它有
//     一句 ALTER TABLE）。
//   - 绑定靠词法而非语法树：多表语句里的裸列、跨字面量片段声明的别名、子查询作用域都不解析
//     ⇒ 这些形态下的未投递引用会漏报（宁可漏报，也不回到「同时出现就算」）。
//   - ④⑤ 只证明「二进制里有这段 SQL」，不证明它在每条启动路径上都会被执行。上游对
//     api_keys.capabilities 的修法属于这一类：2026-11-26 在由仓内投递建出来的空库上实测，
//     迁移全部跑完这一列**仍然不存在**，是网关启动时的自愈才建的 ⇒ 二进制启动之前读它的
//     东西（安装器的后置步骤、离线工具）仍会 42703。
//   - 认可投递位是一张显式的目录清单：新开一种「会被执行」的目录时要回来登记，
//     否则那里的对象会被当成未投递而报红（宁可先红）。
package schemadelivery

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 判据面读写的仓内相对路径（一律正斜杠）。
const (
	DeployChannelScript = "scripts/apply-db-revision-sequence.sh"
	InstallerManifest   = "sql/schema/installed_startup_migrations.tsv"
	InstallerEmbedDir   = "installer/cmd/llm-gw-installer/embeddata/startup"
	// 新装基线（tests 用；判据面按目录 sql/schema 整体认可）。
	BaselineSchema = "sql/schema/01-schema.sql"
)

// RecognizedDirs 是「放在这里就会被新装或升级执行」的 SQL 目录（判据的认可投递位）：
// 目录里的每个 .sql 都算（down 件除外）。两条通道自己的目录不在此列——那里只有点名的
// 那部分才算（目录里还躺着大量没投递的历史件）。
var RecognizedDirs = []string{
	"sql/schema",           // 新装基线、前置件、种子
	"installer/migrations", // 离线升级通道的投递副本
}

// SkipDir 不进判据面的目录名（按路径段逐个判）。
var SkipDir = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	".build-local": true,
	"testdata":     true,
}

// NonProductionPrefixes 是仓根下「不随网关二进制发版」的顶层目录：测试工具、一次性脚本。
// 它们里面的 Go 代码既不算投递来源（④⑤），其中的 SQL 也不算生产引用——测试工具引用
// tests/ 自己的 schema.sql 是正当的；脚本引用了不存在的列是脚本自己的缺陷，不是部署阻断。
var NonProductionPrefixes = []string{"tests/", "scripts/"}

// Object 是一个 schema 对象：表（Column 为空）或表上的列。名字已归一（小写、去 schema 前缀、去引号）。
type Object struct {
	Table  string
	Column string
}

func (o Object) String() string {
	if o.Column == "" {
		return o.Table
	}
	return o.Table + "." + o.Column
}

// Reference 是生产代码里一处「SQL 绑定引用了未投递对象」的命中。
type Reference struct {
	File   string // 相对仓根
	Line   int
	Object Object
	Source string // 定义该对象的候选来源文件之一（相对仓根）；全部来源见 Result.Undelivered
}

// Result 是一次判定的全部读数。门要看的只有两样：References（没登记的引用）与 Stale
// （登记了却不再命中的账）——两者都空才算过；Known 是已知欠账的现行读数，只打印。
type Result struct {
	ChannelFiles    int                 // 两条通道点名（去重）、且磁盘上读得到的迁移文件数
	MissingFiles    int                 // 通道点名或嵌入、但磁盘上读不到的文件数（如实列出，不进判据面）
	RecognizedFiles int                 // 认可投递位目录里读过的 .sql 文件数
	EmbedFiles      int                 // 被生产代码 //go:embed 进来的 .sql 文件数（去重）
	CandidateFiles  int                 // 候选来源 .sql 文件数
	Delivered       map[Object]string   // 已投递对象 ⇒ 创建它的来源；code:<文件>／embed:<Go 文件>
	Undelivered     map[Object][]string // 只由候选来源定义的对象 ⇒ 定义它的文件（已排序）
	References      []Reference         // 生产 SQL 对未投递对象的绑定引用里**没有登记**的那些 ⇒ 非空即红
	Known           []Reference         // 命中 KnownUndelivered 登记的那些（已知欠账）
	Stale           []string            // 登记了、却不再被任何引用命中的条目 ⇒ 非空即红（修好了就要删登记）
	GoFiles         int                 // 解析过（含 SQL 或含嵌入）的生产 Go 文件数
	ParseErrors     []string            // 解析失败的生产 Go 文件（不进判据面，如实列出）
}

// words 把逗号分隔的词表做成集合。
//
// ★ 这里的词表一律用逗号分隔，别改成空格分隔的词表：仓里另有一道「悬空引用门」
// （internal/relrefs）会扫生产 Go 里的字符串字面量，看到 TRUNCATE x／RETURNING 就当 SQL 上下文，
// 再把 FROM／JOIN／UPDATE 后面的词抓成关系名——空格分隔的关键字表里有 `truncate union`、`from full`、
// `join key`、`update user` 这样的相邻词 ⇒ 被当成三张没人建的表（2026-11-26 实测）。
func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		m[w] = true
	}
	return m
}

var (
	// 表名可以紧跟在这些词之后（裸标识符出现在这里才算「表的位置」）。
	tableIntroducers = words("from,join,into,update,table,using,only,lateral")
	// 字面量开头的裸表名（FROM 子句片段）之后只可能跟这些子句词。
	afterTableClauses = words("where,on,group,order,having,limit,offset,union,except,intersect,returning,window,fetch,for")
	// <表> 之后不可能是别名的词；也是「逗号回看」的止步词。
	sqlKeywords = words(`
		add,all,alter,analyze,and,any,array,as,asc,authorization,backup,begin,by,cascade,case,cast,check,close,
		cluster,collate,column,comment,concurrently,constraint,create,cross,current_date,current_time,
		current_timestamp,current_user,database,default,deferrable,deferred,delete,desc,disable,discard,distinct,
		do,drop,else,enable,end,except,exclude,exclusive,explain,extension,false,fetch,for,foreign,from,full,
		grant,group,having,if,ilike,in,increment,index,inherit,initially,inner,insert,instead,lateral,
		intersect,into,is,join,key,language,large,leading,left,like,limit,list,load,local,lock,mapping,
		materialized,move,natural,no,not,notnull,null,of,offset,on,only,operator,or,order,others,outer,
		owner,partition,password,placing,policy,primary,read,refresh,rename,replica,reset,restrict,restart,
		returning,returns,right,rule,savepoint,security,select,sequence,set,share,similar,some,stable,
		symmetric,table,tablesample,then,to,trailing,transaction,trigger,truncate,union,unique,update,user,
		using,validate,value,variadic,verify,view,volatile,when,where,window,with,write
	`)
	notAColumn = words("constraint,primary,unique,check,foreign,exclude,index,key,period,partition,like")
	// SQL 形态：字面量至少要像一段 SQL，才拿去比对（纯日志文案、路径不算）。
	sqlShapes = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\bSELECT\b.*\bFROM\b`),
		regexp.MustCompile(`(?i)\bINSERT\s+INTO\b`),
		regexp.MustCompile(`(?i)\bUPDATE\s+[\w".]+\s+SET\b`),
		regexp.MustCompile(`(?i)\bDELETE\s+FROM\b`),
		regexp.MustCompile(`(?i)\bALTER\s+TABLE\b`),
		regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?(?:INDEX|TABLE)\b`),
		regexp.MustCompile(`(?i)^\s*from\b`), // 拼接用的 FROM 子句片段
		regexp.MustCompile(`(?i)^\s*(?:(?:left|right|inner|cross|full)\s+)?join\b`),
	}
	// 解析前的粗筛：源码小写后一个都不含、也没有 //go:embed ⇒ 不可能有 SQL 字面量，省掉语法树解析。
	roughMarks = [][]byte{
		[]byte("select "), []byte("select\n"), []byte("select\t"), []byte("select*"),
		[]byte("insert "), []byte("update "), []byte("delete from"), []byte(" join "),
		[]byte("alter table"), []byte("create table"), []byte("create index"), []byte("create unique index"),
	}
	embedMark = []byte("//go:embed")
)

var (
	reLineCmt   = regexp.MustCompile(`--[^\n]*`)
	reBlockCmt  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reDollarTag = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*\$|^\$\$`)
	// ALTER TABLE t：取到表名为止。一条语句里可以连做几个动作（527：先 ALTER COLUMN、再连加六列），
	// 后面的 ADD 逐个认——表头若要求「表名后紧跟 ADD」，这种语句整条都读不到。
	reAlterHead = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w".]+)`)
	reAddAny    = regexp.MustCompile(`(?i)\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)
	reCreateTbl = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+|TEMPORARY\s+|TEMP\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)
	rePartition = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+|TEMPORARY\s+|TEMP\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)\s+PARTITION\s+OF\s+([\w".]+)`)
	// 改名是两种形态，两条式子：RENAME TO new_table；RENAME [COLUMN] a TO b（可省 COLUMN）。
	// 列改名那条对「RENAME TO x」天然不匹配（它要求 RENAME 后先有一个列名、再有一个 TO）；
	// RE2 没有 (?!…) 负向先行，别往里加。
	reRenameTbl  = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w".]+)\s+RENAME\s+TO\s+([\w".]+)`)
	reRenameCol  = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w".]+)\s+RENAME\s+(?:COLUMN\s+)?([\w".]+)\s+TO\s+([\w".]+)`)
	reCreateView = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)
	reChannelEnt = regexp.MustCompile(`\$ROOT_DIR/(sql/migrations/startup/[A-Za-z0-9_.\-]+\.sql)`)
)

func stripComments(s string) string {
	return reBlockCmt.ReplaceAllString(reLineCmt.ReplaceAllString(s, ""), "")
}

func skipped(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if SkipDir[seg] {
			return true
		}
	}
	return false
}

func isProductionGo(rel string) bool {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || skipped(rel) {
		return false
	}
	for _, p := range NonProductionPrefixes {
		if strings.HasPrefix(rel, p) {
			return false
		}
	}
	return true
}

// isSQLSource：会定义对象的 SQL 件（down 件不算定义）。
func isSQLSource(rel string) bool {
	return strings.HasSuffix(rel, ".sql") && !strings.HasSuffix(rel, ".down.sql") && !skipped(rel)
}

func inRecognizedDir(rel string) bool {
	for _, d := range RecognizedDirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// normalizeName 取限定名的最后一段，去引号、转小写（public.api_keys ⇒ api_keys）。
func normalizeName(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.ReplaceAll(s, `"`, ""))
}

func put(out map[Object]string, o Object, src string) {
	if o.Table == "" || strings.ContainsAny(o.Table+o.Column, "()\"' ") {
		return
	}
	if _, ok := out[o]; !ok {
		out[o] = src
	}
}

// sqlDefs 是一段 SQL 里定义的对象，连同改名／分区关系（合并时要据此派生）。
type sqlDefs struct {
	objects    map[Object]string // 表／列 ⇒ 来源
	renamedTo  map[string]string // 新表名 ⇒ 旧表名
	partitions map[string]string // 子分区 ⇒ 母表
	views      map[string]bool   // 这段 SQL 建成的视图名
	tables     map[string]bool   // 这段 SQL 的 DDL 提到的全部表名（建表、改表、改名的对象）
}

func (d *sqlDefs) add(o Object, src string) { put(d.objects, o, src) }

// see 记下一个被 DDL 提到的表名：它是不是「仓里定义过的表」关系到别名能不能绑定，
// 而「只在散文件里被 ALTER 过的表」也算定义过——否则它的别名永远绑不上。
func (d *sqlDefs) see(t string) {
	if t != "" {
		if d.tables == nil {
			d.tables = map[string]bool{}
		}
		d.tables[t] = true
	}
}

func parseSQL(name, sql string) *sqlDefs {
	d := &sqlDefs{objects: map[Object]string{}}
	sql = stripComments(sql)
	for _, m := range reRenameTbl.FindAllStringSubmatch(sql, -1) {
		old, neu := normalizeName(m[1]), normalizeName(m[2])
		d.see(old)
		d.see(neu)
		if d.renamedTo == nil {
			d.renamedTo = map[string]string{}
		}
		if _, ok := d.renamedTo[neu]; !ok {
			d.renamedTo[neu] = old
		}
	}
	for _, m := range reRenameCol.FindAllStringSubmatch(sql, -1) {
		table, from, to := normalizeName(m[1]), normalizeName(m[2]), normalizeName(m[3])
		d.see(table)
		if strings.EqualFold(from, "to") { // 保险：表改名被这条式子误配时不当成列
			continue
		}
		d.add(Object{table, to}, name) // RENAME [COLUMN] from TO to：新列名算已创建
	}
	for _, m := range rePartition.FindAllStringSubmatch(sql, -1) {
		child, parent := normalizeName(m[1]), normalizeName(m[2])
		d.see(child)
		d.see(parent)
		if d.partitions == nil {
			d.partitions = map[string]string{}
		}
		if _, ok := d.partitions[child]; !ok {
			d.partitions[child] = parent
		}
	}
	for _, m := range reCreateView.FindAllStringSubmatch(sql, -1) {
		view := normalizeName(m[1])
		d.see(view)
		d.add(Object{Table: view}, name) // 视图名本身算已投递：代码 FROM 它不算引用未投递的表
		if d.views == nil {
			d.views = map[string]bool{}
		}
		d.views[view] = true
	}
	// ALTER TABLE …：一条语句可以连着加多列（852 一条语句加三列），逐个 ADD 认。
	pos := 0
	for pos < len(sql) {
		loc := reAlterHead.FindStringIndex(sql[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[0]
		end := alterExtent(sql, start)
		table := normalizeName(reAlterHead.FindStringSubmatch(sql[start:end])[1])
		d.see(table)
		for _, m := range reAddAny.FindAllStringSubmatch(blankStrings(sql[start:end]), -1) {
			if col := normalizeName(m[1]); !notAColumn[col] {
				d.add(Object{table, col}, name)
			}
		}
		pos = end
	}
	for _, loc := range reCreateTbl.FindAllStringSubmatchIndex(sql, -1) {
		table := normalizeName(sql[loc[2]:loc[3]])
		d.see(table)
		d.add(Object{Table: table}, name)
		rest := sql[loc[1]:]
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\n' || rest[i] == '\t' || rest[i] == '\r') {
			i++
		}
		if i >= len(rest) || rest[i] != '(' {
			continue // CREATE TABLE t AS SELECT …／PARTITION OF …：只记表
		}
		if end := matchParen(rest, i); end > i {
			for _, c := range topLevelColumns(rest[i+1 : end]) {
				d.add(Object{table, c}, name)
			}
		}
	}
	return d
}

// alterExtent 返回从 start 起这条 ALTER 语句的结束下标。正常位置上到分号为止（跳过字符串；
// 撞上美元引号即止——那多半是外层 DO 块的收尾）。若 start 紧跟在一个引号之后，说明它落在
// DO 块的 EXECUTE 字符串里：那个引号是闭合引号，到下一个引号为止——把它当开引号会一路
// 吞到文件尾，后面的 ALTER 全漏。
func alterExtent(s string, start int) int {
	boundary := byte(0)
	k := start - 1
	for k >= 0 && isSpace(s[k]) {
		k--
	}
	if k >= 0 && (s[k] == '\'' || s[k] == '$') {
		boundary = s[k]
	}
	for i := start; i < len(s); {
		c := s[i]
		if boundary != 0 {
			if c == boundary {
				return i
			}
			i++
			continue
		}
		switch c {
		case '\'':
			i = skipQuoted(s, i)
		case ';':
			return i + 1
		case '$':
			if dollarQuotedLen(s[i:]) > 0 {
				return i
			}
			i++
		default:
			i++
		}
	}
	return len(s)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// blankStrings 把 s 里的单引号字符串内容抹成空格（引号保留）：字面量里的 ADD 不是 DDL。
func blankStrings(s string) string {
	b := []byte(s)
	for i := 0; i < len(s); {
		if s[i] != '\'' {
			i++
			continue
		}
		j := skipQuoted(s, i)
		for k := i + 1; k < j && k < len(b); k++ {
			if b[k] != '\'' && b[k] != '\n' {
				b[k] = ' '
			}
		}
		i = j
	}
	return string(b)
}

// skipQuoted 跳过 s[i] 处起的单引号字符串（” 是转义），返回闭合引号之后的下标。
func skipQuoted(s string, i int) int {
	i++
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(s)
}

// dollarQuotedLen 返回 s 开头的美元引号段（$$…$$、$tag$…$tag$）的长度；不是则返回 0。
func dollarQuotedLen(s string) int {
	loc := reDollarTag.FindStringIndex(s)
	if loc == nil {
		return 0
	}
	tag := s[:loc[1]]
	if j := strings.Index(s[loc[1]:], tag); j >= 0 {
		return loc[1] + j + len(tag)
	}
	return len(s)
}

// matchParen 返回 s[open]（必须是 '('）配对的右括号下标；找不到返回 -1。单引号字符串里的括号不计。
func matchParen(s string, open int) int {
	depth, inStr := 0, false
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\'' {
				inStr = false
			}
		case c == '\'':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// topLevelColumns 取建表语句体里每个顶层段落的首个标识符，跳过表级约束段。
func topLevelColumns(body string) []string {
	var out []string
	depth, inStr, start := 0, false, 0
	part := func(s string) {
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return
		}
		col := normalizeName(fields[0])
		if col == "" || notAColumn[col] {
			return
		}
		out = append(out, col)
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case inStr:
			if c == '\'' {
				inStr = false
			}
		case c == '\'':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			part(body[start:i])
			start = i + 1
		}
	}
	part(body[start:])
	return out
}

// mergeDelivered 把各来源的定义合成「已投递对象」全集：表改名 ⇒ 新表名继承旧表名下的全部列，
// 分区 ⇒ 子表继承母表的列（都只增不删，与来源顺序无关）。
func mergeDelivered(parts []*sqlDefs) map[Object]string {
	out := map[Object]string{}
	renames, partitions := map[string]string{}, map[string]string{}
	for _, d := range parts {
		for o, src := range d.objects {
			put(out, o, src)
		}
		for neu, old := range d.renamedTo {
			if renames[neu] == "" {
				renames[neu] = old
			}
		}
		for child, parent := range d.partitions {
			if partitions[child] == "" {
				partitions[child] = parent
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for neu, old := range renames {
			if neu != old && inherit(out, old, neu) {
				changed = true
			}
		}
		for child, parent := range partitions {
			if child != parent && inherit(out, parent, child) {
				changed = true
			}
		}
	}
	return out
}

// inherit 把 from 名下的全部列记到 to 名下（表本身也算——改名后的表以新名字存在）；返回是否有新增。
func inherit(out map[Object]string, from, to string) bool {
	var add []Object
	srcs := map[Object]string{}
	for o, src := range out {
		if o.Table != from {
			continue
		}
		n := Object{to, o.Column}
		if _, ok := out[n]; !ok {
			if _, seen := srcs[n]; !seen {
				add = append(add, n)
				srcs[n] = src
			}
		}
	}
	for _, o := range add {
		put(out, o, srcs[o])
	}
	return len(add) > 0
}

// listFiles 列出 root 下的文件（相对 root，正斜杠；跳过 SkipDir）：root 本身是一个 git 工作树的
// 顶层时问 git（含未跟踪、未忽略的）；否则遍历目录——临时目录若恰好落在别的仓里，问 git 会拿回
// 那个仓的清单。
func listFiles(root string) ([]string, error) {
	if gitTop, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output(); err == nil && samePath(strings.TrimSpace(string(gitTop)), root) {
		out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files 执行失败：%w", err)
		}
		var rels []string
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" && !skipped(p) {
				rels = append(rels, p)
			}
		}
		return rels, nil
	}
	var rels []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if p != root && SkipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, walkErr
	}
	return rels, nil
}

// samePath 比两个路径是否指向同一处：解析符号链接之后比（macOS 的 /var 是 /private/var 的链接）。
func samePath(a, b string) bool {
	ea, errA := filepath.EvalSymlinks(filepath.Clean(a))
	eb, errB := filepath.EvalSymlinks(filepath.Clean(b))
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ea == eb
}

func looksSQL(s string) bool {
	for _, re := range sqlShapes {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

type sqlLiteral struct {
	text string
	line int
}

// goInfo 是一个生产 Go 文件的判据面读数。
type goInfo struct {
	lits   []sqlLiteral // SQL 形态的字符串字面量（按语法树取：注释里、拼接前的半截都不算）
	embeds []string     // //go:embed 进来的 .sql（相对仓根）
}

// analyzeGoFile 解析一个生产 Go 文件。返回 nil 表示粗筛没过（既没有 SQL 也没有嵌入）。
func analyzeGoFile(root, rel string) (*goInfo, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	lower := bytes.ToLower(data)
	rough := false
	for _, m := range roughMarks {
		if bytes.Contains(lower, m) {
			rough = true
			break
		}
	}
	hasEmbed := bytes.Contains(data, embedMark)
	if !rough && !hasEmbed {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, data, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", rel, err)
	}
	info := &goInfo{}
	if rough {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !looksSQL(s) {
				return true
			}
			info.lits = append(info.lits, sqlLiteral{text: s, line: fset.Position(lit.Pos()).Line})
			return true
		})
	}
	if hasEmbed {
		dir := pathpkg.Dir(rel)
		seen := map[string]bool{}
		for _, e := range info.embeds {
			seen[e] = true
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				args, ok := embedArgs(c.Text)
				if !ok {
					continue
				}
				for _, a := range args {
					for _, p := range expandEmbed(root, dir, a) {
						if !seen[p] {
							seen[p] = true
							info.embeds = append(info.embeds, p)
						}
					}
				}
			}
		}
	}
	return info, nil
}

// embedArgs 解析一条 //go:embed 指令的参数（去掉 all: 前缀、去引号）；不是嵌入指令时 ok=false。
func embedArgs(text string) ([]string, bool) {
	rest, ok := strings.CutPrefix(text, "//go:embed")
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return nil, false
	}
	var out []string
	for _, f := range strings.Fields(rest) {
		f = strings.TrimPrefix(f, "all:")
		if len(f) > 1 && (f[0] == '"' || f[0] == '`') {
			if u, err := strconv.Unquote(f); err == nil {
				f = u
			}
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return out, true
}

// expandEmbed 把一个嵌入模式解析成它覆盖的 .sql 文件（相对仓根）：字面路径、通配、目录都认。
func expandEmbed(root, dir, pattern string) []string {
	abs := filepath.Join(root, filepath.FromSlash(dir), filepath.FromSlash(pattern))
	toRel := func(p string) (string, bool) {
		if !strings.HasSuffix(p, ".sql") {
			return "", false
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || skipped(filepath.ToSlash(rel)) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	var out []string
	if strings.ContainsAny(pattern, `*?[`) {
		ms, _ := filepath.Glob(abs)
		for _, m := range ms {
			if rel, ok := toRel(m); ok {
				out = append(out, rel)
			}
		}
		return out
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		if rel, ok := toRel(abs); ok {
			out = append(out, rel)
		}
		return out
	}
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != abs && SkipDir[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			if rel, ok := toRel(p); ok {
				out = append(out, rel)
			}
		}
		return nil
	})
	return out
}

func readRel(root, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
}

// ---------- SQL 词法 ----------

// tok 是 SQL 文本里的一个词法单元。
type tok struct {
	id string // 标识符（已归一：小写、去引号）；非标识符为空
	ch byte   // 非标识符单元的字符（. , ( ) ; = : 等）；标识符为 0
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentByte(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// tokenize 把一段 SQL 切成词法单元：字符串、美元引号里的内容不成词；数字、空白不成词；
// `::` 记成 ':'（后面跟的是类型名）。
func tokenize(s string) []tok {
	out := make([]tok, 0, len(s)/8+8)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			i = skipQuoted(s, i)
		case c == '$':
			if n := dollarQuotedLen(s[i:]); n > 0 {
				i += n
			} else {
				out = append(out, tok{ch: '$'})
				i++
			}
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			out = append(out, tok{id: strings.ToLower(s[i+1 : j])})
			i = j + 1
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			out = append(out, tok{id: strings.ToLower(s[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(s) && (isIdentByte(s[j]) || s[j] == '.') {
				j++
			}
			i = j
		case c == ':' && i+1 < len(s) && s[i+1] == ':':
			out = append(out, tok{ch: ':'})
			i += 2
		default:
			out = append(out, tok{ch: c})
			i++
		}
	}
	return out
}

// closeParen 返回 toks[open]（必须是 '('）配对的 ')' 下标；不配对时返回 len(toks)。
func closeParen(toks []tok, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		if toks[i].ch == '(' {
			depth++
		} else if toks[i].ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(toks)
}

// subjectAfter 从 j 起读「[schema.]表 [AS] [别名]」，返回它之后的位置与解析到的表名。
func subjectAfter(toks []tok, j int, resolve func(string) string) (int, string) {
	n := len(toks)
	if j >= n || toks[j].id == "" {
		return j, ""
	}
	last := toks[j].id
	j++
	for j+1 < n && toks[j].ch == '.' && toks[j+1].id != "" {
		last = toks[j+1].id
		j += 2
	}
	if j < n && toks[j].id == "as" {
		j++
	}
	if j < n && toks[j].id != "" && !sqlKeywords[toks[j].id] {
		j++ // 裸别名
	}
	return j, resolve(last)
}

// lastInsertTable 找 toks[before] 之前最近一次 INSERT INTO 的表（给 DO UPDATE SET 用）。
func lastInsertTable(toks []tok, before int, resolve func(string) string) string {
	for i := before - 2; i >= 0; i-- {
		if toks[i].id == "insert" && toks[i+1].id == "into" {
			_, t := subjectAfter(toks, i+2, resolve)
			return t
		}
	}
	return ""
}

// undeliveredIndex 是未投递对象的检索面，外加「仓里一切定义过的表名」（绑定别名用）。
type undeliveredIndex struct {
	tables map[string]bool            // 未投递的表名
	cols   map[string]map[string]bool // 未投递的列名 ⇒ 拥有它的表
	known  map[string]bool            // 仓里一切 SQL 定义过的表名
}

// referencesIn 找出一段 SQL 文本对未投递对象的绑定引用（去重，按对象名排序）。
func (u *undeliveredIndex) referencesIn(text string) []Object {
	toks := tokenize(stripComments(text))
	n := len(toks)
	maybe := false
	for i := range toks {
		if u.tables[toks[i].id] || u.cols[toks[i].id] != nil {
			maybe = true
			break
		}
	}
	if !maybe {
		return nil
	}

	// 本段声明的 CTE 名：同名表被它遮蔽。
	cte := map[string]bool{}
	for i := 0; i < n; i++ {
		if toks[i].id != "with" || (i > 0 && toks[i-1].ch == '.') {
			continue
		}
		j := i + 1
		if j < n && toks[j].id == "recursive" {
			j++
		}
		for {
			if j >= n || toks[j].id == "" || sqlKeywords[toks[j].id] {
				break
			}
			cte[toks[j].id] = true
			j++
			if j < n && toks[j].ch == '(' {
				j = closeParen(toks, j) + 1
			}
			if j >= n || toks[j].id != "as" {
				break
			}
			j++
			if j < n && toks[j].id == "not" {
				j++
			}
			if j < n && toks[j].id == "materialized" {
				j++
			}
			if j >= n || toks[j].ch != '(' {
				break
			}
			j = closeParen(toks, j) + 1
			if j < n && toks[j].ch == ',' {
				j++
				continue
			}
			break
		}
	}

	// bareTablePosition：toks[i] 是限定名的最后一段，且这个名字出现在「表的位置」——
	// FROM／JOIN／INTO／UPDATE 等之后、FROM 列表的逗号之后、字面量开头（FROM 子句片段）。
	// 只看位置、不看这张表认不认识：别名绑定要先于「认表」成立，否则「只在散文件里被 ALTER 过
	// 的表」永远绑不上别名——那正是 820fcc4f0 的形态。
	bareTablePosition := func(i int) bool {
		if toks[i].id == "" || sqlKeywords[toks[i].id] {
			return false // 关键字不是表名：字面量开头的 SELECT／UPDATE 也在表位上，但它是子句词
		}
		start := i
		for start >= 2 && toks[start-1].ch == '.' && toks[start-2].id != "" {
			start -= 2
		}
		if start >= 1 && toks[start-1].ch == '.' {
			return false // 4 段以上
		}
		if start == 0 {
			// 字面量开头的裸表名是 FROM 子句片段：后面只能跟子句词、分号，或它的别名。
			if i+1 >= n || toks[i+1].ch == ';' {
				return true
			}
			next := toks[i+1]
			return (next.id != "" && !sqlKeywords[next.id]) || afterTableClauses[next.id]
		}
		p := toks[start-1]
		if p.id != "" {
			return tableIntroducers[p.id]
		}
		if p.ch != ',' {
			return false
		}
		// 逗号后的裸标识符：回看这段列表由哪个词起头（括号组整段跳过）。
		depth := 0
		for j := start - 2; j >= 0; j-- {
			if toks[j].id == "" {
				switch toks[j].ch {
				case ')':
					depth++
				case '(':
					if depth == 0 {
						return false
					}
					depth--
				}
				continue
			}
			if depth > 0 {
				continue
			}
			if tableIntroducers[toks[j].id] {
				return true
			}
			if sqlKeywords[toks[j].id] && toks[j].id != "as" {
				return false
			}
		}
		return false
	}

	// 别名声明：<表> [AS] <非关键字标识符>，表出现在表的位置。
	alias := map[string]string{}
	aliasDecl := make([]bool, n)
	for i := 0; i+1 < n; i++ {
		if !bareTablePosition(i) || cte[toks[i].id] {
			continue
		}
		j := i + 1
		if toks[j].id == "as" {
			j++
		}
		if j >= n || toks[j].id == "" || sqlKeywords[toks[j].id] {
			continue
		}
		if alias[toks[j].id] == "" {
			alias[toks[j].id] = toks[i].id
		}
		aliasDecl[j] = true
	}
	resolve := func(q string) string {
		if cte[q] {
			return ""
		}
		if t, ok := alias[q]; ok {
			return t
		}
		if u.known[q] {
			return q
		}
		return ""
	}

	// atTablePosition：toks[i] 是不是「以表的身份」出现（别名声明处不算）。
	atTablePosition := func(i int) bool {
		if aliasDecl[i] {
			return false
		}
		// T.C／schema.T.C：作限定符（4 段以上不认）。
		if i+2 < n && toks[i+1].ch == '.' && toks[i+2].id != "" && !(i+3 < n && toks[i+3].ch == '.') {
			return true
		}
		return bareTablePosition(i)
	}

	seen := map[Object]bool{}
	var refs []Object
	add := func(t, c string) {
		if t == "" {
			return
		}
		o := Object{t, c}
		if !seen[o] {
			seen[o] = true
			refs = append(refs, o)
		}
	}
	hasCol := func(c, t string) bool { return u.cols[c] != nil && u.cols[c][t] }

	// 表提及（经别名解析、去重）：未投递的表直接算引用；全部提及用于「只提到一张表」的绑定。
	// 不认识的表在表位上也占一个提及——有它在，裸列就不能只绑给另一张表。
	var mentions []string
	inList := map[string]bool{}
	for i := 0; i < n; i++ {
		id := toks[i].id
		if id == "" || cte[id] || !atTablePosition(i) {
			continue
		}
		t := resolve(id)
		bare := bareTablePosition(i)
		if t == "" && !bare {
			continue // 只是作限定符的陌生名字（EXCLUDED.x、pg_catalog.x）⇒ 不算表提及
		}
		if t == "" {
			t = id
		}
		if inList[t] {
			continue
		}
		inList[t] = true
		mentions = append(mentions, t)
		if u.tables[t] {
			add(t, "")
		}
	}

	// a. 限定名 T.C／schema.T.C／别名.C
	for i := 2; i < n; i++ {
		id := toks[i].id
		if id == "" || u.cols[id] == nil || toks[i-1].ch != '.' || toks[i-2].id == "" {
			continue
		}
		if t := resolve(toks[i-2].id); hasCol(id, t) {
			add(t, id)
		}
	}

	// b. INSERT INTO T (c1, c2, …)
	for i := 0; i+1 < n; i++ {
		if toks[i].id != "insert" || toks[i+1].id != "into" {
			continue
		}
		j, t := subjectAfter(toks, i+2, resolve)
		if j >= n || toks[j].ch != '(' {
			continue
		}
		cl := closeParen(toks, j)
		for k := j + 1; k < cl; k++ {
			if c := toks[k].id; c != "" && hasCol(c, t) {
				add(t, c)
			}
		}
	}

	// c. UPDATE T SET c = …（含 ON CONFLICT … DO UPDATE SET）
	for i := 0; i < n; i++ {
		if toks[i].id != "update" {
			continue
		}
		k, t := i+1, ""
		if i > 0 && toks[i-1].id == "do" {
			t = lastInsertTable(toks, i, resolve)
		} else {
			if k < n && toks[k].id == "only" {
				k++
			}
			k, t = subjectAfter(toks, k, resolve)
		}
		if k >= n || toks[k].id != "set" {
			continue
		}
		for m := k + 1; m < n; {
			tk := toks[m]
			if tk.ch == ';' || tk.id == "from" || tk.id == "where" || tk.id == "returning" {
				break
			}
			if tk.ch == '(' {
				cl := closeParen(toks, m)
				if cl+1 < n && toks[cl+1].ch == '=' { // SET (a, b) = …
					for q := m + 1; q < cl; q++ {
						if c := toks[q].id; c != "" && hasCol(c, t) {
							add(t, c)
						}
					}
				}
				m = cl + 1
				continue
			}
			if tk.id != "" && m+1 < n && toks[m+1].ch == '=' && (m == k+1 || (toks[m-1].ch != '.' && toks[m-1].id != "as")) {
				if hasCol(tk.id, t) {
					add(t, tk.id)
				}
			}
			m++
		}
	}

	// d. 整段只提到一张表 ⇒ 裸列只能属于它
	if len(mentions) == 1 {
		t := mentions[0]
		for i := 0; i < n; i++ {
			id := toks[i].id
			if id == "" || !hasCol(id, t) || aliasDecl[i] || sqlKeywords[id] {
				continue
			}
			if i == 0 {
				continue
			}
			prev := toks[i-1]
			if prev.ch == '.' || prev.ch == ':' || prev.id == "as" || (i+1 < n && toks[i+1].ch == '(') {
				continue // 限定列／类型名／输出别名／函数名
			}
			add(t, id)
		}
	}

	sort.Slice(refs, func(a, b int) bool { return refs[a].String() < refs[b].String() })
	return refs
}

// Check 在仓根 root 上做一次完整判定。
func Check(root string) (*Result, error) {
	// 相对路径先落成绝对路径：下面要拿它跟 git 报的顶层目录比。
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = abs
	res := &Result{Delivered: map[Object]string{}, Undelivered: map[Object][]string{}}
	tracked, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	sort.Strings(tracked)

	// ① 两条通道点名的迁移。
	named := map[string]bool{}
	script, err := readRel(root, DeployChannelScript)
	if err != nil {
		return nil, fmt.Errorf("读部署通道清单失败：%w", err)
	}
	for _, m := range reChannelEnt.FindAllStringSubmatch(string(script), -1) {
		if isSQLSource(m[1]) {
			named[m[1]] = true
		}
	}
	manifest, err := readRel(root, InstallerManifest)
	if err != nil {
		return nil, fmt.Errorf("读安装器清单失败：%w", err)
	}
	for _, line := range strings.Split(string(manifest), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(parts[0])); err != nil {
			continue
		}
		if name := strings.TrimSpace(parts[1]); isSQLSource(name) {
			named[InstallerEmbedDir+"/"+name] = true
		}
	}
	channelN := len(named)

	// ③ 认可投递位目录里的 SQL。
	for _, rel := range tracked {
		if isSQLSource(rel) && inRecognizedDir(rel) {
			named[rel] = true
		}
	}
	res.RecognizedFiles = len(named) - channelN
	namedFiles := make([]string, 0, len(named))
	for rel := range named {
		namedFiles = append(namedFiles, rel)
	}
	sort.Strings(namedFiles)

	type source struct{ rel, sql string }
	var delivered []source
	for _, rel := range namedFiles {
		data, err := readRel(root, rel)
		if errors.Is(err, fs.ErrNotExist) {
			res.MissingFiles++ // 点名了、磁盘上却没有 ⇒ 属于别的门（embed 同步门）的判据面，这里如实计数
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读已投递迁移 %s 失败：%w", rel, err)
		}
		res.ChannelFiles++
		delivered = append(delivered, source{rel, string(data)})
	}

	// ④⑤ 生产 Go 文件：一次解析，取 SQL 字面量（代码自建 DDL）与 //go:embed 的 SQL。
	goFiles := map[string]*goInfo{}
	embeds := map[string]string{} // .sql 相对仓根 ⇒ 嵌入它的 Go 文件
	for _, rel := range tracked {
		if !isProductionGo(rel) {
			continue
		}
		info, err := analyzeGoFile(root, rel)
		if err != nil {
			res.ParseErrors = append(res.ParseErrors, err.Error())
			continue
		}
		if info == nil {
			continue
		}
		res.GoFiles++
		goFiles[rel] = info
		for _, e := range info.embeds {
			if embeds[e] == "" {
				embeds[e] = rel
			}
		}
	}
	embedFiles := make([]string, 0, len(embeds))
	for e := range embeds {
		embedFiles = append(embedFiles, e)
	}
	sort.Strings(embedFiles)
	for _, e := range embedFiles {
		data, err := readRel(root, e)
		if errors.Is(err, fs.ErrNotExist) {
			res.MissingFiles++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读嵌入的 SQL %s 失败：%w", e, err)
		}
		res.EmbedFiles++
		delivered = append(delivered, source{"embed:" + embeds[e], string(data)})
	}

	// 已投递对象全集 ＋ 仓里一切定义过的表名 ＋ 已投递来源建成的视图名。
	known := map[string]bool{}
	views := map[string]bool{}
	note := func(d *sqlDefs) {
		for t := range d.tables {
			known[t] = true
		}
	}
	var parts []*sqlDefs
	for _, s := range delivered {
		d := parseSQL(s.rel, s.sql)
		parts = append(parts, d)
		note(d)
		for v := range d.views {
			views[v] = true
		}
	}
	goRels := make([]string, 0, len(goFiles))
	for rel := range goFiles {
		goRels = append(goRels, rel)
	}
	sort.Strings(goRels)
	for _, rel := range goRels {
		for _, l := range goFiles[rel].lits {
			d := parseSQL("code:"+rel, l.text)
			if len(d.objects) == 0 && len(d.renamedTo) == 0 && len(d.partitions) == 0 && len(d.views) == 0 {
				continue
			}
			parts = append(parts, d)
			note(d)
			for v := range d.views {
				views[v] = true
			}
		}
	}
	res.Delivered = mergeDelivered(parts)

	// 候选来源：其余所有 .sql。
	candidates := map[Object][]string{}
	for _, rel := range tracked {
		if !isSQLSource(rel) || named[rel] || embeds[rel] != "" {
			continue
		}
		data, err := readRel(root, rel)
		if err != nil {
			continue // 读不到（符号链接断了之类）⇒ 不在判据面里
		}
		res.CandidateFiles++
		d := parseSQL(rel, string(data))
		note(d)
		for o := range d.objects {
			candidates[o] = append(candidates[o], rel)
		}
	}
	for o, srcs := range candidates {
		// 视图的列不判：它的列来自 SELECT 列表，静态上说不清哪个来源「创建」了它
		//（基线里的 model_offers 是 cmb 之上的视图，而散文件 292 对它有一句 ALTER TABLE）。
		if _, ok := res.Delivered[o]; !ok && !(o.Column != "" && views[o.Table]) {
			sort.Strings(srcs)
			res.Undelivered[o] = srcs
		}
	}

	// 引用。
	idx := &undeliveredIndex{tables: map[string]bool{}, cols: map[string]map[string]bool{}, known: known}
	for o := range res.Undelivered {
		if o.Column == "" {
			idx.tables[o.Table] = true
			continue
		}
		if idx.cols[o.Column] == nil {
			idx.cols[o.Column] = map[string]bool{}
		}
		idx.cols[o.Column][o.Table] = true
	}
	for _, rel := range goRels {
		for _, l := range goFiles[rel].lits {
			for _, o := range idx.referencesIn(l.text) {
				r := Reference{File: rel, Line: l.line, Object: o, Source: res.Undelivered[o][0]}
				if _, ok := knownEntry(rel, o); ok {
					res.Known = append(res.Known, r)
				} else {
					res.References = append(res.References, r)
				}
			}
		}
	}
	sort.Slice(res.Known, func(i, j int) bool { return refLess(res.Known[i], res.Known[j]) })
	sort.Slice(res.References, func(i, j int) bool { return refLess(res.References[i], res.References[j]) })
	res.Stale = staleKnown(res.Known)
	return res, nil
}

func refLess(a, b Reference) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Object.String() < b.Object.String()
}
