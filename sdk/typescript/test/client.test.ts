import assert from "node:assert/strict";
import { test } from "node:test";
import { ToolError } from "../src/client.ts";
import { acme, open, runs, seed } from "./setup.ts";

test("a corrected run teaches an item the same tenant receives and another does not", async () => {
  const c = await open();
  try {
    const run = await c.record("support-bot", "Here are the refund steps", acme);
    await c.judge(run, "reject", { reason: "the refund window was missing", reasonCode: "scope" });
    const p = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run, id: "refund-window" });
    await c.approve(p.id, p.version, "ann");

    const mine = await c.knowledge("support-bot", acme);
    const other = await c.knowledge("support-bot", { tenant: ["globex"] });
    const next = await c.record("support-bot", "The window is 30 days", acme, mine.items);

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
    const run = await c.record("bot", "answer", acme);
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
