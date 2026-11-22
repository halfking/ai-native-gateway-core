#!/usr/bin/env node
/**
 * extract-routes.mjs — 用 TypeScript AST 从 router.ts 提取所有路由
 * 
 * 用途：L10 收口 — 列出未被任何 flow 覆盖的路由
 * 
 * 输出：JSON 数组，每项包含 path / meta / component / redirect 等信息
 */

import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';
import { createRequire } from 'module';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

/**
 * ★ 2026-11-23（L10 复核轮）：typescript 的解析位置必须**显式指定**。
 *
 * 原来写 `import ts from 'typescript'` ⇒ ESM 的模块解析从**本文件所在目录**
 * （maestro/tools/）向上找 node_modules ⇒ 仓内 typescript 装在 **web/**
 * （前端工作区）⇒ 仓根跑这个工具直接 `ERR_MODULE_NOT_FOUND` ⇒
 * ★ **工具不可复跑 ⇒ 它产出的报告也不可复现**（L10 的工具价值就在「可重复运行」）。
 *
 * ⇒ 用 createRequire 从 **web/package.json** 那个位置解析 ⇒ 无论从哪个目录
 *   调起都能命中仓内那份 typescript（不依赖 npm 的 node_modules 提升行为）。
 */
const webRequire = createRequire(resolve(__dirname, '../../web/package.json'));
const ts = webRequire('typescript');

const routerPath = resolve(__dirname, '../../web/src/router.ts');
const source = readFileSync(routerPath, 'utf-8');

const sourceFile = ts.createSourceFile(
  'router.ts',
  source,
  ts.ScriptTarget.Latest,
  true
);

const routes = [];

function visitNode(node) {
  // 查找 routes: [ ... ] 数组
  if (ts.isPropertyAssignment(node) && 
      ts.isIdentifier(node.name) && 
      node.name.text === 'routes' &&
      ts.isArrayLiteralExpression(node.initializer)) {
    
    // 遍历数组中的每个路由对象
    node.initializer.elements.forEach(element => {
      if (ts.isObjectLiteralExpression(element)) {
        const route = extractRoute(element);
        if (route) {
          routes.push(route);
        }
      }
    });
  }

  ts.forEachChild(node, visitNode);
}

function extractRoute(objLiteral) {
  const route = {};
  
  objLiteral.properties.forEach(prop => {
    if (!ts.isPropertyAssignment(prop)) return;
    
    const name = prop.name.text;
    
    if (name === 'path') {
      route.path = getStringValue(prop.initializer);
    } else if (name === 'redirect') {
      // redirect 可能是字符串或对象 { path: '...' }
      if (ts.isStringLiteral(prop.initializer)) {
        route.redirect = prop.initializer.text;
      } else if (ts.isObjectLiteralExpression(prop.initializer)) {
        const pathProp = prop.initializer.properties.find(p => 
          ts.isPropertyAssignment(p) && p.name.text === 'path'
        );
        if (pathProp) {
          route.redirect = getStringValue(pathProp.initializer);
        }
      }
    } else if (name === 'meta') {
      route.meta = extractMeta(prop.initializer);
    } else if (name === 'component') {
      route.component = getComponentName(prop.initializer);
    } else if (name === 'children') {
      // 递归处理子路由
      if (ts.isArrayLiteralExpression(prop.initializer)) {
        route.children = [];
        prop.initializer.elements.forEach(child => {
          if (ts.isObjectLiteralExpression(child)) {
            const childRoute = extractRoute(child);
            if (childRoute) {
              route.children.push(childRoute);
            }
          }
        });
      }
    }
  });
  
  return route;
}

function extractMeta(node) {
  const meta = {};
  
  if (!ts.isObjectLiteralExpression(node)) {
    return meta;
  }
  
  node.properties.forEach(prop => {
    if (!ts.isPropertyAssignment(prop)) return;
    
    const name = prop.name.text;
    
    if (name === 'public') {
      meta.public = prop.initializer.kind === ts.SyntaxKind.TrueKeyword;
    } else if (name === 'requiresAuth') {
      meta.requiresAuth = prop.initializer.kind === ts.SyntaxKind.TrueKeyword;
    } else if (name === 'requiresSuper') {
      meta.requiresSuper = prop.initializer.kind === ts.SyntaxKind.TrueKeyword;
    } else if (name === 'requiresPlatformOps') {
      meta.requiresPlatformOps = prop.initializer.kind === ts.SyntaxKind.TrueKeyword;
    } else if (name === 'requiresProviderConsole') {
      meta.requiresProviderConsole = prop.initializer.kind === ts.SyntaxKind.TrueKeyword;
    }
  });
  
  return meta;
}

function getStringValue(node) {
  if (ts.isStringLiteral(node)) {
    return node.text;
  }
  if (ts.isTemplateExpression(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    // 模板字符串，返回原始文本
    return node.getText();
  }
  return null;
}

function getComponentName(node) {
  // 尝试提取组件名称
  if (ts.isIdentifier(node)) {
    return node.text;
  }
  if (ts.isArrowFunction(node) || ts.isFunctionExpression(node)) {
    // import() 动态导入
    const text = node.getText();
    const match = text.match(/import\(['"](.+)['"]\)/);
    if (match) {
      return match[1];
    }
  }
  if (ts.isCallExpression(node)) {
    // externalMaintainRedirect 等调用
    const text = node.getText();
    if (text.includes('externalMaintainRedirect')) {
      return 'externalMaintainRedirect';
    }
  }
  return null;
}

// 展开所有路由（包括子路由）到扁平数组
function flattenRoutes(routes, parentPath = '') {
  const result = [];
  
  for (const route of routes) {
    const fullPath = route.path 
      ? (parentPath ? `${parentPath}/${route.path}` : route.path).replace(/\/+/g, '/')
      : parentPath;
    
    const flatRoute = {
      path: fullPath,
      redirect: route.redirect,
      meta: route.meta || {},
      component: route.component
    };
    
    result.push(flatRoute);
    
    if (route.children) {
      result.push(...flattenRoutes(route.children, fullPath));
    }
  }
  
  return result;
}

visitNode(sourceFile);

const flatRoutes = flattenRoutes(routes);

// 按 path 排序
flatRoutes.sort((a, b) => a.path.localeCompare(b.path));

// 输出 JSON
console.log(JSON.stringify(flatRoutes, null, 2));
