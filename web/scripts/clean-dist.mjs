// Empties the embedded dist directory but keeps its .gitkeep (an emptyOutDir build would delete it).
import { mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = fileURLToPath(new URL("../../internal/webui/dist", import.meta.url));
mkdirSync(dist, { recursive: true });
for (const entry of readdirSync(dist)) {
  if (entry !== ".gitkeep") rmSync(join(dist, entry), { recursive: true, force: true });
}
writeFileSync(join(dist, ".gitkeep"), "");
