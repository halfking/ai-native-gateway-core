# 48 小时修正任务审计与三环境发布计划

**目标**：审查最近 48 小时提交和全部初始 WIP，修正可复现问题，小步合并推送，按 211 → 245 → 154 验证发布。

**基线**：本地 main `bde2baa598e44f1391c4a15a9eb0e6f88b962ce3`；初始 10 个 tracked 修改与 1 个 untracked 文档。原始 patch/status/逐文件备份在 `/tmp/lgw-48h-initial*`。首次 fetch 后 ff 到 `1fa2ea5925174051bba6925fbffedb4315fc5bf8`，43 文件，和初始 WIP 无交集。

**方法**：ACC ci-review → bugfix → 部署流程；采用已有当前 checkout，域工作并行但文件所有权互斥。每段先行为回归、编译与 vet，再 review、Task Stop Summary、commit、merge --no-ff、push。未通过的检查不得描述为通过。

- [x] 读取路由、verification-before-completion、task-stop-audit、diagnose/tdd/review、部署与浏览器技能。
- [x] 保存初始工作树，fetch 并 ff 合入最新主干。
- [x] 固定审计时点，输出逐提交/逐文件清单，核对本地及两远端全部 heads；近一周且一小时前的不活跃未合并分支逐文件判定后处置。
- [x] MCP 控制面：HTTP 行为测试先复现租户/权限边界，再最小修正；保留初始 WIP。
- [x] 统计：以整桶替换行为复现迟到重扫截断，修正完整闭分钟边界。
- [x] MaaS：参考价失败可见、gift 预算、首购资格与确认链行为回归。
- [x] 核对 953 迁移源码与真库应用记录，先证据后决定可否修改既有字节。
- [x] 本地 full build/vet/test（CGO=1）、installer、审计守卫、前端检查；记录失败根因及范围。
- [x] 三环境应用/数据库日志与慢 SQL 实测，按证据实施优化并比较前后。
- [x] 分段审查提交推送；按发布脚本部署 211，readyz/版本/日志/浏览器 admin 登录与功能验证；通过后 245，再 154。
- [x] 生成完整分目标报告、更新 AGENTS handoff、下一轮提示词；session append/pin/archive。

**证据位置**：命令原始日志 `/tmp/lgw-48h-audit/`；可发布且脱敏的清单及报告写入本目录。密码、token、DSN 和原始业务日志不得入仓。

**回滚**：每个修正可 `git revert <commit>`；211 保留旧蓝绿槽并按部署引擎回切；245/154 使用 `bash scripts/deploy-seamless.sh rollback <target>`。数据库先确认迁移是否应用，不改已应用字节以规避 checksum 门禁。

**执行结果**：三环境应用sha998269fb；211/245/154 seq2978/2956/2957。发布中修复迁移DNS歧义及154混合nginx worker；完整证据和遗留见同日remediation-report。
