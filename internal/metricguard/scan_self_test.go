package metricguard

// scan_self_test.go —— 扫描器自身的判别力自测。
//
// **为什么必须有这道门**：本守卫靠 filepath.Walk 直接读磁盘，**不受
// `go test -overlay` 影响**——用 overlay 做变异时它照样是绿的（本次实测）。
// 也就是说，守卫「有判别力」这件事本身没有任何证据，而一道恒绿的守卫正是本
// 项目反复吃亏的形状（门全绿≠门覆盖我）。
//
// 所以判别力必须变成**永久属性**：这里用一个合成的小仓库夹具喂给扫描器，
// 分别喂「有记录调用」与「无记录调用」两种形态，断言扫描器能区分。合成夹具
// 里的文件由本测试自己写出到 t.TempDir()，不碰仓库任何真实文件。

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 声明一个指标，形态与生产代码一致（promauto.NewCounter + Name 字段）。
const declBody = `package p

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var SomeCounter = promauto.NewCounter(prometheus.CounterOpts{
	Name: "some_total",
	Help: "h",
})
`

// 一个自洽的、不该被误报的最小生产文件。
const recordedBody = `package p

func bump() { SomeCounter.Inc() }
`

// 一个测试文件里的 Inc —— **不构成生产覆盖**，扫描器必须忽略它。
const testOnlyBody = `package p

func TestBump(t *testing.T) { SomeCounter.Inc() }
`

func TestScannerDetectsUnrecordedMetric(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantOut bool // 是否应被判为"从不记录"
		why     string
	}{
		{
			name:    "生产代码有记录调用 ⇒ 不算问题",
			files:   map[string]string{"decl.go": declBody, "use.go": recordedBody},
			wantOut: false,
			why:     "有 .Inc()，必须放行，否则守卫成了只会报错的噪音源",
		},
		{
			name:    "只有声明、无人记录 ⇒ 必须报出",
			files:   map[string]string{"decl.go": declBody},
			wantOut: true,
			why:     "这正是本守卫要抓的形态：/metrics 上恒 ABSENT 而面板看起来存在",
		},
		{
			name:    "只有测试在记录 ⇒ 仍须报出",
			files:   map[string]string{"decl.go": declBody, "use_test.go": testOnlyBody},
			wantOut: true,
			why: "测试里的 .Inc() 只让测试通过，不产生数据点；把它当覆盖会让守卫漏报" +
				"——这正是本守卫最容易被绕过的一种形态",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tc.files {
				writeFile(t, root, rel, body)
			}
			decls, err := CollectDecls(root)
			if err != nil {
				t.Fatalf("CollectDecls: %v", err)
			}
			if len(decls) != 1 {
				t.Fatalf("CollectDecls 得到 %d 个声明，want 1（扫描规则没命中合成夹具，"+
					"本自测就失去意义）: %+v", len(decls), decls)
			}
			never, err := NeverRecorded(root, decls)
			if err != nil {
				t.Fatalf("NeverRecorded: %v", err)
			}
			got := len(never) > 0
			if got != tc.wantOut {
				t.Errorf("NeverRecorded 判定 = %v（want %v）: %s", got, tc.wantOut, tc.why)
			}
		})
	}
}

// TestScannerSkipsVendorAndBuildDirs 守住目录跳过规则。
// 漏掉这条会让 vendor 里的第三方指标混进结果，门从"偶发误报"变成"永远误报"。
func TestScannerSkipsVendorAndBuildDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "decl.go", declBody)
	writeFile(t, root, "use.go", recordedBody)
	// vendor 与构建产物里放一个从不记录的同名指标形态。
	writeFile(t, root, "vendor/github.com/x/p/decl.go", declBody)
	writeFile(t, root, ".build-local/llm-gateway-go/decl.go", declBody)
	writeFile(t, root, "web/src/x.go", declBody)

	decls, err := CollectDecls(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls) != 1 {
		names := make([]string, 0, len(decls))
		for _, d := range decls {
			names = append(names, d.File)
		}
		t.Fatalf("CollectDecls 得到 %d 个声明（want 1），跳过规则失效: %v", len(decls), names)
	}
}
