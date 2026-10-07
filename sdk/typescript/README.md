# nodloop for TypeScript

Record what your agent answered, take a person's verdict on it, and hand the corrections they approve to the agent's next run in the same situation. This is the TypeScript client of [nodloop](https://github.com/jeon-jihyeon/nodloop).

```
npm install nodloop
```

The client starts a local `nodloop mcp` over stdio, so no server runs. It uses `NODLOOP_BIN`, then the binary the Claude Code plugin keeps under `~/.nodloop/bin`, then `nodloop` on PATH, and otherwise downloads the release of its own version from GitHub and checks it against the release checksums. Tested on Node 24 on macOS and Linux.

## The loop

```ts
import { Client } from "nodloop";

const acme = { tenant: ["acme"], task: ["refund"] };
const c = await Client.open();

const knowledge = await c.knowledge("support-bot", acme);
const answer = await myAgent(`${systemPrompt}\n\n${knowledge.text}`); // empty text when nothing is approved
const run = await c.record({ producer: "support-bot", output: answer, labels: acme, applied: knowledge.items });

// later, when a person reads the answer
await c.judge(run, "reject", { reason: "the refund window was missing", reasonCode: "scope" });
const candidate = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run });
const approval = await c.approve(candidate.id, candidate.version, "ann");
await c.close();
```

From then on every `knowledge("support-bot", acme)` carries the item, and a run of another tenant never does. `judge` takes `approve`, `edit` with `edited` holding the corrected output, `reject` with what was wrong, or `withdraw` to take back the verdict before it.

| Method | Returns |
|---|---|
| `knowledge(producer, labels)` | `Knowledge` with `items` and the `text` to put in the prompt |
| `record({ producer, output, labels, applied, subject })` | the run id a verdict cites |
| `judge(run, verdict, { reason, reasonCode, edited, reviewer })` | nothing |
| `propose({ kind, content, from, producer, labels, newLabels, id, veto })` | `Candidate` with `id`, `version`, `status` and `overlaps` |
| `approve(id, version, approver)` | `Approval` with `id`, `version`, `status` and `approver` |
| `checkCall(tool, input, producer)` | `Decision` with `action` of allow, block or ask, `veto` and `reason` |

A tool that refuses, such as a proposal from a run nobody corrected, throws `ToolError` with the reason.

## Adapters

Each adapter is its own entry point, and the framework it wraps is an optional peer dependency.

LangChain `createAgent` middleware puts the items in the system message, records the final answer and refuses a vetoed tool call:

```ts
import { NodloopMiddleware } from "nodloop/langchain";

const mw = new NodloopMiddleware(c, "support-bot", acme);
const agent = createAgent({ model, tools, middleware: [mw.middleware()] });
await agent.invoke({ messages: [...] });
await c.judge(mw.run, "approve");
```

OpenAI Agents SDK instructions, recording and a tool input guardrail:

```ts
import { NodloopHooks, vetoGuardrail } from "nodloop/openai-agents";

const hooks = new NodloopHooks(c, "support-bot", acme);
const agent = new Agent({ name: "support", instructions: hooks.instructions("You help with refunds"), tools });
const runner = new Runner();
hooks.attach(runner);
await runner.run(agent, "refund?");
```

Pass `vetoGuardrail(c, "support-bot")` in a tool's `inputGuardrails`.

Claude Agent SDK hooks, the same loop the Claude Code plugin runs:

```ts
import { NodloopHooks } from "nodloop/claude-agent";

const hooks = new NodloopHooks(c, "support-bot", acme);
for await (const m of query({ prompt, options: { hooks: hooks.hooks() } })) {}
```

Use one adapter instance per run or conversation, since it keeps the items it handed out and the id of the run it recorded.

## A shared server

`Client.open({ url: "https://nodloop.example/mcp", key: "nl_..." })` connects to `nodloop server serve` instead of a local binary. The key decides the tenant and what the client may do, and an approval through it is recorded under the key's name.

## More

The [nodloop README](https://github.com/jeon-jihyeon/nodloop#readme) covers labels, vetoes, reports and the server. MIT licensed.
