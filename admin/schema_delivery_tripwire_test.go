package admin

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/schemadelivery"
)

// TestAdminSQLDoesNotReferenceUndeliveredSchema —— 「生产 SQL 引用的表与列必须由投递来源创建」
// 在本包（admin）的绊线。判据本体在 internal/schemadelivery；全仓的那道门是
// scripts/ursmcheck/schema_delivery_gate_test.go。
//
// 为什么全仓有了门还要在这里钉一遍：部署闸门只跑「改动所及的包」。2026-11-26 那次缺口
// （820fcc4f0：admin/keys.go 写 api_keys.capabilities，而创建这一列的迁移不在任何投递通道）
// 落在本包与 domains/authentication 里，两处包测试全绿 ⇒ 闸门放行 ⇒ 差一步就是生产事故。
// 本绊线让「改了 admin 并部署」这条路上必跑一次。只报本包目录下的引用。
func TestAdminSQLDoesNotReferenceUndeliveredSchema(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("★ 定位仓根失败：%v", err)
	}
	res, err := schemadelivery.Check(root)
	if err != nil {
		t.Fatalf("★ 判定失败（量具面，不是结论）：%v", err)
	}
	if res.ChannelFiles < 300 || len(res.Delivered) < 1000 || res.GoFiles < 100 {
		t.Fatalf("★ 判据面可疑地小（通道文件 %d／已投递对象 %d／含 SQL 的生产 Go 文件 %d）\n"+
			"★ ⇒ 判据可能已失效 ⇒ ★ 别按本门结论改代码",
			res.ChannelFiles, len(res.Delivered), res.GoFiles)
	}
	seen := 0
	for _, r := range res.References {
		if !strings.HasPrefix(r.File, "admin/") {
			continue
		}
		seen++
		t.Errorf("★ %s:%d 的 SQL 引用了未投递对象 %s（仓里只有这些没被投递的文件定义它：%s）\n"+
			"★ ⇒ 部署与新装都不会创建它 ⇒ 运行时 42703／42P01\n"+
			"★ ⇒ 出路二选一：把定义它的迁移搬进投递通道并登记，或别在 SQL 里引用它",
			r.File, r.Line, r.Object, strings.Join(res.Undelivered[r.Object], "、"))
	}
	for _, s := range res.Stale {
		t.Errorf("★ 已知欠账台账（internal/schemadelivery/known_undelivered.go）里这一条不再被命中：%s\n"+
			"★ ⇒ 问题修好了就要删登记，防免死金牌", s)
	}
	t.Logf("★ 绊线：全仓未登记的引用 %d 处，其中本包 %d 处", len(res.References), seen)
}
