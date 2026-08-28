import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import process from "node:process";

const mode = process.argv[2];
if (mode !== "--check" && mode !== "--staged-fix") {
  console.error("usage: node scripts/eol.mjs --check|--staged-fix");
  process.exit(2);
}

const BINARY_EXT = new Set(["png", "jpg", "jpeg", "gif", "ico", "woff", "woff2", "ttf", "eot", "lockb"]);

function gitNul(args) {
  return execFileSync("git", args).toString("utf8").split("\0").filter(Boolean);
}

function isBinaryPath(file) {
  const dot = file.lastIndexOf(".");
  if (dot < 0) return false;
  return BINARY_EXT.has(file.slice(dot + 1).toLowerCase());
}

const files =
  mode === "--staged-fix"
    ? gitNul(["diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z"])
    : gitNul(["ls-files", "-z"]);

const crlf = [];
for (const file of files) {
  if (isBinaryPath(file) || !existsSync(file)) continue;
  let buf;
  try {
    buf = readFileSync(file);
  } catch {
    continue;
  }
  if (buf.includes(0) || !buf.includes(0x0d)) continue;
  crlf.push(file);
}

if (mode === "--check") {
  if (crlf.length) {
    console.error("以下文件含 CRLF，仓库要求 LF:\n" + crlf.join("\n"));
    process.exit(1);
  }
  process.exit(0);
}

for (const file of crlf) {
  const text = readFileSync(file, "utf8").replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  writeFileSync(file, text);
}

if (crlf.length) {
  execFileSync("git", ["add", "--", ...crlf], { stdio: "inherit" });
  console.log("[eol] 已将以下暂存文件转为 LF:\n" + crlf.join("\n"));
}
