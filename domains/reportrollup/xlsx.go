// xlsx.go —— 对账报表 Excel 导出的最小 OOXML writer。
//
// 仓库无 excelize 依赖（设计文档 §5 的"go.sum 间接依赖"说法与事实不符，
// R63 勘误），vendor 树锁死也不宜为此引入 2 万行第三方传递依赖；xlsx 本质
// 是 zip + 受控 XML，本文件自写生成端（读端测试见 xlsx_test.go 的
// round-trip 解析）。只覆盖本报表需要的能力：多 sheet、inline string /
// number 单元格、粗体表头、列宽——不实现的特性（公式/共享字符串/合并格）
// 恰好也是注入面收窄：所有文本走 xml.EscapeText，单元格一律字符串或数字，
// 不存在公式求值路径。
package reportrollup

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// cellKind 区分单元格值类型。
type cellKind int

const (
	cellString cellKind = iota
	cellNumber
)

// cellStyle 是 styles.xml 中 cellXfs 的索引（0=默认，1=粗体）。
type cellStyle int

const (
	styleDefault cellStyle = 0
	styleBold    cellStyle = 1
)

type cell struct {
	kind  cellKind
	style cellStyle
	str   string
	num   float64
}

// strCell 文本单元格。
func strCell(s string) cell { return cell{kind: cellString, str: s} }

// numCell 数值单元格（报表主体：计数/token/金额，可在 Excel 内再聚合）。
func numCell(n float64) cell { return cell{kind: cellNumber, num: n} }

// intCell 整数数值单元格。
func intCell(n int64) cell { return numCell(float64(n)) }

// boldStrCell 粗体文本（表头/分节标题）。
func boldStrCell(s string) cell { return cell{kind: cellString, style: styleBold, str: s} }

// sheetSheet 单个 worksheet：名称、行、列宽（Excel 字符宽单位）。
type sheetSheet struct {
	name   string
	rows   [][]cell
	widths []float64
}

// writeWorkbook 把 sheets 渲染为 xlsx 字节流写入 w。
//
// sheet 名会做 XML 转义；调用方需自行保证 ≤31 字符且不含 []:*?/\（Excel
// 侧约束，本包两个内置报表名均为中文短语，天然满足）。
func writeWorkbook(w io.Writer, sheets []sheetSheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("workbook needs at least one sheet")
	}
	zw := zip.NewWriter(w)
	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML(len(sheets)),
		"_rels/.rels":                rootRelsXML(),
		"xl/workbook.xml":            workbookXML(sheets),
		"xl/_rels/workbook.xml.rels": workbookRelsXML(len(sheets)),
		"xl/styles.xml":              stylesXML(),
	}
	for i, s := range sheets {
		files[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = worksheetXML(s)
	}
	// 固定顺序写入，保证输出字节稳定（测试快照友好）。
	order := []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml",
		"xl/_rels/workbook.xml.rels", "xl/styles.xml"}
	for i := range sheets {
		order = append(order, fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1))
	}
	for _, name := range order {
		f, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("zip create %s: %w", name, err)
		}
		if _, err := io.WriteString(f, files[name]); err != nil {
			return fmt.Errorf("zip write %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("close zip: %w", err)
	}
	return nil
}

// buildWorkbookBytes 便捷封装：渲染成字节切片。
// maxWorksheetRows 是 Excel 单 sheet 的硬上限（1,048,576 行）。手写
// writer 不做隐式截断——超限行号会使 Excel 判定文件损坏，必须在产出前
// 显式报错（R65；现实触发需单 sheet 超 1M 分组行，属防御性守卫）。
const maxWorksheetRows = 1048576

func buildWorkbookBytes(sheets []sheetSheet) ([]byte, error) {
	for _, s := range sheets {
		if len(s.rows) > maxWorksheetRows {
			return nil, fmt.Errorf("sheet %q has %d rows, exceeding Excel limit %d; narrow the range or aggregation granularity", s.name, len(s.rows), maxWorksheetRows)
		}
	}
	var buf bytes.Buffer
	if err := writeWorkbook(&buf, sheets); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func contentTypesXML(n int) string {
	overrides := ""
	for i := 1; i <= n; i++ {
		overrides += fmt.Sprintf(
			`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i)
	}
	return xmlDecl + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		overrides +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
		`</Types>`
}

func rootRelsXML() string {
	return xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
		`</Relationships>`
}

func workbookXML(sheets []sheetSheet) string {
	out := xmlDecl + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`
	for i, s := range sheets {
		out += fmt.Sprintf(`<sheet name=%q sheetId="%d" r:id="rId%d"/>`,
			xmlEscapeAttr(s.name), i+1, i+1)
	}
	out += `</sheets></workbook>`
	return out
}

func workbookRelsXML(n int) string {
	out := xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	for i := 1; i <= n; i++ {
		out += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
	}
	out += `<Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`
	out += `</Relationships>`
	return out
}

// worksheetXML 渲染单个 worksheet。行号/列号从 1 起；空 sheet 也要有一行
// （Excel 兼容）。列宽通过 <cols> 声明。
func worksheetXML(s sheetSheet) string {
	var b bytes.Buffer
	b.WriteString(xmlDecl)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	widthed := 0
	for _, w := range s.widths {
		if w > 0 {
			widthed++
		}
	}
	if widthed > 0 {
		b.WriteString(`<cols>`)
		for i, w := range s.widths {
			if w <= 0 {
				continue
			}
			b.WriteString(fmt.Sprintf(`<col min="%d" max="%d" width="%s" customWidth="1"/>`,
				i+1, i+1, strconv.FormatFloat(w, 'f', 1, 64)))
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>`)
	if len(s.rows) == 0 {
		s.rows = [][]cell{{}}
	}
	for r, row := range s.rows {
		b.WriteString(fmt.Sprintf(`<row r="%d">`, r+1))
		for c, cl := range row {
			ref := fmt.Sprintf("%s%d", colName(c), r+1)
			switch cl.kind {
			case cellNumber:
				if cl.style == styleBold {
					b.WriteString(fmt.Sprintf(`<c r=%q s="1"><v>%s</v></c>`, ref, strconv.FormatFloat(cl.num, 'f', -1, 64)))
				} else {
					b.WriteString(fmt.Sprintf(`<c r=%q><v>%s</v></c>`, ref, strconv.FormatFloat(cl.num, 'f', -1, 64)))
				}
			default:
				if cl.style == styleBold {
					b.WriteString(fmt.Sprintf(`<c r=%q s="1" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xmlEscapeText(cl.str)))
				} else {
					b.WriteString(fmt.Sprintf(`<c r=%q t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xmlEscapeText(cl.str)))
				}
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

// stylesXML 最小样式表：font0 默认 / font1 粗体，cellXfs 两项与
// styleDefault/styleBold 对齐。fill/_border 占位满足 schema 最小序列。
func stylesXML() string {
	return xmlDecl + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<fonts count="2">` +
		`<font><sz val="11"/><name val="Calibri"/></font>` +
		`<font><b/><sz val="11"/><name val="Calibri"/></font>` +
		`</fonts>` +
		`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
		`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
		`<cellXfs count="2">` +
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
		`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
		`</cellXfs>` +
		`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
		`</styleSheet>`
}

const xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// colName 把 0 起列号转成 Excel 字母列名（0→A，25→Z，26→AA）。
func colName(c int) string {
	name := ""
	for c >= 0 {
		name = string(rune('A'+c%26)) + name
		c = c/26 - 1
	}
	return name
}

func xmlEscapeText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// xmlEscapeAttr 属性值转义（EscapeText 不转义引号，双引号包裹的属性需
// 额外处理 `"`）。
func xmlEscapeAttr(s string) string {
	return strings.ReplaceAll(xmlEscapeText(s), `"`, "&quot;")
}
