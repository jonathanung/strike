// Screenshot, video, and Kiro-reference side-by-side. ffmpeg is required for
// video and for the side-by-side generator. Named PNG screenshots are raw
// CDP captures and do not need ffmpeg.

import { spawn } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { meanAbsDiff, sideBySideFilter } from "./lib.mjs";

function run(cmd, args, { allowFail = false } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: ["ignore", "pipe", "pipe"] });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (b) => stdout.push(b));
    child.stderr.on("data", (b) => stderr.push(b));
    child.on("error", reject);
    child.on("close", (code) => {
      const out = Buffer.concat(stdout);
      const err = Buffer.concat(stderr).toString();
      if (code !== 0 && !allowFail) {
        reject(new Error(`${cmd} exited ${code}\n${err.slice(-1500)}`));
        return;
      }
      resolve({ code, stdout: out, stderr: err });
    });
  });
}

export async function writeBytes(path, bytes) {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, bytes);
}

export async function encodeVideo(framesDir, outPath, fps = 6) {
  await mkdir(dirname(outPath), { recursive: true });
  await run("ffmpeg", [
    "-y",
    "-framerate",
    String(fps),
    "-i",
    `${framesDir}/frame-%05d.jpg`,
    "-c:v",
    "libx264",
    "-pix_fmt",
    "yuv420p",
    "-movflags",
    "+faststart",
    outPath,
  ]);
}

// Place a reference frame (typically a Kiro capture the caller already has)
// on the left and a Strike capture on the right. Both are scaled to the same
// height with Lanczos. This does not copy branding into the repo.
export async function sideBySide(left, right, outPath, height) {
  await mkdir(dirname(outPath), { recursive: true });
  await run("ffmpeg", [
    "-y",
    "-i",
    left,
    "-i",
    right,
    "-filter_complex",
    sideBySideFilter(height),
    outPath,
  ]);
}

export async function downscaleRaw(pngPath) {
  const { stdout } = await run("ffmpeg", [
    "-i",
    pngPath,
    "-vf",
    "scale=32:18",
    "-f",
    "rawvideo",
    "-pix_fmt",
    "rgb24",
    "pipe:1",
  ]);
  return stdout;
}

function asPath(p) {
  return p instanceof URL ? fileURLToPath(p) : p;
}

export async function compareGolden(pngPath, goldenPath, update) {
  const path = asPath(goldenPath);
  const raw = await downscaleRaw(pngPath);
  if (update) {
    await writeBytes(path, raw);
    return { updated: true, diff: 0 };
  }
  let prev;
  try {
    prev = await readFile(path);
  } catch {
    return { missing: true, diff: null };
  }
  return { missing: false, diff: meanAbsDiff(raw, prev) };
}
