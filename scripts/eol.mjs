import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import process from "node:process";

const mode = process.argv[2];
if (mode !== "--check" && mode !== "--staged-fix") {
  console.error("usage: node scripts/eol.mjs --check|--staged-fix");
  process.exit(2);
}

/** 读取 git 以 NUL 分隔的多行输出 */
function gitNul(args) {
  return execFileSync("git", args).toString("utf8").split("\0").filter(Boolean);
}

/** 用 git check-attr 查询单个文件的 text / eol 属性，判定它该用 LF 还是 CRLF */
function desiredEol(file) {
  const out = execFileSync("git", ["check-attr", "text", "eol", "--", file], {
    encoding: "utf8",
  }).trim();
  // 输出形如: <file>: text: auto / <file>: eol: lf
  const isText = /^.*: text: (auto|true|set)$/m.test(out);
  const eolMatch = out.match(/^.*: eol: (\S+)$/m);
  const eolAttr = eolMatch ? eolMatch[1] : "unset";
  if (eolAttr === "lf") return "lf";
  if (eolAttr === "crlf") return "crlf";
  // text=auto 但没显式 eol：由 core.autocrlf 决定，本机为 input/false → LF
  if (isText) return "lf";
  // 二进制文件不检查
  return null;
}

/** 缓存 desiredEol 结果，避免对同名文件重复调 git check-attr */
const eolCache = new Map();
function cachedEol(file) {
  if (eolCache.has(file)) return eolCache.get(file);
  const result = desiredEol(file);
  eolCache.set(file, result);
  return result;
}

const files =
  mode === "--staged-fix"
    ? gitNul(["diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z"])
    : gitNul(["ls-files", "-z"]);

/** 违规文件列表，每项为 { file, expected } */
const violations = [];

for (const file of files) {
  if (!existsSync(file)) continue;
  const expected = cachedEol(file);
  if (!expected) continue; // 二进制
  let buf;
  try {
    buf = readFileSync(file);
  } catch {
    continue;
  }
  if (buf.includes(0)) continue; // 含 NUL 字节，按二进制跳过

  const hasCRLF = buf.includes(0x0d);
  // 裸 LF：存在 \n 且前一字节不是 \r
  let hasBareLF = false;
  for (let i = 0; i < buf.length; i++) {
    if (buf[i] === 0x0a && (i === 0 || buf[i - 1] !== 0x0d)) {
      hasBareLF = true;
      break;
    }
  }

  if (expected === "lf" && hasCRLF) {
    violations.push({ file, expected: "lf" });
  } else if (expected === "crlf" && hasBareLF) {
    violations.push({ file, expected: "crlf" });
  }
}

if (mode === "--check") {
  if (violations.length) {
    const lfFiles = violations.filter((v) => v.expected === "lf").map((v) => v.file);
    const crlfFiles = violations.filter((v) => v.expected === "crlf").map((v) => v.file);
    const lines = [];
    if (lfFiles.length) lines.push("以下文件含 CRLF，应为 LF:\n" + lfFiles.join("\n"));
    if (crlfFiles.length) lines.push("以下文件含裸 LF，应为 CRLF:\n" + crlfFiles.join("\n"));
    console.error(lines.join("\n\n"));
    process.exit(1);
  }
  process.exit(0);
}

for (const { file, expected } of violations) {
  const raw = readFileSync(file, "utf8");
  // 先统一为 LF，再按期望行尾写回
  const unified = raw.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  const result = expected === "lf" ? unified : unified.replace(/\n/g, "\r\n");
  writeFileSync(file, result);
}

if (violations.length) {
  const allFiles = violations.map((v) => v.file);
  execFileSync("git", ["add", "--", ...allFiles], { stdio: "inherit" });
  const lfFiles = violations.filter((v) => v.expected === "lf").map((v) => v.file);
  const crlfFiles = violations.filter((v) => v.expected === "crlf").map((v) => v.file);
  const lines = [];
  if (lfFiles.length) lines.push("→ 转 LF:\n" + lfFiles.join("\n"));
  if (crlfFiles.length) lines.push("→ 转 CRLF:\n" + crlfFiles.join("\n"));
  console.log("[eol] 已修正暂存文件行尾:\n" + lines.join("\n\n"));
}
