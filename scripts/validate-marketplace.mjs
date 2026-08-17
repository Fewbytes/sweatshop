import { readFile } from "node:fs/promises";
import { access } from "node:fs/promises";

const marketplace = JSON.parse(await readFile(".claude-plugin/marketplace.json", "utf8"));
if (!Array.isArray(marketplace.plugins) || marketplace.plugins.length === 0) throw new Error("marketplace has no plugins");
for (const plugin of marketplace.plugins) {
  if (!plugin.name || !plugin.source) throw new Error("plugin requires name and source");
  const manifest = `${plugin.source}/.claude-plugin/plugin.json`;
  await access(manifest);
  const packageJSON = `${plugin.source}/package.json`;
  await access(packageJSON);
}
console.log(`validated ${marketplace.plugins.length} marketplace plugin(s)`);
