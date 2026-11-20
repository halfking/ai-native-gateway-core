#!/usr/bin/env node
/**
 * extract-routes-simple.mjs — 简单版路由提取器（不依赖 typescript 包）
 * 
 * 从 router.ts 提取所有路由 path，用于 L10 路由覆盖核对
 */

import { readFileSync } from 'fs';
import { resolve } from 'path';

const routerPath = resolve(process.cwd(), 'web/src/router.ts');
const source = readFileSync(routerPath, 'utf-8');

// 找到 routes 数组的起止位置
const routesMatch = source.match(/const routes.*?=\s*\[([\s\S]*?)\n\]/);
if (!routesMatch) {
  console.error('无法找到 routes 数组');
  process.exit(1);
}

const routesContent = routesMatch[1];

// 提取所有 path: 'xxx' 或 path: "xxx"
const pathRegex = /path:\s*['"`]([^'"`]+)['"`]/g;
const paths = [];
let match;

while ((match = pathRegex.exec(routesContent)) !== null) {
  paths.push(match[1]);
}

// 去重并排序
const uniquePaths = [...new Set(paths)].sort();

console.log(`从 router.ts 提取到 ${uniquePaths.length} 条路由路径：\n`);
uniquePaths.forEach((path, i) => {
  console.log(`${(i + 1).toString().padStart(3)}.  ${path}`);
});

console.log(`\n总计：${uniquePaths.length} 条`);
