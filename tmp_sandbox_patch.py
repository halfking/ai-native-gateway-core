# -*- coding: utf-8 -*-
import io, shutil

sandbox = '/private/tmp/l30-982-merge/installer/internal/dbinit/'
mine = '/private/tmp/l30-admin-hunt/installer/internal/dbinit/'

# 1) 拷测试文件
shutil.copy(mine + 'runner_version_unique_test.go', sandbox + 'runner_version_unique_test.go')

# 2) 把守卫函数 ＋ InitSchema 调用 拼进沙箱的 runner.go（★ 沙箱的 runner.go 已被并行车改过 16 行）
p = sandbox + 'runner.go'
s = io.open(p, encoding='utf-8').read()

guard_func = u'''// validateStartupVersionsUnique 校验 startup 清单里没有两个文件共用同一个数字版本号。
//
// ★ 判据口径（★ 与仓内 `sql/migrations/startup` 的目录级重号门同款）：
//   - 版本号＝文件名第一个下划线前的数字前缀（★ 复用 migrationVersionOf ⇒
//     `361b_...prelude` 这类「数字＋字母」前缀解析不出 ⇒ 不参与重号判定、
//     ★ 与增量通道「无版本号 ⇒ 恒执行」的口径一致）；
//   - `.down.sql` 不在 StartupFiles 里（★ 清单只投递 up 迁移）⇒ 不额外排除；
//   - 同号即报错、★ **不设豁免表**（★ 新清单不该有重号；★ 真要豁免必须写清
//     「量在哪」★ 而不能像历史目录级门那样把旧账留在表里）。
func validateStartupVersionsUnique(files []string) error {
	seen := make(map[int]string, len(files))
	for _, name := range files {
		v, ok := migrationVersionOf(name)
		if !ok {
			continue
		}
		if prev, dup := seen[v]; dup {
			return fmt.Errorf("startup migration version %d is duplicated by %s and %s", v, prev, name)
		}
		seen[v] = name
	}
	return nil
}

'''

anchor = u'// startupPending 报告增量模式下该 startup 文件是否需要执行：'
assert anchor in s
assert 'func validateStartupVersionsUnique' not in s
s = s.replace(anchor, guard_func + anchor, 1)

call_anchor = u'func (r *Runner) InitSchema(logger func(string)) error {\n\tapplied, hasLedger, err := r.appliedVersions()'
assert call_anchor in s, 'InitSchema anchor not found'
call_new = u'''func (r *Runner) InitSchema(logger func(string)) error {
	// ★ 沙箱验证用：重号守卫（★ 真身在并行车分支待 pick 的提交 1d2875245 里）
	if err := validateStartupVersionsUnique(r.StartupFiles); err != nil {
		return fmt.Errorf("startup 迁移清单版本号重号，拒绝启动（fail-closed，不做任何数据库动作）: %w", err)
	}

	applied, hasLedger, err := r.appliedVersions()'''
s = s.replace(call_anchor, call_new, 1)
io.open(p, 'w', encoding='utf-8').write(s)
print('sandbox patched')
