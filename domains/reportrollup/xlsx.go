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
	name      string
	rows      [][]cell
	widths    []float64
	dataCount int
	dataRow   func(int) []cell
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
	files := []struct{ name, content string }{
		{"[Content_Types].xml", contentTypesXML(len(sheets))},
		{"_rels/.rels", rootRelsXML()},
		{"xl/workbook.xml", workbookXML(sheets)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML(len(sheets))},
		{"xl/styles.xml", stylesXML()},
	}
	for _, file := range files {
		f, err := zw.Create(file.name)
		if err != nil {
			return fmt.Errorf("zip create %s: %w", file.name, err)
		}
		if _, err := io.WriteString(f, file.content); err != nil {
			return fmt.Errorf("zip write %s: %w", file.name, err)
		}
	}
	for i, sheet := range sheets {
		name := fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		f, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("zip create %s: %w", name, err)
		}
		if err := writeWorksheet(f, sheet); err != nil {
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
		if rows := len(s.rows) + s.dataCount; rows > maxWorksheetRows {
			return nil, fmt.Errorf("sheet %q has %d rows, exceeding Excel limit %d; narrow the range or aggregation granularity", s.name, rows, maxWorksheetRows)
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

// writeWorksheet streams bounded XML chunks. Row number 1 is also written
// for an empty sheet for Excel compatibility.
func writeWorksheet(w io.Writer, s sheetSheet) error {
	const chunkSize = 64 * 1024
	var b bytes.Buffer
	b.Grow(chunkSize)
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
	var rowNum [24]byte
	var number [64]byte
	cols := make([]string, 0, len(s.widths))
	for r := range max(1, len(s.rows)+s.dataCount) {
		var row []cell
		switch {
		case r < len(s.rows):
			row = s.rows[r]
		case r < len(s.rows)+s.dataCount:
			row = s.dataRow(r - len(s.rows))
		}
		for len(cols) < len(row) {
			cols = append(cols, colName(len(cols)))
		}
		digits := strconv.AppendInt(rowNum[:0], int64(r+1), 10)
		b.WriteString(`<row r="`)
		b.Write(digits)
		b.WriteString(`">`)
		for c, cl := range row {
			b.WriteString(`<c r="`)
			b.WriteString(cols[c])
			b.Write(digits)
			b.WriteByte('"')
			if cl.style == styleBold {
				b.WriteString(` s="1"`)
			}
			if cl.kind == cellNumber {
				b.WriteString(`><v>`)
				b.Write(strconv.AppendFloat(number[:0], cl.num, 'f', -1, 64))
				b.WriteString(`</v></c>`)
			} else {
				b.WriteString(` t="inlineStr"><is><t xml:space="preserve">`)
				_ = xml.EscapeText(&b, []byte(cl.str))
				b.WriteString(`</t></is></c>`)
			}
		}
		b.WriteString(`</row>`)
		if b.Len() >= chunkSize {
			if _, err := b.WriteTo(w); err != nil {
				return err
			}
		}
	}
	b.WriteString(`</sheetData></worksheet>`)
	_, err := b.WriteTo(w)
	return err
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
