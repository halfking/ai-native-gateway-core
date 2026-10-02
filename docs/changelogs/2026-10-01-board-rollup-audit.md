# 看板分钟重算复审（2026-10-01）

上一轮只重算了 `request_stats_minute`。维度表仍大约是日志的一半。744 的索引导挂会把数据库启动判失败。

## 核对过的数

UTC `[2026-09-24 00:00, 整分钟)`：

- 日志费用 `$151.806657`，1,097,722 笔。
- 主分钟表 `$152.091165`，1,097,989 笔。对得上的键差为 0。多出的 `$0.284508` 当时写成 canonical 已清空。再核见同日设计稿「再核」一节：`0.284420` 的成功日志 canonical 仍是 887407，分钟表上这一键是累加器按日志再写的一份。
- provider 维度重算前 `$89.030981` / 551,309。重算后 `$151.806657` / 1,097,841。费用与日志一致，请求数多 119。
- 删掉看板 Redis 基线并重启后，`days=7` 英雄卡 `$152.091165`，供应商饼图 21 项合计 `$151.806657`。

英雄卡仍读分钟表。

## 代码

`attachDigestNullChildrenSQL` 在分区表上已有任意子索引时跳过 ATTACH。8782 当时的进程没有这份守卫；11 月索引已改成规范名后，旧进程可以启动。

## 测试

`go test ./db/ -count=1 -run TestAttachDigestNullSkipsWhenPartitionAlreadyHasChild` 通过。`go test ./bg/ -count=1 -run 'TestRollupScanSince|TestRollupPredicates'` 通过。没有对删除旧键做回归，因为这 267 行没有删。
