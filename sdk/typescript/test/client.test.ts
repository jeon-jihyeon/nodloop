import assert from "node:assert/strict";
import { test } from "node:test";
import { ToolError } from "../src/client.ts";
import { Client } from "../src/client.ts";
import { acme, open, runs, seed, server } from "./setup.ts";

test("a corrected run teaches an item the same tenant receives and another does not", async () => {
  const c = await open();
  try {
    const run = await c.record({ producer: "support-bot", output: "Here are the refund steps", labels: acme });
    await c.judge(run, "reject", { reason: "the refund window was missing", reasonCode: "scope" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run, id: "refund-window" });
    await c.approve(p.id, p.version, "ann");

    const mine = await c.knowledge("support-bot", acme);
    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    const next = await c.record({ producer: "support-bot", output: "The window is 30 days", labels: acme, applied: mine.items });

    assert.deepEqual(mine.items.map((i) => i.id), ["refund-window"]);
    assert.match(mine.text, /^nodloop: corrections a person approved/);
    assert.deepEqual(other, { items: [], text: "" });
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
