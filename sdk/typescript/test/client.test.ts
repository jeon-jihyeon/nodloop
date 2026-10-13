import assert from "node:assert/strict";
import { test } from "node:test";
import { ToolError } from "../src/client.ts";
import { Client } from "../src/client.ts";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { acme, binary, holdout, open, runs, seed, server } from "./setup.ts";

test("a corrected run teaches an item the same tenant receives and another does not", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "Here are the refund steps", labels: acme });
    await c.judge(run, "reject", { reason: "the refund window was missing", reasonCode: "scope" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run, id: "refund-window" });
    const approved = await c.approve(p.id, p.version, "ann");

    const mine = await c.knowledge("support-bot", acme);
    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    const next = await c.record({ producer: "support-bot", output: "The window is 30 days", labels: acme, applied: mine.items });

    assert.deepEqual(approved, { id: "refund-window", version: 1, status: "approved", approver: "ann", veto: false, compactionDue: false, exportError: "", folderError: "" });
    assert.deepEqual(mine.items.map((i) => i.id), ["refund-window"]);
    assert.match(mine.text, /^nodloop: corrections a person approved/);
    assert.deepEqual(other, { items: [], text: "", withheld: [] });
    assert.ok((await runs(c)).includes(next));
  } finally {
    await c.close();
  }
});

test("a refusal is a ToolError", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "bot", output: "answer", labels: acme });
    await assert.rejects(c.propose({ kind: "judgment", content: "x", from: run }), (e) => e instanceof ToolError && /edit or a reject/.test(e.message));
  } finally {
    await c.close();
  }
});

for (const [command, action] of [["cd / && rm -rf /x", "block"], ["ls", "allow"]] as const) {
  test(`check call ${command} is ${action}`, async () => {
    const c = await open();
    try {
      await seed(c);
      const decision = await c.checkCall("Bash", { command });
      assert.equal(decision.action, action);
    } finally {
      await c.close();
    }
  });
}

test("a server key works the same and approves under its own name", async () => {
  const { url, key, proc } = await server();
  const c = await Client.open({ url, key });
  try {
    const run = await c.record({ producer: "support-bot", output: "steps", labels: acme });
    await c.judge(run, "reject", { reason: "the window was missing" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window", from: run, id: "window" });
    const approved = await c.approve(p.id, p.version, "mallory");
    const mine = await c.knowledge("support-bot", acme);

    assert.deepEqual([p.id, p.status], ["window", "candidate"]);
    assert.deepEqual([approved.id, approved.version, approved.status, approved.approver], ["window", 1, "approved", "ann"]);
    assert.deepEqual(mine.items.map((i) => i.id), ["window"]);
  } finally {
    await c.close();
    proc.kill();
  }
});

test("a wrong server key is refused", async () => {
  const { url, proc } = await server();
  try {
    await assert.rejects(Client.open({ url, key: "nl_wrong" }));
  } finally {
    proc.kill();
  }
});

test("the approval of a sixth item for the same runs says a compaction is due", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "steps", labels: acme });
    await c.judge(run, "reject", { reason: "the window was missing" });
    const due: boolean[] = [];
    for (let n = 0; n < 6; n++) {
      const p = await c.propose({ kind: "judgment", content: `Refund rule number ${n}`, from: run, id: `rule-${n}` });
      due.push((await c.approve(p.id, p.version, "ann")).compactionDue);
    }
    assert.deepEqual(due, [false, false, false, false, false, true]);
  } finally {
    await c.close();
  }
});

test("approve takes the labels the person picked", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "Here are the refund steps", labels: acme });
    await c.record({ producer: "support-bot", output: "ok", labels: { tenant: ["globex"] } });
    await c.judge(run, "reject", { reason: "too long", reasonCode: "form" });
    const p = await c.propose({ kind: "judgment", content: "Answer in three lines", from: run });

    const approved = await c.approve(p.id, p.version, "ann", { labels: {} });

    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    assert.equal(approved.status, "approved");
    assert.deepEqual(other.items.map((i) => i.id), [p.id]);
  } finally {
    await c.close();
  }
});

test("a holdout key withholds a share of runs and a withheld run joins the withheld arm of report effect", async () => {
  holdout(0.5);
  const c = await open();
  try {
    const run = await c.record({ producer: "stream-bot", output: "lag rose", labels: acme });
    await c.judge(run, "reject", { reason: "name the partition" });
    const p = await c.propose({ kind: "judgment", content: "Name the partition", from: run, id: "partition" });
    await c.approve(p.id, p.version, "ann");
    const drawn = [];
    for (let n = 0; n < 200; n++) drawn.push(await c.knowledge("stream-bot", acme, `window-${n}`));
    const unkeyed = await c.knowledge("stream-bot", acme);
    const n = drawn.findIndex((k) => k.withheld.length > 0);
    const withheld = drawn[n];
    await c.record({ producer: "stream-bot", output: "lag rose again", labels: acme, withheld: withheld.withheld, holdoutKey: `window-${n}` });
    const effect = (await c.call("report", { name: "effect" })).report;

    const count = drawn.filter((k) => k.withheld.length > 0).length;
    assert.ok(count >= 60 && count <= 140, `${count} of 200 drawn`);
    assert.ok(drawn.filter((k) => k.withheld.length > 0).every((k) => k.items.length === 0 && k.text === ""));
    assert.deepEqual(withheld.withheld, [{ id: "partition", version: 1 }]);
    assert.deepEqual(unkeyed.withheld, []);
    const row = effect.find((r: { producer: string; arm: string }) => r.producer === "stream-bot" && r.arm === "withheld");
    assert.deepEqual([row.runs, row.judged, row.undrawn], [1, 0, 0]);
  } finally {
    holdout(0);
    await c.close();
  }
});

test("items passed as withheld and refs passed as applied are recorded by id and version", async () => {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  const c = await Client.open({ recordDir: records, binary: binary() });
  try {
    const run = await c.record({ producer: "stream-bot", output: "lag rose", labels: acme });
    await c.judge(run, "reject", { reason: "name the partition" });
    const p = await c.propose({ kind: "judgment", content: "Name the partition", from: run, id: "partition" });
    await c.approve(p.id, p.version, "ann");
    const { items } = await c.knowledge("stream-bot", acme);

    const recorded = await c.record({ producer: "stream-bot", output: "lag rose again", labels: acme, withheld: items });
    const byRef = await c.record({ producer: "stream-bot", output: "lag rose once more", labels: acme, applied: [{ id: "partition", version: 1 }] });
    const { report: effect } = await c.report("effect");

    const traces = readFileSync(join(records, "traces.jsonl"), "utf8").split("\n").filter((l) => l.trim()).map((l) => JSON.parse(l));
    const input = traces.find((t) => t.id === recorded).input;
    assert.deepEqual(input.applied, []);
    assert.deepEqual(input.withheld, [{ id: "partition", version: 1 }]);
    assert.deepEqual(traces.find((t) => t.id === byRef).input.applied, [{ id: "partition", version: 1 }]);
    // Recorded without a holdout key the run counts in no arm and only as undrawn
    const rows = effect.filter((r: { producer?: string }) => r.producer === "stream-bot");
    assert.deepEqual(rows.map((r: { runs: number; undrawn: number }) => [r.runs, r.undrawn]), [[0, 2], [0, 2]]);
  } finally {
    await c.close();
  }
});

test("an outcome is recorded on a run and a report reads by name with its window", async () => {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  // A prune another surface ran sets the window every report reads
  mkdirSync(records);
  writeFileSync(join(records, "prune.json"), '{"at":"2020-01-02T00:00:00Z","cutoff":"2020-01-01T00:00:00Z"}');
  const c = await Client.open({ recordDir: records, binary: binary() });
  try {
    const run = await c.record({ producer: "stream-bot", output: "lag comes from partition 7", labels: acme });
    await c.outcome(run, "confirmed", { confirmedCause: "partition 7 was hot", note: "checked the broker" });
    const loop = await c.report("loop");
    await assert.rejects(c.outcome(run, "refuted", { confirmedCause: "partition 7 was hot" }), ToolError);

    assert.equal(loop.report.totals.runs, 1);
    assert.equal(loop.since, "2020-01-01T00:00:00Z");
  } finally {
    await c.close();
  }
});
