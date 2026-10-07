import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:net";
import { mkdtempSync } from "node:fs";
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

export async function open(): Promise<Client> {
  return Client.open({ recordDir: join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records"), binary: binary() });
}

export const acme = { tenant: ["acme"] };
const noRm = { tool: "Bash", when: [{ field: "commands", match: "(?m)^rm " }], example: { command: "rm -rf /data" } };

// An approved item for acme and an approved veto on rm
export async function seed(c: Client): Promise<void> {
  const run = await c.record({ producer: "bot", output: "answer", labels: acme });
  await c.judge(run, "reject", { reason: "missing the window" });
  for (const [id, content, veto] of [["window", "Quote the refund window", undefined], ["no-rm", "Never run rm", noRm]] as const) {
    const p = await c.propose({ kind: "judgment", content, from: run, id, veto });
    await c.approve(p.id, p.version, "ann");
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

// A nodloop server with an approver key of tenant acme
export async function server(): Promise<{ url: string; key: string; proc: ChildProcess }> {
  // A home of its own so its keys never meet those of another server
  const env = { ...process.env, HOME: mkdtempSync(join(tmpdir(), "nodloop-home-")) };
  const key = execFileSync(serverBinary(), ["key", "add", "ann", "--tenant", "acme", "--role", "approver"], { env }).toString().trim();
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
  return { url: `http://127.0.0.1:${port}/mcp`, key, proc };
}
