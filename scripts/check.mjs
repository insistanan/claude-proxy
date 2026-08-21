// 全量门禁：前端 type-check + 图标扫描；后端 gofmt + vet + test。
// 等价于根 Makefile 的 check 目标，供未安装 make 的环境直接 `npm run check` 使用。
import { spawnSync } from "node:child_process";
import process from "node:process";

const isWin = process.platform === "win32";
// Windows 下 npm 是 npm.cmd，spawn 需要带扩展名；其余平台直接用 npm。
const npmCmd = isWin ? "npm.cmd" : "npm";

function fail(name, detail) {
  console.error(`\n[check] FAILED: ${name}`);
  if (detail) console.error(detail);
  process.exit(1);
}

// 带输出校验的步骤：命令本身退出码为 0 不代表通过（如 gofmt -l）。
function runValidated(name, cmd, args, cwd, validate) {
  console.log(`\n==> ${name}`);
  const r = spawnSync(cmd, args, { cwd, encoding: "utf8" });
  if (r.error) fail(name, r.error.message);
  if (r.status !== 0) fail(name, r.stdout + r.stderr);
  const problem = validate(r.stdout);
  if (problem) fail(name, problem);
  console.log("[check] OK");
}

function run(name, cmd, args, cwd, opts = {}) {
  console.log(`\n==> ${name}`);
  const r = spawnSync(cmd, args, { cwd, stdio: "inherit", ...opts });
  if (r.error) fail(name, r.error.message);
  if (r.status !== 0) fail(name);
  console.log("[check] OK");
}

runValidated(
  "后端 gofmt 校验",
  "gofmt",
  ["-l", "."],
  "backend-go",
  (out) => out.trim() && `以下文件未格式化:\n${out}`,
);

run("后端 go vet", "go", ["vet", "./..."], "backend-go");
run("后端 go test", "go", ["test", "./..."], "backend-go");
// Windows 下 npm 是 npm.cmd，必须经 shell 启动；命令串 + 空 args 避免 DEP0190。
run("前端 type-check + 图标扫描", isWin ? "npm run check" : "npm", isWin ? [] : ["run", "check"], "frontend", isWin ? { shell: true } : {});

console.log("\n[check] 全量门禁通过");
