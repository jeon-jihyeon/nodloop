# nodloop for Python

Record what your agent answered, take a person's verdict on it, and hand the corrections they approve to the agent's next run in the same situation. This is the Python client of [nodloop](https://github.com/jeon-jihyeon/nodloop).

```
pip install nodloop
pip install "nodloop[langchain]"      # or openai-agents, claude-agent
```

The client starts a local `nodloop mcp` over stdio, so no server runs. It uses `NODLOOP_BIN`, then the binary the Claude Code plugin keeps under `~/.nodloop/bin`, then `nodloop` on PATH, and otherwise downloads the release of its own version from GitHub and checks it against the release checksums. It requires Python 3.10 or newer and is tested on Linux with Python 3.12.

## The loop

```python
from nodloop import Client

acme = {"tenant": ["acme"], "task": ["refund"]}

async with Client() as c:
    knowledge = await c.knowledge("support-bot", acme)
    prompt = f"{system_prompt}\n\n{knowledge.text}"  # empty text when nothing is approved
    answer = await my_agent(prompt)
    run = await c.record("support-bot", answer, labels=acme, applied=knowledge.items)

    # later, when a person reads the answer
    await c.judge(run, "reject", reason="the refund window was missing", reason_code="scope")
    candidate = await c.propose("judgment", "Quote the refund window before the steps", from_run=run)
    approval = await c.approve(candidate.id, candidate.version, approver="ann")
```

From then on every `knowledge("support-bot", acme)` carries the item, and a run of another tenant never does. `judge` takes `approve`, `edit` with `edited=` holding the corrected output, `reject` with what was wrong, or `withdraw` to take back the verdict before it.

| Method | Returns |
|---|---|
| `knowledge(producer, labels, holdout_key)` | `Knowledge` with `items`, the `text` to put in the prompt and `withheld`. With a `holdout_key` such as `<service>:<hour>`, the `holdout` share of config.json gets no items and `withheld` instead |
| `record(producer, output, *, labels, applied, subject, withheld, holdout_key, model)` | the run id a verdict cites. Pass `knowledge.withheld` as `withheld` and the `holdout_key` the run passed to `knowledge`. `report effect` counts only runs recorded with the key and counts a run without it as `undrawn`. `withheld` takes `Ref` or `Item` values. `model` names the one model the producer runs, which the OTLP export sends as `gen_ai.request.model`. Leave it out when the producer may switch models |
| `judge(run, verdict, reason, reason_code, edited, reviewer)` | nothing |
| `propose(kind, content, from_run, producer, labels, new_labels, item_id, veto)` | `Candidate` with `id`, `version`, `status` and `overlaps`, the current items of the same kind one run may carry with it |
| `approve(item_id, version, approver, labels=)` | Approves as proposed, or for the runs carrying `labels` when given, and `{}` for every run of the producer. `Approval` with `id`, `version`, `status`, `approver`, `veto`, and `export_error` and `folder_error` when a step after the approval failed |
| `check_call(tool, arguments, producer)` | `Decision` with `action` of allow, block or ask, `veto` and `reason` |
| `outcome(run, result, confirmed_cause, note, reviewer)` | nothing. `result` is a `Result`, confirmed, refuted or inconclusive: what a real check found, apart from the verdict on the output |
| `report(name)` | `Report` with `report`, the report as JSON, and `since`, the RFC3339 start of the runs it read or empty for every run. `name` is a `ReportName`: `loop`, `extract`, `critic`, `effect`, `health` or `replay`. `critic` is `{critics, reactions}`: the rows of the draft critics and of the reaction classifiers |

A tool that refuses, such as a proposal from a run nobody corrected, raises `ToolError` with the reason.

Every adapter takes an optional `holdout_key`, passes it to `knowledge` and `record` and records what came back as applied or withheld.

## A stream producer with a holdout

A producer that runs on a schedule, such as an anomaly check over each window of a stream, draws the holdout on a key it keeps for the run. Use `<service>:<time block of an hour or more>` so the windows of one block land in the same arm and a lesson seen in one window does not leak into a withheld window next to it.

```python
from datetime import datetime, timezone

block = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H")  # one hour
orders = {"service": ["orders"]}
knowledge = await c.knowledge("lag-check", orders, holdout_key=f"orders:{block}")
answer = await check(window, knowledge.text)
run = await c.record(
    "lag-check", answer, labels=orders, applied=knowledge.items, withheld=knowledge.withheld, holdout_key=f"orders:{block}"
)

# later, when the cause was checked
await c.outcome(run, "confirmed", confirmed_cause="partition 7 was hot")
effect = (await c.report("effect")).report
```

The share comes from `holdout` in config.json. With `url` the share is the one in the config.json of the nodloop server, read when the server starts.

## Adapters

LangChain `create_agent` middleware fetches the items once per run and puts them in the system message, records the final answer and refuses a vetoed tool call:

```python
from nodloop.langchain import NodloopMiddleware

mw = NodloopMiddleware(c, "support-bot", acme)
agent = create_agent(model, tools, middleware=[mw])
await agent.ainvoke({"messages": [...]})
await c.judge(mw.run, "approve")
```

OpenAI Agents SDK hooks and a tool input guardrail:

```python
from nodloop.openai_agents import NodloopHooks, veto_guardrail

hooks = NodloopHooks(c, "support-bot", acme)
agent = Agent(name="support", instructions=hooks.instructions("You help with refunds"), tools=[...])
await Runner.run(agent, "refund?", hooks=hooks)
```

Pass `veto_guardrail(c, "support-bot")` in a tool's `tool_input_guardrails`.

Claude Agent SDK hooks, the same loop the Claude Code plugin runs:

```python
from nodloop.claude_agent import NodloopHooks

hooks = NodloopHooks(c, "support-bot", acme, holdout_key=conversation_id)
options = ClaudeAgentOptions(hooks=hooks.hooks())
```

Use one adapter instance per run or conversation, since it keeps the items it handed out and the id of the run it recorded.

The Claude Agent hooks draw once per conversation since the key stays the same for every turn, so a whole conversation is in one arm. `report effect` counts every turn of one key as one draw unit, so the turns of a conversation widen its intervals instead of passing for independent samples.

## A shared server

`Client(url="https://nodloop.example/mcp", key="nl_...")` connects to `nodloop-server serve` instead of a local binary. The key decides the tenant and what the client may do, and an approval through it is recorded under the key's name.

## More

The [nodloop README](https://github.com/jeon-jihyeon/nodloop#readme) covers labels, vetoes, reports and the server. MIT licensed.
