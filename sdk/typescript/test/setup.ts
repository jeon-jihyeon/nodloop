import { execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { Client } from "../src/client.ts";

const root = resolve(import.meta.dirname, "..", "..", "..");
let built: string | undefined;

// The nodloop of this checkout built once per test process
export function binary(): string {
  if (!built) {
    built = join(mkdtempSync(join(tmpdir(), "nodloop-bin-")), "nodloop");
    execFileSync("go", ["build", "-o", built, "./cmd/nodloop"], { cwd: root });
  }
  return built;
}

export async function open(): Promise<Client> {
  return Client.open({ recordDir: join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records"), binary: binary() });
}

export const acme = { tenant: ["acme"] };
const noRm = { tool: "Bash", when: [{ field: "commands", match: "(?m)^rm " }], example: { command: "rm -rf /data" } };

// An approved item for acme and an approved veto on rm
export async function seed(c: Client): Promise<void> {
  const run = await c.record("bot", "answer", acme);
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
