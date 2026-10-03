'use strict';
// npm-spawn-shape-probe.cjs — 真跑一次 npm 入口的 Windows spawn 形状。
//
// 存在的理由：只断言「生成的命令字符串」抓不住真实事故。事故是
// `cmd.exe -c "<cmd>"` —— cmd 的开关是 `/c`，给成 `-c` 且没有 `/c` 时 cmd 会
// 起一个交互式 cmd：打印 banner 和提示符、什么都不执行、退出码还是 0。
// 字符串完全正确，错的是怎么把它送进 shell，所以门必须真的 spawn 一次。
//
// 它按 fallbackPlan 产出的 bin/args 原样执行，只把「取回并执行」那一段换成
// marker（不联网），于是这一次真跑同时证明：
//   1. 这套 argv 真的会被执行（而不是起一个空交互 shell）
//   2. argv 真的被翻译成了 $env:MODE 等环境变量
//
// 用法：node npm-spawn-shape-probe.cjs <path-to-llm-gw-installer.js>

const { spawnSync } = require('child_process');

const target = process.argv[2];
if (!target) {
  console.log('PROBE_ERROR: 缺少 llm-gw-installer.js 路径');
  process.exit(0);
}

let m;
try {
  m = require(target);
} catch (e) {
  console.log('PROBE_ERROR: require 失败 ' + e.message);
  process.exit(0);
}

const plan = m.fallbackPlan('http://x/install', ['--mode', 'lite'], 'win32');
process.stdout.write('bin=' + plan.bin + ' ');

if (plan.bin !== 'powershell.exe') {
  process.stdout.write('PROBE_ERROR: windows 计划没有直连 powershell.exe，而是 ' + plan.bin + '\n');
  process.exit(0);
}

const args = plan.args.slice();
const src = args[args.length - 1];
if (!/^\$env:MODE='lite';/.test(src)) {
  process.stdout.write('PROBE_ERROR: 脚本没有 $env:MODE 前缀，实际=' + JSON.stringify(src) + '\n');
  process.exit(0);
}

// 只替换 `$s = ` 之后的部分，保留 argv→环境变量的映射前缀。
args[args.length - 1] = src.replace(
  /\$s = [\s\S]*$/,
  'Write-Output SPARNSHAPE_RAN; Write-Output ("mode=" + $env:MODE)'
);

const r = spawnSync(plan.bin, args, { encoding: 'utf8' });
process.stdout.write('status=' + r.status + ' ');
process.stdout.write('out=' + String((r.stdout || '') + (r.stderr || '')).trim().replace(/\s+/g, ' ') + '\n');
