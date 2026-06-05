import { copyFile, mkdir, stat } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const webDirectory = join(scriptDirectory, "..");
const sourcePath = join(webDirectory, "node_modules", "htmx.org", "dist", "htmx.min.js");
const outputPath = join(webDirectory, "static", "vendor", "htmx.min.js");

async function assertReadableSource() {
  try {
    const sourceStats = await stat(sourcePath);
    if (!sourceStats.isFile()) {
      throw new Error("source is not a file");
    }
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error);
    throw new Error(`HTMX source file is missing or unreadable: ${sourcePath} (${detail})`);
  }
}

async function assertNonEmptyOutput() {
  const outputStats = await stat(outputPath);
  if (!outputStats.isFile() || outputStats.size === 0) {
    throw new Error(`HTMX vendor output is empty or invalid: ${outputPath}`);
  }
}

try {
  await assertReadableSource();
  await mkdir(dirname(outputPath), { recursive: true });
  await copyFile(sourcePath, outputPath);
  await assertNonEmptyOutput();
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  console.error(`Failed to copy HTMX vendor asset: ${message}`);
  process.exitCode = 1;
}
