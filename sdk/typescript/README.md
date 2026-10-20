# nodloop for TypeScript

Record what your agent answered, take a person's verdict on it, and hand the corrections they approve to the agent's next run in the same situation. This is the TypeScript client of [nodloop](https://github.com/jeon-jihyeon/nodloop).

```
npm install nodloop
```

The client starts a local `nodloop mcp` over stdio, so no server runs. It uses `NODLOOP_BIN`, then the binary the Claude Code plugin keeps under `~/.nodloop/bin`, then `nodloop` on PATH, and otherwise downloads the release of its own version from GitHub and checks it against the release checksums. Tested on Linux with Node 24.

## The loop

```ts
import { Client } from "nodloop";

const acme = { tenant: ["acme"], task: ["refund"] };
const c = await Client.open();

const knowledge = await c.knowledge("support-bot", acme);
const answer = await myAgent(`${systemPrompt}\n\n${knowledge.text}`); // empty text when nothing is approved
const run = await c.record({ producer: "support-bot", output: answer, labels: acme, applied: knowledge.items });

// later, when a person reads the answer
await c.judge(run, "reject", "ann", { reason: "the refund window was missing", reasonCode: "scope" });
const candidate = await c.propose({ kind: "judgment", content: "Quote the refund window before the steps", from: run });
await c.close();
```

The person approves the candidate in a terminal of their own with `nodloop knowledge approve <id> --version <n>`, or the client approves it through a nodloop server under an approver key the person holds, added with `--person`. Over stdio `approve` records a proposal and changes nothing, since a program proves no person. From then on every `knowledge("support-bot", acme)` carries the item, and a run of another tenant never does. `judge` takes `approve`, `edit` with `edited` holding the corrected output, `reject` with what was wrong, or `withdraw` to take back the verdict before it, and always the name of the reviewer.

| Method | Returns |
|---|---|
| `knowledge(producer, labels, holdoutKey)` | `Knowledge` with `items`, the `text` to put in the prompt and `withheld`. With a `holdoutKey` such as `<service>:<hour>`, the `holdout` share of config.json gets no items and `withheld` instead |
| `record({ producer, output, labels, applied, subject, withheld, holdoutKey, model })` | the run id a verdict cites. Pass `knowledge.withheld` as `withheld` and the `holdoutKey` the run passed to `knowledge`. `report effect` counts only runs recorded with the key and counts a run without it as `undrawn`. Both lists take items or `{ id, version }` refs. `model` names the one model the producer runs, which the OTLP export sends as `gen_ai.request.model`. Leave it out when the producer may switch models |
| `judge(run, verdict, reviewer, { reason, reasonCode, edited })` | nothing. `reviewer` is required, so a verdict without a name never reads as a person's |
| `propose({ kind, content, from, producer, labels, newLabels, id, veto })` | `Candidate` with `id`, `version`, `status` and `overlaps`, the current items of the same kind one run may carry with it |
| `approve(id, version, approver, { labels })` | Under a key of a nodloop server that a person holds, approves as proposed, or for the runs carrying `labels` when given, and `{}` for every run of the producer. Anywhere else it records a proposal the person is asked about and the status stays. `Approval` with `id`, `version`, `status`, `approver`, `veto`, `proposal` when a proposal was recorded, and `exportError` and `folderError` when a step after the approval failed |
| `checkCall(tool, input, producer)` | `Decision` with `action` of allow, block or ask, `veto` and `reason` |
| `outcome(run, result, { confirmedCause, note, reviewer })` | nothing. `result` is confirmed, refuted or inconclusive: what a real check found, apart from the verdict on the output. Over stdio `reviewer` is required |
| `report(name)` | `Report` with `report`, the report as JSON, and `since`, the RFC3339 start of the runs it read, absent for every run. `name` is a `ReportName`: `loop`, `extract`, `critic`, `effect`, `health` or `replay`. `critic` is `{ critics, reactions }`: the rows of the draft critics and of the reaction classifiers |

A tool that refuses, such as a proposal from a run nobody corrected, throws `ToolError` with the reason.

Every adapter takes `{ labels, holdoutKey }` or the labels alone as its third argument, passes the key to `knowledge` and `record` and records what came back as applied or withheld.

## A stream producer with a holdout

A producer that runs on a schedule, such as an anomaly check over each window of a stream, draws the holdout on a key it keeps for the run. Use `<service>:<time block of an hour or more>` so the windows of one block land in the same arm and a lesson seen in one window does not leak into a withheld window next to it.

```ts
const block = new Date().toISOString().slice(0, 13); // one hour
const key = `orders:${block}`;
const knowledge = await c.knowledge("lag-check", { service: ["orders"] }, key);
const answer = await check(window, knowledge.text);
const run = await c.record({
  producer: "lag-check", output: answer, labels: { service: ["orders"] },
  applied: knowledge.items, withheld: knowledge.withheld, holdoutKey: key,
});

// later, when the cause was checked
await c.outcome(run, "confirmed", { confirmedCause: "partition 7 was hot", reviewer: "ann" });
const { report: effect } = await c.report("effect");
```

The share comes from `holdout` in config.json. With `url` the share is the one in the config.json of the nodloop server, read when the server starts.

## Adapters

Each adapter is its own entry point, and the framework it wraps is an optional peer dependency.

LangChain `createAgent` middleware fetches the items once per run and adds them to the system message, records the final answer and refuses a vetoed tool call:

```ts
import { NodloopMiddleware } from "nodloop/langchain";

const mw = new NodloopMiddleware(c, "support-bot", acme);
const agent = createAgent({ model, tools, middleware: [mw.middleware()] });
await agent.invoke({ messages: [...] });
await c.judge(mw.run, "approve", "ann");
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

const hooks = new NodloopHooks(c, "support-bot", { labels: acme, holdoutKey: conversationId });
for await (const m of query({ prompt, options: { hooks: hooks.hooks() } })) {}
```

Use one adapter instance per run or conversation, since it keeps the items it handed out and the id of the run it recorded.

The Claude Agent hooks draw once per conversation since the key stays the same for every turn, so a whole conversation is in one arm. `report effect` counts every turn of one key as one draw unit, so the turns of a conversation widen its intervals instead of passing for independent samples.

## A shared server

`Client.open({ url: "https://nodloop.example/mcp", key: "nl_..." })` connects to `nodloop-server serve` instead of a local binary. The key decides the tenant and what the client may do, and an approval through it is recorded under the key's name.

## More

The [nodloop README](https://github.com/jeon-jihyeon/nodloop#readme) covers labels, vetoes, reports and the server. MIT licensed.
