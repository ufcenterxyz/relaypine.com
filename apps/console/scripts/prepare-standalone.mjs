import { access, cp, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const project = new URL("../", import.meta.url);
const standalone = new URL(".next/standalone/", project);
await access(new URL("server.js", standalone));

for (const relative of [".next/static", "public"]) {
  const source = new URL(relative, project);
  try {
    await access(source);
  } catch (error) {
    if (relative === "public" && error.code === "ENOENT") continue;
    throw error;
  }
  const destination = new URL(relative, standalone);
  await rm(destination, { recursive: true, force: true });
  await cp(source, destination, { recursive: true });
  console.log(`Staged ${relative} in ${fileURLToPath(standalone)}`);
}
