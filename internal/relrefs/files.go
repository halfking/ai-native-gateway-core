package relrefs

import (
	"fmt"
	"os/exec"
	"strings"
)

// RepoFiles 列出仓内匹配 patterns 的已跟踪文件。
//
// ★ 用 `git ls-files` 而不是目录遍历 ⇒ ① 不会把构建产物/临时文件算进去
//
//	② 集合与 CI 看到的一致 ⇒ 门与工具读的是同一份事实源。
//
// ★★★ ⚠⚠ 必须加 `-z`（NUL 分隔）—— ★★★ 否则**非 ASCII 路径会被静默转义**：
//
//	`git ls-files '*.sql'` 默认 `core.quotePath=true` ⇒ 含中文的路径会被输出成
//	`"docs/\345\205\250\346\241\210..."`（八进制转义 + 双引号）⇒
//	⇒ 拿这个字符串去开文件 ⇒ **必然 file not found** ⇒
//	⇒ 而【那批文件里的 DDL 就静默缺席了】⇒ ⇒ 「悬空引用」被高估。
//	⇒ ⇒ `-z` 模式下 git 不加引号也不转义 ⇒ 按 NUL 切分即可。
//	⇒ ⇒ ★★ 这是「量具自身的读取方式」的坑：**它不会报错，只会少读**。
func RepoFiles(root string, patterns ...string) ([]string, error) {
	args := append([]string{"ls-files", "-z"}, patterns...)
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files %v: %w", patterns, err)
	}
	var res []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			res = append(res, p)
		}
	}
	return res, nil
}
