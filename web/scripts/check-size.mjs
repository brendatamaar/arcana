// Enforces the PRD §8.2 budget: the JavaScript needed to start the dashboard must be at most
// 200 KB gzipped. "Initial" means the entry script plus every chunk it statically imports.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const BUDGET = 200 * 1024;
const dist = fileURLToPath(new URL("../../internal/webui/dist", import.meta.url));
const index = readFileSync(join(dist, "index.html"), "utf8");
const queue = [...index.matchAll(/(?:src|href)="\/ui\/(assets\/[^"]+\.js)"/g)].map((m) => m[1]);
if (queue.length === 0)
  throw new Error("index.html references no scripts; run `pnpm run build` first");
const seen = new Set();
let total = 0;
while (queue.length > 0) {
  const file = queue.pop();
  if (seen.has(file)) continue;
  seen.add(file);
  const code = readFileSync(join(dist, file), "utf8");
  const size = gzipSync(code).length;
  total += size;
  console.log(`${file}: ${(size / 1024).toFixed(1)} KB gzip`);
  // Static imports only: dynamic import() chunks are not part of the initial load.
  for (const m of code.matchAll(/(?:^|[^.\w])(?:import|export)[^"'`;]*?from\s*"\.\/([^"]+\.js)"/g))
    queue.push(`assets/${m[1]}`);
  for (const m of code.matchAll(/(?:^|[;}])import"\.\/([^"]+\.js)"/g)) queue.push(`assets/${m[1]}`);
}
const unused = readdirSync(join(dist, "assets")).filter(
  (f) => f.endsWith(".js") && !seen.has(`assets/${f}`),
);
console.log(
  `initial JavaScript: ${(total / 1024).toFixed(1)} KB gzip (budget ${BUDGET / 1024} KB)${unused.length ? `; lazy chunks: ${unused.join(", ")}` : ""}`,
);
if (total > BUDGET) {
  console.error("initial JavaScript exceeds the 200 KB gzip budget");
  process.exit(1);
}
