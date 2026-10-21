import assert from "node:assert/strict";
import { test } from "node:test";
import { ToolError } from "../src/client.ts";
import { Client } from "../src/client.ts";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { acme, approve, binary, first, holdout, open, runs, second, seed, seedRecords, server } from "./setup.ts";

test("a corrected run teaches an item the same tenant receives once the person approved it and another tenant never does", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "Here are the refund steps", labels: acme });
    await c.judge(run, "reject", { reviewer: "ann", reason: "the refund window was missing", reasonCode: "scope" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run, id: "refund-window" });
    const proposal = await c.approve(p.id, p.version, "ann");
    const before = await c.knowledge("support-bot", acme);
    approve(c.recordDir, p.id, p.version);

    const mine = await c.knowledge("support-bot", acme);
    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    const next = await c.record({ producer: "support-bot", output: "The window is 30 days", labels: acme, applied: mine.items });

    assert.deepEqual([proposal.status, proposal.approver, proposal.proposal !== ""], ["candidate", "", true]);
    assert.deepEqual(before.items, []);
    assert.deepEqual(mine.items.map((i) => i.id), ["refund-window"]);
    assert.match(mine.text, /^nodloop: corrections for work in this place that a person approved or nodloop carried out/);
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

test("a verdict without a reviewer is refused over stdio, where no key names one", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "bot", output: "answer", labels: acme });
    const required = (e: unknown) => e instanceof ToolError && /a reviewer is required/.test(e.message);
    await assert.rejects(c.judge(run, "approve", { reviewer: "" }), required);
    // A call of an earlier release whose options name no reviewer
    await assert.rejects(c.judge(run, "reject", { reason: "the window was missing" } as never), required);
    const written = existsSync(join(c.recordDir, "feedback.jsonl")) && readFileSync(join(c.recordDir, "feedback.jsonl"), "utf8") !== "";
    assert.equal(written, false);
  } finally {
    await c.close();
  }
});

test("nodloop is reserved for the capture of a session, so a verdict or an outcome under it is refused and nothing is recorded", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "bot", output: "answer", labels: acme });
    const reserved = (e: unknown) => e instanceof ToolError && /reserved for what the capture reads/.test(e.message);
    await assert.rejects(c.judge(run, "reject", { reviewer: "nodloop", reason: "wrong" }), reserved);
    await assert.rejects(c.outcome(run, "confirmed", { reviewer: "nodloop" }), reserved);
    const written = ["feedback.jsonl", "outcomes.jsonl"].filter((f) => existsSync(join(c.recordDir, f)) && readFileSync(join(c.recordDir, f), "utf8") !== "");
    assert.deepEqual(written, []);
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

test("a server key a person holds approves under its own name and a key of a service only proposes", async () => {
  const { url, key, service, proc } = await server();
  const bot = await Client.open({ url, key: service });
  const c = await Client.open({ url, key });
  try {
    const run = await bot.record({ producer: "support-bot", output: "steps", labels: acme });
    await bot.judge(run, "reject", { reviewer: "ci", reason: "the window was missing" });
    const p = await bot.propose({ kind: "judgment", content: "Quote the refund window", from: run, id: "window" });
    const proposal = await bot.approve(p.id, p.version, "mallory");
    const approved = await c.approve(p.id, p.version, "mallory");
    const mine = await c.knowledge("support-bot", acme);
    await assert.rejects(c.judge(run, "approve", { reviewer: "nodloop" }), (e) => e instanceof ToolError && /reserved for what the capture reads/.test(e.message));

    assert.deepEqual([p.id, p.status], ["window", "candidate"]);
    assert.deepEqual([proposal.status, proposal.proposal !== ""], ["candidate", true]);
    assert.deepEqual([approved.id, approved.version, approved.status, approved.approver, approved.proposal], ["window", 1, "approved", "ann", ""]);
    assert.deepEqual(mine.items.map((i) => i.id), ["window"]);
  } finally {
    await bot.close();
    await c.close();
    proc.kill();
  }
});

test("an intervention under a key a person holds decides at once, its reverse takes it back and a key of a service only proposes", async () => {
  const { url, key, service, proc } = await server();
  const bot = await Client.open({ url, key: service });
  const c = await Client.open({ url, key });
  try {
    const run = await bot.record({ producer: "support-bot", output: "steps", labels: acme });
    await bot.judge(run, "reject", { reviewer: "ci", reason: "the window was missing" });
    const p = await bot.propose({ kind: "judgment", content: "Quote the refund window", from: run, id: "window" });
    const proposal = await bot.intervene({ action: "judge", target: `${p.id}:${p.version}`, choice: "keep" });
    const kept = await c.intervene({ action: "judge", target: `${p.id}:${p.version}`, choice: "keep" });
    const received = await c.knowledge("support-bot", acme);
    const back = await c.intervene({ action: "reverse", target: kept.intervention?.id });
    const after = await c.knowledge("support-bot", acme);

    assert.deepEqual([proposal.authority, proposal.records], ["agent", []]);
    assert.deepEqual([kept.authority, kept.proof, kept.records], ["person", "key", [{ id: "window", version: 1, status: "approved" }]]);
    assert.equal(kept.intervention?.record_kind, "intervention");
    assert.deepEqual(received.items.map((i) => i.id), ["window"]);
    assert.deepEqual(back.records.map((r) => r.status), ["retired", "candidate"]);
    assert.deepEqual(after.items, []);
  } finally {
    await bot.close();
    await c.close();
    proc.kill();
  }
});

test("an intervention over stdio records a proposal, a digest answers its cursor and a knob outside its range is a ToolError", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "steps", labels: acme });
    await c.judge(run, "reject", { reviewer: "ann", reason: "the window was missing" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window", from: run, id: "window" });
    const proposal = await c.intervene({ action: "judge", target: `${p.id}:${p.version}`, choice: "keep", session: "s-1" });
    const digest = await c.intervene({ action: "digest", target: "support-bot", labels: acme });
    await assert.rejects(c.intervene({ action: "knob", target: "audit_tier1", value: 0.9 }), ToolError);

    assert.deepEqual([proposal.authority, proposal.proof, proposal.records, proposal.declined], ["agent", "none", [], undefined]);
    assert.equal(proposal.intervention?.session_id !== undefined, true);
    assert.deepEqual([digest.authority, digest.since ?? ""], ["agent", ""]);
    assert.deepEqual((await c.knowledge("support-bot", acme)).items, []);
  } finally {
    await c.close();
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

test("an eleventh item for the same runs under the run chars approves", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "steps", labels: acme });
    await c.judge(run, "reject", { reviewer: "ann", reason: "the window was missing" });
    for (let n = 0; n < 11; n++) {
      const p = await c.propose({ kind: "judgment", content: `Refund rule number ${n}`, from: run, id: `rule-${n}` });
      approve(c.recordDir, p.id, p.version);
    }
    const mine = await c.knowledge("support-bot", acme);
    assert.equal(mine.items.length, 11);
  } finally {
    await c.close();
  }
});

test("approve proposes the labels the person picked and the person approves them at the terminal", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "Here are the refund steps", labels: acme });
    await c.record({ producer: "support-bot", output: "ok", labels: { tenant: ["globex"] } });
    await c.judge(run, "reject", { reviewer: "ann", reason: "too long", reasonCode: "form" });
    const p = await c.propose({ kind: "judgment", content: "Answer in three lines", from: run });

    const proposal = await c.approve(p.id, p.version, "ann", { labels: {} });
    await assert.rejects(c.approve(p.id, p.version, "ann", { labels: { tenant: ["initech"] } }), ToolError);
    approve(c.recordDir, p.id, p.version, "--everywhere");

    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    assert.deepEqual([proposal.status, proposal.proposal !== ""], ["candidate", true]);
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
    await c.judge(run, "reject", { reviewer: "ann", reason: "name the partition" });
    const p = await c.propose({ kind: "judgment", content: "Name the partition", from: run, id: "partition" });
    approve(c.recordDir, p.id, p.version);
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
    await c.judge(run, "reject", { reviewer: "ann", reason: "name the partition" });
    const p = await c.propose({ kind: "judgment", content: "Name the partition", from: run, id: "partition" });
    approve(records, p.id, p.version);
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

test("the model of a run lands in its trace and a run without one records none", async () => {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  const c = await Client.open({ recordDir: records, binary: binary() });
  try {
    const named = await c.record({ producer: "stream-bot", output: "lag rose", model: "gpt-4o-mini" });
    const bare = await c.record({ producer: "stream-bot", output: "lag rose again" });

    const traces = readFileSync(join(records, "traces.jsonl"), "utf8").split("\n").filter((l) => l.trim()).map((l) => JSON.parse(l));
    assert.equal(traces.find((t) => t.id === named).model, "gpt-4o-mini");
    assert.equal(traces.find((t) => t.id === bare).model, undefined);
  } finally {
    await c.close();
  }
});

test("the duration of a run lands in its trace so its span has a length", async () => {
  const records = join(mkdtempSync(join(tmpdir(), "nodloop-rec-")), "records");
  const c = await Client.open({ recordDir: records, binary: binary() });
  try {
    const timed = await c.record({ producer: "stream-bot", output: "lag rose", durationMs: 1500.4 });
    const bare = await c.record({ producer: "stream-bot", output: "lag rose again" });

    const traces = readFileSync(join(records, "traces.jsonl"), "utf8").split("\n").filter((l) => l.trim()).map((l) => JSON.parse(l));
    assert.equal(traces.find((t) => t.id === timed).duration_ms, 1500);
    assert.equal(traces.find((t) => t.id === bare).duration_ms, 0);
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
    await c.outcome(run, "confirmed", { confirmedCause: "partition 7 was hot", note: "checked the broker", reviewer: "ann" });
    const loop = await c.report("loop");
    await assert.rejects(c.outcome(run, "refuted", { confirmedCause: "partition 7 was hot", reviewer: "ann" }), ToolError);
    await assert.rejects(c.outcome(run, "refuted"), (e) => e instanceof ToolError && /a reviewer is required/.test(e.message));

    assert.equal(loop.report.totals.runs, 1);
    assert.equal(loop.since, "2020-01-01T00:00:00Z");
  } finally {
    await c.close();
  }
});

test("records page across a cursor in the order their store appended them, in their published schema, text left out unless asked", async () => {
  const c = await open();
  seedRecords(c.recordDir);
  try {
    const one = await c.records("run", { limit: 1 });
    const two = await c.records("run", { since: one.next });
    const end = await c.records("run", { since: two.next });
    const shown = await c.records("run", { limit: 1, content: true });
    const verdicts = await c.records("verdict");

    assert.deepEqual(one.records.map((r) => r.record_id), [first]);
    assert.deepEqual(two.records.map((r) => r.record_id), [second]);
    assert.deepEqual(end, { records: [], next: two.next });
    assert.equal(one.records[0].output, undefined);
    assert.equal(shown.records[0].output, "refund steps");
    assert.deepEqual(verdicts.records.map((v) => [v.record_kind, v.trace_id, v.reason]), [["verdict", first, undefined]]);
    await assert.rejects(c.records("run", { since: "%%%" }), (e) => e instanceof ToolError && /the cursor names no record/.test(e.message));
  } finally {
    await c.close();
  }
});

test("a key of the server pages the records of its tenant", async () => {
  const s = await server();
  seedRecords(join(s.records, "tenants", "acme"));
  const c = await Client.open({ url: s.url, key: s.key });
  try {
    const page = await c.records("run", { content: true });

    assert.deepEqual(page.records.map((r) => [r.record_id, r.output]), [[first, "refund steps"], [second, "the window is 30 days"]]);
  } finally {
    await c.close();
    s.proc.kill();
  }
});
