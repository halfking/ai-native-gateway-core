package dbinit

import (
	"strings"
	"testing"
)

// TestStartupFilesVersionsAreUnique —— 安装器投递清单的版本号唯一性（★ 状态门）。
//
// ★ 这道门与仓内 `sql/migrations/startup` 的目录级重号门
// （TestNumericUpMigrationVersionsAreUnique）**覆盖面不同、不是重复**：
//
//	★ 目录级门 ＝ 扫【磁盘上的目录】⇒ 抓「同一目录里两个文件同号」；
//	★ 本门      ＝ 扫【安装器实际投递的清单 StartupFiles】⇒ 抓
//	  「清单里两行同号」★ —— ★ 而清单是**手写维护**的（★ 不是目录扫描），
//	  ★ ★ 且**清单才是真正决定「谁会被应用」的东西**。
//
// ★ 2026-11-23 实测的真事故（★ 本门的存在理由）：★ origin/main 的
// ★ `982_provider_call_attempts.sql`（供应商逐次调用落库）与并行车分支的
// ★ `982_teams_table.sql`（teams 族首件）同号 ⇒ ★ 合并后 StartupFiles 里
// ★ 两行同号（★ 实测行号 2049 / 2064）⇒ ★ 增量通道会**静默跳过第二份**。
func TestStartupFilesVersionsAreUnique(t *testing.T) {
	files := NewRunner("citus", "user", "db", "").StartupFiles
	if len(files) == 0 {
		t.Fatal("StartupFiles 为空：测试失去意义")
	}
	if err := validateStartupVersionsUnique(files); err != nil {
		t.Fatalf("安装器投递清单存在重号（★ 增量通道会静默跳过第二份、★ 全新装会让台账与实际不符）: %v", err)
	}
}

// TestValidateStartupVersionsUniqueRejectsDuplicate —— 判据本身的正向自证。
//
// ★ 口径：★ 同号 ⇒ 报错；★ 不同号 ⇒ 放行；★ 无数字前缀 ⇒ 不参与判定
// （★ 与增量通道「无版本号 ⇒ 恒执行」口径一致）。
func TestValidateStartupVersionsUniqueRejectsDuplicate(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		wantErr bool
	}{
		{name: "不同号放行", files: []string{"981_a.sql", "983_b.sql", "985_c.sql"}},
		{name: "无前缀放行", files: []string{"session_turns_hot_bootstrap.sql", "982_a.sql"}},
		{name: "数字加字母前缀放行", files: []string{"361b_prelude.sql", "982_a.sql"}},
		{name: "同号报错", files: []string{"982_a.sql", "982_b.sql"}, wantErr: true},
		{name: "同号但隔着一个不同号仍报错", files: []string{"982_a.sql", "983_b.sql", "982_c.sql"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStartupVersionsUnique(tc.files)
			if tc.wantErr && err == nil {
				t.Fatalf("期望重号报错、★ 实际放行：%v", tc.files)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望放行、★ 实际报错：%v（%v）", tc.files, err)
			}
		})
	}
}

// TestStartupVersionCollision982IsRejected —— 把 2026-11-23 那次真撞车钉住。
//
// ★ 为什么值得单独留一条：★ 上面的门是【状态门】（清单当前无重号），
// ★ 而这条是【事故门】（★ 那一对具体文件必须被判红）⇒ ★★ 于是「那次撞车
// ★ 发生过、且被量过」从此有机器可查的痕迹（★ 与仓内「缺口登记成棘轮门」
// ★ 同款做法）。
func TestStartupVersionCollision982IsRejected(t *testing.T) {
	colliding := []string{
		"982_provider_call_attempts.sql", // origin/main 2026-11-23 18:51
		"982_teams_table.sql",            // 并行车分支 2026-11-23 10:20
	}
	err := validateStartupVersionsUnique(colliding)
	if err == nil {
		t.Fatal("2026-11-23 那次真撞车必须被判红（★ 982 两份不同 schema）")
	}
	if !strings.Contains(err.Error(), "982") {
		t.Fatalf("报错必须点名版本号 982，★ 实际：%v", err)
	}
}

// TestInitSchemaRejectsDuplicateVersionsBeforeAnyDBWork —— 中止点必须早于任何数据库动作。
//
// ★ 这条判的是【顺序】而不是【有没有报错】：★ 守卫若放在数据库动作之后，
// ★ 那么「重号」这件事会先在生产上留下半截状态 ⇒ ★ 守卫必须 fail-closed
// 且**零写**。
//
// ★ 手法：★ 给一个【根本不存在】的容器名（★ 若守卫没挡住、★ 代码会先去
// ★ 连容器 ⇒ 报的是 docker/连接类错误）⇒ ★ 期望拿到的却是【重号】错误
// ★ ⇒ ★★ 即证明中止发生在任何数据库动作之前。
func TestInitSchemaRejectsDuplicateVersionsBeforeAnyDBWork(t *testing.T) {
	r := NewRunner("definitely-not-a-real-container", "u", "d", "")
	r.StartupFiles = []string{
		"982_provider_call_attempts.sql",
		"982_teams_table.sql",
	}
	err := r.InitSchema(func(string) {})
	if err == nil {
		t.Fatal("重号清单必须让 InitSchema 失败")
	}
	if !strings.Contains(err.Error(), "重号") || !strings.Contains(err.Error(), "982") {
		t.Fatalf("期望【重号 + 982】的守卫错误，★ 实际：%v", err)
	}
	if strings.Contains(err.Error(), "docker") || strings.Contains(err.Error(), "container") {
		t.Fatalf("★ 中止必须早于任何数据库动作（★ 不该出现连接类错误），★ 实际：%v", err)
	}
}
