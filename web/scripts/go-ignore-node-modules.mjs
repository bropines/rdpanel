// Some npm packages ship .go files (e.g. flatted). Because web/ lives inside the
// Go module, `go test ./...` would otherwise try to build them. A go.mod inside
// node_modules turns it into a separate (ignored) module.
import { existsSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const nodeModules = join(dirname(fileURLToPath(import.meta.url)), "..", "node_modules");
if (existsSync(nodeModules)) {
  writeFileSync(join(nodeModules, "go.mod"), "module ignore_node_modules\n");
}
