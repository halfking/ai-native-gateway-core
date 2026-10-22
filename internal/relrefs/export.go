// export.go —— 对外导出的符号（★ 供 cmd/tools 的薄壳与 scripts/ursmcheck 的门共用）。
//
// ★★ 为什么要抽成包：★
//
//	若提取器只是 `package main` ⇒ ⇒ 门只能靠 `go run` 调它 ⇒ ⇒
//	① 慢（每次重新编译）② 口径可能漂移（工具改了、门没改 ⇒ 门还在测旧口径）
//	⇒ ⇒ 抽成可导入包 ⇒ ★ 门与工具【同源、同一次编译】⇒ ⇒ 口径不可能漂移。
package relrefs

// DDLRelations = 腿 A 的产出（仓内 DDL 建出的关系名集合）。
type DDLRelations = ddlRelations

// ProductScan = 腿 B 的产出（产品侧的关系引用）。
type ProductScan = productScan

// RelationRef = 一次关系引用。
type RelationRef = relationRef

// NewDDLRelations 造一个空的腿 A 集合。
func NewDDLRelations() *DDLRelations { return newDDLRelations() }

// NormalizeIdent 规整限定名（去引号、小写、点号连接）。
func NormalizeIdent(raw string) string { return normalizeIdent(raw) }

// BareName 取限定名的最后一段。
func BareName(qualified string) string { return bareName(qualified) }

// ScanDDLRepo 扫描 .sql 文件清单，产出腿 A 的关系集合。
func ScanDDLRepo(root string, sqlFiles []string) (*DDLRelations, error) {
	return scanDDLRepo(root, sqlFiles)
}

// ScanDDLFile 从一段 SQL 文本里抽关系名（★ 供「Go 侧 DDL」复用）。
func ScanDDLFile(d *DDLRelations, text string) { scanDDLFile(d, text) }

// ScanProductRepo 扫描 .go 文件清单，产出腿 B 的引用集合。
func ScanProductRepo(root string, goFiles []string, ddl *DDLRelations) (*ProductScan, error) {
	return scanProductRepo(root, goFiles, ddl)
}

// StrongNames = 强命中名字集合。
func (p *ProductScan) StrongNames() map[string]bool { return p.strongNames }

// WeakNames = 弱命中名字集合。
func (p *ProductScan) WeakNames() map[string]bool { return p.weakNames }

// PrefixSuspects = 拼接嫌疑名字集合。
func (p *ProductScan) PrefixSuspects() map[string]bool { return p.prefixSuspects }

// Refs = 全部关系引用。
func (p *ProductScan) Refs() []RelationRef { return p.refs }

// Files = 扫过的 .go 文件数。
func (p *ProductScan) Files() int { return p.files }

// StringLits = 取到的字符串字面量总数。
func (p *ProductScan) StringLits() int { return p.stringLits }

// SQLLits = 其中被判为 SQL 上下文的。
func (p *ProductScan) SQLLits() int { return p.sqlLits }

// GlobalCTECount = 全仓 CTE 名个数。
func (p *ProductScan) GlobalCTECount() int { return p.globalCTECount }

// FullNames = 腿 A 的全名集合。
func (d *DDLRelations) FullNames() map[string]bool { return d.full }

// BareNames = 腿 A 的裸名集合。
func (d *DDLRelations) BareNames() map[string]bool { return d.bare }

// Has 判断某个名字是否被仓内 DDL 建出。
func (d *DDLRelations) Has(name string) bool { return d.has(name) }

// SQLFiles = 扫过的 .sql 文件数。
func (d *DDLRelations) SQLFiles() int { return d.files }

// PerKind = 各 DDL 形态的计数。
func (d *DDLRelations) PerKind() map[string]int { return d.perKind }
