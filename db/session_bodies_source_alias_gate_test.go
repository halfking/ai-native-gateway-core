package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSessionBodiesSourceConsumersAliasRB 钉住 SessionBodiesSourceSQL 的
// 消费点必须自己补 `rb` 别名。
//
// R46 轮（2026-10-05）A 路审计实锤的 P0：e974a0d79 把 admin 的薄包装
// （旧 wrapper 返回 `"request_logs_bodies_with_current_month rb"`）删掉、
// 消费点改为直连 db.SessionBodiesSourceSQL() 时，13 个 admin 调用点
// 机械替换后丢了 ` rb` 别名 —— 而每处的 ON 子句都引用 `rb.request_id`。
// 后果：两臂（灰度开关开/关）SQL 都是
//
//	ERROR: missing FROM-clause entry for table "rb" (SQLSTATE 42P01)
//
// 即**默认关的生产现状就炸**：会话导出/对比、压缩统计、日志摘要、
// memora 会话消息、无主题会话消息、标题生成全 500；其中
// session_sanitize_matches.go 的 `_ = h.db.QueryRow(...)` 吞错路径静默
// 返回空串。全部离线门当时是绿的 —— 没有任何单测渲染这些 SQL。
//
// 为什么别名在调用方而不是 helper 里：e974a0d79 自己迁移的 4 个跨包
// 读方（domains/sessionforensics、domains/sessionsummary、bg）自带
// ` rb` 后缀写法 —— helper 内置别名会让那 4 处变成 `... rb rb`。
//
// 判据：每个非测试消费点，`SessionBodiesSourceSQL() +` 紧邻的字面量
// 必须以 `rb` token 开头（允许 `rb ON ...`、`rb⏎ON ...`、`rb /* 注释 */`）。
// 负控在 R46 审计文档：删掉任一处别名本门即红。
func TestSessionBodiesSourceConsumersAliasRB(t *testing.T) {
	roots := []string{"../admin", "../domains", "../bg", "."}
	// raw string 不能内嵌反引号，这里用 "..." 拼接表达「紧跟闭合反引号」。
	callRe := regexp.MustCompile("SessionBodiesSourceSQL\\(\\)\\s*\\+\\s*`")
	aliasRe := regexp.MustCompile(`^\s*rb\b`)

	var missing []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, loc := range callRe.FindAllStringIndex(string(src), -1) {
				rest := string(src[loc[1]:])
				end := strings.IndexByte(rest, '`')
				lit := rest
				if end >= 0 {
					lit = rest[:end]
				}
				if lit == "" || !aliasRe.MatchString(lit) {
					line := 1 + strings.Count(string(src[:loc[0]]), "\n")
					missing = append(missing, path+":"+itoaGate(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(missing) > 0 {
		t.Fatalf("SessionBodiesSourceSQL() 的消费点缺 `rb` 别名（ON 子句引用 rb.request_id，"+
			"缺别名即 42P01 missing FROM-clause entry，且离线测试全绿、只有真库/生产会炸）：%v\n"+
			"修法：在调用后的字面量开头补 ` rb`（如 `+dbpkg.SessionBodiesSourceSQL()+` rb ON rb.request_id = ...`）。"+
			"不要把别名内置进 helper —— 跨包 4 处自带 rb 后缀会变成 rb rb。",
			missing)
	}
}

func itoaGate(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
