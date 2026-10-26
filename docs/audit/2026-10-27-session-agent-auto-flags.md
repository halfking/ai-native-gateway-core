# 211 session-agent 试运行的自动标题/摘要开关

网关发布引擎此前会重写 `gateway.env`，且容器 `GW_ENV` 没有这两个开关。
因此只修改当前容器或保存文件，下一次蓝绿发布仍会恢复自动生成。

`scripts/deploy-seamless-docker.sh` 现在按「调用环境 > 目标机已保存配置 >
不设置」读取 `LLM_GATEWAY_AUTO_TITLE_ENABLED` 和
`LLM_GATEWAY_AUTO_SUMMARY_ENABLED`，将合法值规范成 `true`/`false`，写回
已有环境文件并注入新容器。`--via` 也保留这两个显式调用值。
未配置时保持网关原有默认行为；空值及非法值在 SSH 或目标机写入前拒绝，
错误只含键名。保存文件作为文本读取，不作为 shell 代码执行。

用户已授权 211 临时关闭两项自动任务，保留回滚。该脚本改动本身不会关闭
任何正在运行的容器。部署操作需同时备份目标机原引擎、原 `gateway.env`
及当前容器，再应用两个 `false`。回滚必须恢复三者，且应核对现场容器和
配置指纹，防止覆盖并发发布。恢复开关也可通过下一次正常发布显式传入两个
`true`；删除保存项并取消调用环境覆盖则恢复网关默认值。

验证命令：

```sh
bash scripts/deploy-seamless-auto_env_test.sh
bash scripts/deploy-seamless-asm_env_test.sh
bash -n scripts/deploy-seamless-docker.sh scripts/deploy-seamless-auto_env_test.sh
shellcheck scripts/deploy-seamless-auto_env_test.sh
```

新门执行引擎的真实读取、保存、注入与转发片段，并通过真实入口检查非法值
在外部命令之前失败。负控分别移除保存、注入、转发及入口验证，四臂均被抓住。
未连接 211、未调用模型、未提交或推送。整个引擎的 ShellCheck 有 14 项既有
诊断；相对 baseline 的新增诊断为 0，不能将其报告成整个引擎零告警。
