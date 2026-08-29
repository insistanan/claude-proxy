// 复制前端构建产物到 Go embed 目录。
// 唯一权威入口；所有 Makefile / build.sh / release workflow 都通过此脚本复制，
// 避免在不同 cwd 下 cp -r 时产生 backend-go/backend-go/... 或 frontend/backend-go/...
// 等嵌套 dist 目录（历史 bug：曾手动 cp 误把 frontend/dist 复制到了错误的相对路径下）。
//
// 行为约定：
// - 仓库根 = scripts/ 所在目录的父级（按 import.meta.url 解析，与 cwd 无关）。
// - 源：<repo>/frontend/dist（Vite build 产物）
// - 目标：<repo>/backend-go/frontend/dist（被 backend-go/main.go 的 //go:embed all:frontend/dist 引用）
// - 清空目标目录后整体复制（rm -rf + cp -r）。
// - 源不存在 → 退出码 2，提示先 `cd frontend && npm run build`。
// - 目标残留 .gitkeep 不删除（保留用户标记）。
//
// 用法：
//   node scripts/copy-frontend.mjs           # 执行复制
//   node scripts/copy-frontend.mjs --check   # 仅做嵌套 dist 体检（CI 门禁模式）

import { existsSync, mkdirSync, rmSync, cpSync, readdirSync, statSync } from "node:fs";
import { dirname, resolve, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import process from "node:process";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..");
const srcDir = join(repoRoot, "frontend", "dist");
const dstDir = join(repoRoot, "backend-go", "frontend", "dist");

const mode = process.argv[2];

function findNestedDists() {
  // 任意位置出现的 backend-go/backend-go 或 frontend/backend-go 子树都视为脏
  const hits = [];
  for (const candidate of [
    join(repoRoot, "backend-go", "backend-go"),
    join(repoRoot, "frontend", "backend-go"),
  ]) {
    if (existsSync(candidate)) hits.push(relative(repoRoot, candidate));
  }
  return hits;
}

function copyDirContents(from, to) {
  // 等价于 `rm -rf <dst>; mkdir -p <dst>; cp -r <src>/* <dst>/`，但跨平台且对 cwd 免疫。
  if (existsSync(to)) {
    for (const entry of readdirSync(to)) {
      rmSync(join(to, entry), { recursive: true, force: true });
    }
  } else {
    mkdirSync(to, { recursive: true });
  }
  for (const entry of readdirSync(from)) {
    const s = join(from, entry);
    const d = join(to, entry);
    cpSync(s, d, { recursive: true });
  }
}

if (mode === "--check") {
  const nested = findNestedDists();
  if (nested.length) {
    console.error("[copy-frontend] 检测到嵌套 dist 残留：");
    for (const p of nested) console.error("  - " + p);
    console.error("[copy-frontend] 请 `rm -rf` 后再提交。");
    process.exit(1);
  }
  // 同时校验目标目录是否就是 embed 指向的那一份
  const expectedDst = dstDir;
  if (!existsSync(expectedDst)) {
    // 目标不存在不算错（首次构建前），但提示一下
    console.log("[copy-frontend] OK（目标暂不存在）");
    process.exit(0);
  }
  const stat = statSync(expectedDst);
  if (!stat.isDirectory()) {
    console.error(`[copy-frontend] 目标不是目录: ${expectedDst}`);
    process.exit(1);
  }
  console.log("[copy-frontend] OK");
  process.exit(0);
}

if (mode !== undefined && mode !== "--help" && mode !== "-h") {
  console.error(`[copy-frontend] 未知参数: ${mode}`);
  console.error("用法: node scripts/copy-frontend.mjs [--check|--help]");
  process.exit(2);
}

if (mode === "--help" || mode === "-h") {
  console.log("用法:");
  console.log("  node scripts/copy-frontend.mjs           复制 frontend/dist → backend-go/frontend/dist");
  console.log("  node scripts/copy-frontend.mjs --check   仅做嵌套 dist 体检");
  process.exit(0);
}

if (!existsSync(srcDir)) {
  console.error(`[copy-frontend] 源目录不存在: ${srcDir}`);
  console.error("  请先构建前端: cd frontend && npm run build");
  process.exit(2);
}

const start = Date.now();
copyDirContents(srcDir, dstDir);
const ms = Date.now() - start;
console.log(`[copy-frontend] 已复制 ${srcDir} -> ${dstDir} (${ms} ms)`);

// 复制完再体检一次，留存快照防漂移
const nested = findNestedDists();
if (nested.length) {
  console.error("[copy-frontend] 复制后检测到嵌套 dist（异常，请检查复制脚本）：");
  for (const p of nested) console.error("  - " + p);
  process.exit(1);
}
