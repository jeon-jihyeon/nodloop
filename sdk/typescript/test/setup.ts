import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:net";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { Client } from "../src/client.ts";

const root = resolve(import.meta.dirname, "..", "..", "..");

// A home of the test process so an approval never writes a veto file under the real one
// The build keeps the real home since go finds its caches there
const realHome = process.env.HOME;
process.env.HOME = mkdtempSync(join(tmpdir(), "nodloop-home-"));
let built: string | undefined;
let builtServer: string | undefined;

// The nodloop of this checkout built once per test process
export function binary(): string {
  if (!built) {
    built = join(mkdtempSync(join(tmpdir(), "nodloop-bin-")), "nodloop");
    execFileSync("go", ["build", "-o", built, "./cmd/nodloop"], { cwd: root, env: { ...process.env, HOME: realHome } });
  }
  return built;
}

// The nodloop-server of this checkout built once per test process
function serverBinary(): string {
  if (!builtServer) {
    builtServer = join(mkdtempSync(join(tmpdir(), "nodloop-bin-")), "nodloop-server");
    execFileSync("go", ["build", "-o", builtServer, "./cmd/nodloop-server"], { cwd: join(root, "server"), env: { ...process.env, HOME: realHome } });
  }
  return builtServer;
}

// Saves the holdout share in config.json of the test home
// Only a call with a holdout key draws on it
export function holdout(share: number): void {
  mkdirSync(join(process.env.HOME!, ".nodloop"), { recursive: true });
  writeFileSync(join(process.env.HOME!, ".nodloop", "config.json"), JSON.stringify({ holdout: share }));
}

// A client over new records and the directory of those records
export async function open(): Promise<Client & { records: string }> {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  return Object.assign(await Client.open({ recordDir: records, binary: binary() }), { records });
}

// Approves a version of the records as the person ann at a terminal of their own
// An SDK call proves no person so it records a proposal
// The CLI decides only when stdin and stdout are a terminal outside an agent session
// script gives the command the pseudo terminal that stands for the person here
export function approve(records: string, id: string, version: number, ...flags: string[]): void {
  const args = [binary(), "knowledge", "approve", id, "--version", String(version), "--approver", "ann", "--record-dir", records, ...flags];
  const env: NodeJS.ProcessEnv = { ...process.env };
  delete env.CLAUDECODE;
  const quoted = args.map((a) => `'${a.replaceAll("'", `'\\''`)}'`).join(" ");
  const argv = process.platform === "darwin" ? ["-q", "/dev/null", ...args] : ["-q", "-e", "-c", quoted, "/dev/null"];
  execFileSync("script", argv, { env, stdio: ["ignore", "pipe", "pipe"] });
}

export const acme = { tenant: ["acme"] };
const noRm = { tool: "Bash", when: [{ field: "commands", match: "(?m)^rm " }], example: { command: "rm -rf /data" } };

// An item for acme and a veto on rm, both approved by the person at a terminal
export async function seed(c: Client & { records: string }): Promise<void> {
  const run = await c.record({ producer: "bot", output: "answer", labels: acme });
  await c.judge(run, "reject", "ann", { reason: "missing the window" });
  for (const [id, content, veto] of [["window", "Quote the refund window", undefined], ["no-rm", "Never run rm", noRm]] as const) {
    const p = await c.propose({ kind: "judgment", content, from: run, id, veto });
    approve(c.records, p.id, p.version);
  }
}

// The ids of the runs that wait for a verdict
export async function runs(c: Client): Promise<string[]> {
  return (await c.call("queue", { limit: 50 })).items.map((i: { trace_id: string }) => i.trace_id);
}

// A free port the kernel picked
async function freePort(): Promise<number> {
  return new Promise((done) => {
    const srv = createServer().listen(0, "127.0.0.1", () => {
      const port = (srv.address() as { port: number }).port;
      srv.close(() => done(port));
    });
  });
}

// A nodloop server with an approver key of tenant acme a person holds and an approver key of a service that proves no person
export async function server(): Promise<{ url: string; key: string; service: string; proc: ChildProcess }> {
  // A home of its own so its keys never meet those of another server
  const env = { ...process.env, HOME: mkdtempSync(join(tmpdir(), "nodloop-home-")) };
  const key = execFileSync(serverBinary(), ["key", "add", "ann", "--tenant", "acme", "--role", "approver", "--person"], { env }).toString().trim();
  const service = execFileSync(serverBinary(), ["key", "add", "ci", "--tenant", "acme", "--role", "approver"], { env }).toString().trim();
  const port = await freePort();
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  const proc = spawn(serverBinary(), ["serve", "--addr", `127.0.0.1:${port}`, "--record-dir", records], { env, stdio: "ignore" });
  for (let i = 0; i < 100; i++) {
    try {
      await fetch(`http://127.0.0.1:${port}/mcp`);
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 50));
    }
  }
  return { url: `http://127.0.0.1:${port}/mcp`, key, service, proc };
}
