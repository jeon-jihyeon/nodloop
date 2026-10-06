// Finds the nodloop binary the client runs and fetches the release one when there is none

import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import { chmod, mkdir, mkdtemp, rename, rm, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { execFileSync } from "node:child_process";

const repo = "jeon-jihyeon/nodloop";

const homeBin = (): string => join(homedir(), ".nodloop", "bin");

// The first of
// 1. NODLOOP_BIN
// 2. the stable link the Claude Code plugin keeps under ~/.nodloop/bin
// 3. nodloop on PATH
// 4. the release of the version fetched once into ~/.nodloop/bin/v<version> and checked against its checksums
export async function find(version: string): Promise<string> {
  const env = process.env.NODLOOP_BIN;
  if (env) return env;
  const stable = join(homeBin(), "nodloop");
  if (existsSync(stable)) return stable;
  for (const dir of (process.env.PATH ?? "").split(delimiter)) {
    const candidate = join(dir, "nodloop");
    if (dir && existsSync(candidate)) return candidate;
  }
  return fetchRelease(version);
}

function archiveName(): string {
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch as "x64" | "arm64"];
  if (!arch) throw new Error(`no nodloop release for ${process.arch}`);
  return `nodloop_${process.platform}_${arch}.tar.gz`;
}

// The release binary of the version, refused when its archive does not match checksums.txt
export async function fetchRelease(version: string): Promise<string> {
  const target = join(homeBin(), `v${version}`, "nodloop");
  if (existsSync(target)) return target;
  const base = `https://github.com/${repo}/releases/download/v${version}`;
  const name = archiveName();
  const archive = Buffer.from(await (await fetch(`${base}/${name}`)).arrayBuffer());
  const sums = await (await fetch(`${base}/checksums.txt`)).text();
  const want = sums
    .split("\n")
    .map((line) => line.trim().split(/\s+/))
    .find((parts) => parts[1] === name)?.[0];
  if (!want || createHash("sha256").update(archive).digest("hex") !== want) {
    throw new Error(`checksum of ${base}/${name} does not match checksums.txt`);
  }
  const dir = await mkdtemp(join(tmpdir(), "nodloop-"));
  try {
    await writeFile(join(dir, name), archive);
    execFileSync("tar", ["-xzf", join(dir, name), "-C", dir, "nodloop"]);
    await mkdir(join(homeBin(), `v${version}`), { recursive: true });
    await chmod(join(dir, "nodloop"), 0o755);
    await rename(join(dir, "nodloop"), target);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  return target;
}
