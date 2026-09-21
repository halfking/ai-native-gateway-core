# fuzz 失败样本回归流程

IR 解析器 fuzz 目标（`internal/ir/fuzz_parsers_test.go`）的失败样本处理闭环：
**限时 fuzz → 崩溃自动最小化（Go 内置）→ 脱敏检查 → 入库为回归语料 → 普通 go test 永久回归**。

## 组成

| 文件 | 作用 |
|---|---|
| `fuzz-regress.sh run [fuzztime]` | 对全部已登记 fuzz 目标限时运行；失败时定位最小化样本并做脱敏检查，给出入库命令 |
| `fuzz-regress.sh install <样本> <目标>` | 脱敏检查通过后，把样本复制到 `internal/ir/testdata/fuzz/<目标>/` 并跑回归验证 |
| `sanitize_corpus.sh [--all\|<文件>]` | 扫描语料中的敏感串（密钥/令牌/邮箱/内网 IP 等），命中即退出 1 |
| `allowlist.txt` | 文件级误报放行清单（basename 精确匹配），默认为空 |
| `internal/ir/testdata/fuzz/` | 回归语料：崩溃样本 + 手工边界种子；普通 `go test ./internal/ir` 即会执行 |

## 崩溃处置流程

```bash
# 1. 限时 fuzz（CI smoke 为 5s，本地可加大）
scripts/fuzz/fuzz-regress.sh run 30s

# 2. 失败时脚本会打印最小化样本路径与脱敏结果；确认可复现且已脱敏后入库：
scripts/fuzz/fuzz-regress.sh install /path/to/crasher FuzzIRParsersNeverPanic

# 3. 提交语料（会被 make fuzz-corpus-lint 与 CI 检查）
```

## 约定

- 样本最小化由 `go test -fuzz` 引擎在失败退出前自动完成，本流程不重复最小化。
- 入库样本必须通过脱敏检查；确需替换敏感值时保持等长/等结构，再确认崩溃仍可复现。
- 新增 fuzz 函数时在 `fuzz-regress.sh` 的 `FUZZ_TARGETS` 中登记。
- `SKIP_SANITIZE=1` 仅供本地临时调试，禁止用于入库路径。
