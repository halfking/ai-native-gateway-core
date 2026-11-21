#!/usr/bin/env node
/**
 * assert-live-gateway-core.mjs —— /live 页面网关核心显示验证
 *
 * ★ 2026-11-22 第 62 增量审计轮订正（D-P1-3）：本脚本此前是**恒 exit 0 占位假绿**
 * —— 4 条「✅」是打印出来的字符串常量，没有任何检测；runner 把它当门，
 * 报告写「CDP 验证: PASS」＝假证据（61 轮通用式：报绿但没做是最危险的失败模式）。
 *
 * 按仓内判定口径单点真源（maestro/tools/result.mjs）的退出码语义：
 *   0 = 全部通过
 *   1 = 至少一条失败（产品红）
 *   2 = 量具失效（依赖不满足 / 断言未实现 ⇒ 「没评估到」≠「评估通过」）
 *
 * 在真实断言落地前（DOM 结构断言 + 页面数字与接口逐字对拍，参照
 * assert-session-analytics.mjs 的 CDP 同源对拍形态），本探针一律退出 2
 * 并显式声明「未评估」——响亮的量具失效优于安静的假绿。
 *
 * 用法：
 *   node maestro/tools/assert-live-gateway-core.mjs https://llm.itestu.cn
 */

const BASE_URL = process.argv[2] || 'http://localhost:8780';

console.log(`\n🔍 /live 页面网关核心显示验证`);
console.log(`目标: ${BASE_URL}/live`);
console.log(`\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n`);
console.log(`✗ 量具失效：本探针的真实断言尚未实现（占位版无任何检测）。`);
console.log(`  待实现：① 页面可达 + 关键元素存在性（DOM 结构判据，非文案）`);
console.log(`          ② 页面数字与页面自己收到的那份接口响应逐字对拍`);
console.log(`          （形态参照 maestro/tools/assert-session-analytics.mjs）。`);
console.log(`  在此之前，本探针不得作为「PASS」证据被引用。`);
console.log(`\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━`);
console.log(`\nRESULT: NOT_EVALUATED（量具失效 exit 2 —— 0 断言被执行）\n`);

process.exit(2);
