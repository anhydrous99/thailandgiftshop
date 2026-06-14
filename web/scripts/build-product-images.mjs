import { mkdir, readdir, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(scriptDir, "..");
const productImagesRoot = path.join(webRoot, "product-images");

const widths = [320, 480, 720, 960, 1200];
const sourceExtensions = new Set([".jpg", ".jpeg", ".png", ".webp"]);
const derivativePattern = /-\d+w\.(jpg|webp)$/i;

async function walk(dir, visitor) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "uploads") continue;
      await walk(fullPath, visitor);
      continue;
    }
    if (entry.isFile()) {
      await visitor(fullPath, entry.name);
    }
  }
}

async function removeGeneratedDerivatives() {
  await walk(productImagesRoot, async (filePath, filename) => {
    if (derivativePattern.test(filename)) {
      await rm(filePath, { force: true });
    }
  });
}

async function sourceImages() {
  const sources = [];
  await walk(productImagesRoot, async (filePath, filename) => {
    if (derivativePattern.test(filename)) return;
    if (!sourceExtensions.has(path.extname(filename).toLowerCase())) return;
    sources.push(filePath);
  });
  return sources.sort();
}

function outputPath(sourcePath, width, extension) {
  const ext = path.extname(sourcePath);
  const basename = path.basename(sourcePath, ext);
  return path.join(path.dirname(sourcePath), `${basename}-${width}w.${extension}`);
}

async function buildDerivatives(sourcePath) {
  const metadata = await sharp(sourcePath, { failOn: "error" }).metadata();
  if (!metadata.width || !metadata.height) {
    throw new Error(`${path.relative(productImagesRoot, sourcePath)} has no readable dimensions`);
  }
  if (metadata.width < 1200 || metadata.height < 900) {
    throw new Error(`${path.relative(productImagesRoot, sourcePath)} must be at least 1200x900 for responsive product variants`);
  }

  await mkdir(path.dirname(sourcePath), { recursive: true });
  for (const width of widths) {
    const height = Math.round(width * 0.75);
    const base = sharp(sourcePath, { failOn: "error" })
      .rotate()
      .resize({ width, height, fit: "cover", position: "centre" });

    await Promise.all([
      base.clone().webp({ quality: 82, effort: 4 }).toFile(outputPath(sourcePath, width, "webp")),
      base.clone().jpeg({ quality: 84, progressive: true, mozjpeg: true }).toFile(outputPath(sourcePath, width, "jpg")),
    ]);
  }
}

await removeGeneratedDerivatives();
const sources = await sourceImages();
await Promise.all(sources.map(buildDerivatives));

console.log(`generated responsive variants for ${sources.length} product images`);
