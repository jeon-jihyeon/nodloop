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
export async function open(): Promise<Client & { recordDir: string }> {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  return Object.assign(await Client.open({ recordDir: records, binary: binary() }), { recordDir: records });
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
export async function seed(c: Client & { recordDir: string }): Promise<void> {
  const run = await c.record({ producer: "bot", output: "answer", labels: acme });
  await c.judge(run, "reject", "ann", { reason: "missing the window" });
  for (const [id, content, veto] of [["window", "Quote the refund window", undefined], ["no-rm", "Never run rm", noRm]] as const) {
    const p = await c.propose({ kind: "judgment", content, from: run, id, veto });
    approve(c.recordDir, p.id, p.version);
  }
}

export const first = "019a0c8fcb9e00000001";
export const second = "019a0c8fcb9e00000002";

// Two runs and a verdict recorded long before the test so every page holds them, each with text a page leaves out
export function seedRecords(records: string): void {
  mkdirSync(records, { recursive: true });
  const usage = { input_tokens: 0, output_tokens: 0, cache_read: 0, cache_create: 0, cost_usd: 0 };
  const run = (id: string, time: string, output: string) =>
    JSON.stringify({ id, name: "run", time, input: { applied: [] }, output, usage, duration_ms: 0, producer: "support-bot", schema_version: 1 });
  writeFileSync(join(records, "traces.jsonl"), `${run(first, "2026-10-01T09:00:00Z", "refund steps")}\n${run(second, "2026-10-01T09:00:01Z", "the window is 30 days")}\n`);
  const verdict = { trace_id: first, time: "2026-10-01T09:00:02Z", verdict: "reject", reason: "the window was missing", reviewer: "ann", proof: "none", schema_version: 1 };
  writeFileSync(join(records, "feedback.jsonl"), `${JSON.stringify(verdict)}\n`);
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
// records is the record directory it keeps the tenants under
export async function server(): Promise<{ url: string; key: string; service: string; proc: ChildProcess; records: string }> {
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
  return { url: `http://127.0.0.1:${port}/mcp`, key, service, proc, records };
}
