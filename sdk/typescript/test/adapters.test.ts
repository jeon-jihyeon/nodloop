import assert from "node:assert/strict";
import { test } from "node:test";
import { NodloopHooks as ClaudeHooks } from "../src/claude-agent.ts";
import { NodloopHooks as OpenAIHooks, vetoGuardrail } from "../src/openai-agents.ts";
import { acme, open, runs, seed } from "./setup.ts";

const signal = { signal: new AbortController().signal };

for (const [command, decision] of [["rm -rf /x", "deny"], ["ls", undefined]] as const) {
  test(`claude agent hooks add the items, record the answer and give ${command} ${decision ?? "no decision"}`, async () => {
    const c = await open();
    try {
      await seed(c);
      const hooks = new ClaudeHooks(c, "bot", acme);

      const prompt: any = await hooks.prompt({ hook_event_name: "UserPromptSubmit", prompt: "refund?" } as any, undefined, signal);
      const pre: any = await hooks.preToolUse({ hook_event_name: "PreToolUse", tool_name: "Bash", tool_input: { command } } as any, "t1", signal);
      await hooks.stop({ hook_event_name: "Stop", last_assistant_message: "The window is 30 days" } as any, undefined, signal);

      assert.match(prompt.hookSpecificOutput.additionalContext, /\[window v1 judgment\]/);
      assert.equal(pre.hookSpecificOutput?.permissionDecision, decision);
      assert.ok((await runs(c)).includes(hooks.run));
      assert.deepEqual(Object.keys(hooks.hooks()).sort(), ["PreToolUse", "Stop", "UserPromptSubmit"]);
    } finally {
      await c.close();
    }
  });
}

test("openai agents instructions carry the items and the guardrail rejects a vetoed call", async () => {
  const c = await open();
  try {
    await seed(c);
    const hooks = new OpenAIHooks(c, "bot", acme);
    const guard = vetoGuardrail(c);

    const instructions = await hooks.instructions("You help with refunds")();
    const run = await hooks.record("The window is 30 days");
    const blocked = await guard.run({ toolCall: { name: "Bash", arguments: '{"command":"rm -rf /x"}' } } as any);
    const allowed = await guard.run({ toolCall: { name: "Bash", arguments: '{"command":"ls"}' } } as any);

    assert.match(instructions, /^You help with refunds\n\nnodloop: corrections/);
    assert.equal(blocked.behavior.type, "rejectContent");
    assert.equal(allowed.behavior.type, "allow");
    assert.ok((await runs(c)).includes(run));
  } finally {
    await c.close();
  }
});
