<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset=".github/logo-light.svg">
    <img alt="nodloop" src=".github/logo.svg" width="360">
  </picture>
</div>

<div align="center">
  <h3>Your nods and corrections on AI output, recorded and carried to the next run in the same place.</h3>
</div>

<div align="center">
  <a href="https://github.com/jeon-jihyeon/nodloop/releases"><img src="https://img.shields.io/github/v/release/jeon-jihyeon/nodloop?display_name=tag" alt="release"></a>
  <a href="https://github.com/jeon-jihyeon/nodloop/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/nodloop/test.yml?branch=main&label=test" alt="test"></a>
  <a href="https://goreportcard.com/report/github.com/jeon-jihyeon/nodloop"><img src="https://goreportcard.com/badge/github.com/jeon-jihyeon/nodloop" alt="go report"></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/jeon-jihyeon/nodloop"><img src="https://api.scorecard.dev/projects/github.com/jeon-jihyeon/nodloop/badge" alt="openssf scorecard"></a>
  <a href="https://pkg.go.dev/github.com/jeon-jihyeon/nodloop"><img src="https://pkg.go.dev/badge/github.com/jeon-jihyeon/nodloop.svg" alt="go reference"></a>
  <img src="https://img.shields.io/badge/license-MIT-green" alt="license">
  <img src="https://img.shields.io/badge/API%20key-none-lightgrey" alt="API key">
</div>

<br>

You correct an AI answer. The session ends. Next week, in the same repo, the same mistake is back.

nodloop closes that loop. It records every answer along with where it was made, records your verdict on it, and turns the corrections you approve into lessons that the next prompt in that place receives. Nothing reaches a prompt until a person approves it, and a lesson only shows up where its scope says it belongs.

## Quickstart

```
/plugin marketplace add jeon-jihyeon/nodloop
/plugin install nodloop@nodloop
```

That's it. Keep working the way you do now.

- **Tell Claude when it's wrong.** Plain words work. The conversation records your verdict quietly, without interrupting you.
- **Review when you come back.** Each correction is drafted into a one-line lesson in the background. The next time you start or resume Claude Code in that repo, it walks you through the waiting drafts, quoting what you said.
- **Approve by name, and pick where.** Each draft asks whether to approve it and where it applies: everywhere, this repo or this directory. Approved lessons are added to every later prompt there, so you don't have to say it twice.

Want it recorded right now? Nod on it:

```
> /nodloop:nod you ran cd before git again. Use git -C <dir> instead
> approve it, approver <your name>
```

`/nodloop:nod` on its own reviews the waiting drafts at any time. If a draft came from something that wasn't really a correction, mark it that way and both the verdict and the draft go away.

> [!TIP]
> `~/.nodloop/bin/nodloop config session_mode <mode>` changes how much happens on its own. `deferred` (the default) works as described above. `immediate` drafts the lesson and asks you in the same turn. `manual` waits for an explicit nod. `off` stops recording. Everything stays on your machine under `~/.nodloop/records`, with keys, tokens and passwords redacted.

## Why not just CLAUDE.md?

A CLAUDE.md or a memory file works too. nodloop adds four things:

| | Hand-kept notes | nodloop |
|---|---|---|
| Approval | whatever got written | only versions a named person approved, with full history |
| Scope | every session reads every line | a lesson reaches only the runs its labels match |
| Measurement | none | for each lesson: the runs that got it, how many you approved, how many you had to correct again |
| Enforcement | a polite request | the guard hook blocks a forbidden tool call before it runs |

## How it works

```
answer → run with labels → your nod → proposed item → approved by name
   ↑                                                         ↓
   ← ← ← the next prompt in the same place receives it ← ← ←
```

- **Run**: one output of a producer, tagged with labels for its situation. For Claude Code sessions the labels are `repo` and `dir`, taken from where Claude Code started, so a `cd` into a scratch directory never mislabels a run.
- **Nod**: your verdict on a run. `approve`, `edit` with the corrected output, `reject` with what was wrong, or `withdraw` to take a verdict back.
- **Item**: an approved lesson. A `meaning` says how to read something here; a `judgment` says what to do or avoid. Its scope names the producer and the labels it applies to, and you confirm it when you approve: everywhere, or only where a run carries a label such as a repo, a domain or a file format. A label has to match one a real run already carries, so a typo fails loudly instead of matching nothing.

Before you see a draft, a second model call checks it. It rejects drafts that just copy the answer, that only fit this one answer, or that get their relation to existing items wrong. A draft that repeats or contradicts an existing item shows you that item instead.

| Command | What it does |
|---|---|
| `knowledge replay <id>` | Tests a draft against past answers: it should change the ones you corrected and leave the newest ten approved ones alone. Advisory only |
| `knowledge compact`, `knowledge check` | When a run would carry more than five items, drafts a shorter set and checks that it loses no fact before you approve it |
| `knowledge health`, `narrow`, `promote` | Flags items that keep getting corrected or haven't been used in 30 days, and proposes narrower or verified versions |
| `report loop` | Shows where the loop stalls: runs, verdicts, corrections, drafts waiting and items approved, then per-item results |
| `report effect` | With `config holdout 0.1`, one turn in ten gets no items, so you can compare correction rates with and without them |
| `classifier set <point> --url <url>` | Lets your own HTTP classifier answer a decision point first, with claude as the fallback |

Items are kept within a size budget per run, and an approved version goes stale after 90 days unless someone reaffirms it. `nodloop help <command>` has the details for each command.

<details>
<summary>Classifier wire format</summary>

An endpoint gets a POST with the text and named yes/no questions, plus `Authorization: Bearer` when you set a key. It answers each question with the probability of yes under `noul`, the question type of the Jev wire format. claude takes over when the endpoint fails, returns a non-2xx status, sends more than 1 MiB, leaves a question out, or is less than 0.8 sure of any answer.

```
→ {"model": "<model>", "state": "<text>", "questions": {"copies": {"type": "noul", "instructions": "Does the lesson copy the answer?"}}}
← {"answers": {"copies": {"noul": 0.12}}}
```

</details>

## Use it from any tool

The loop isn't tied to Claude Code. Any tool can record a run, take a nod on it and get the approved items back for its next run. The plugin keeps its binary at `~/.nodloop/bin/nodloop`.

```
nodloop run record --producer review-bot --label repo=api --label task=review --output review.json
nodloop feedback add --trace <run id> --verdict edit --edited corrected.json --reason "a nil map read is not a panic"
nodloop knowledge propose --from <run id> --kind meaning --content "Reading a nil map in Go returns the zero value"
nodloop knowledge approve <id> --version 1 --approver <your name>
nodloop knowledge for --producer review-bot --label repo=api --label task=review
```

Labels are any keys and values. Name them the way your team thinks about the work: `tenant`, `agent`, `task`, `env`.

Python and TypeScript agents can use the SDKs, which run a local `nodloop mcp` over stdio and download the binary if it's missing. Both include adapters for LangChain, the OpenAI Agents SDK and the Claude Agent SDK.

```
pip install nodloop
npm install nodloop
```

```python
from nodloop import Client
from nodloop.langchain import NodloopMiddleware

async with Client() as c:
    mw = NodloopMiddleware(c, "support-bot", {"tenant": ["acme"]})
    agent = create_agent(model, tools, middleware=[mw])  # items in, answer recorded, vetoed tool calls refused
    await agent.ainvoke({"messages": [...]})
    await c.judge(mw.run, "reject", reason="the refund window was missing")
```

See the [Python](sdk/python/README.md) and [TypeScript](sdk/typescript/README.md) READMEs for every method, and [example_test.go](example_test.go) for the Go package.

| Need | Use |
|---|---|
| Any MCP client | `nodloop mcp` serves the same loop as tools: `run`, `knowledge_for`, `feedback`, `propose`, `approve`, `check_call` and more |
| A shared server for a team | `nodloop-server serve` offers the MCP tools over HTTP at `/mcp`, with per-tenant records, `producer`, `reviewer` and `approver` keys, and optional PostgreSQL |
| Dashboards | `report <name> --json`, the MCP tool `report`, or `GET /v1/reports/{name}` on the server, for Grafana or any JSON data source |
| Tracing tools | `nodloop export otel --endpoint <url>` sends runs as OpenTelemetry GenAI spans and verdicts as evaluation events, to a Collector, Langfuse or any OTLP backend |

## Guard

`nodloop guard` blocks tool calls you've vetoed, before they run. It's a PreToolUse hook, so the model can't talk its way past it.

```
~/.nodloop/bin/nodloop guard install
curl -fsSL --create-dirs https://raw.githubusercontent.com/jeon-jihyeon/nodloop/main/examples/vetoes.yaml -o ~/.claude/nodloop/vetoes.yaml
~/.nodloop/bin/nodloop guard check
```

Bash vetoes match each simple command a shell parser finds, including ones inside loops, pipes and substitutions. Words inside quotes or heredocs don't count. The header of [vetoes.yaml](examples/vetoes.yaml) lists what the parser can't follow yet. A veto can `block` or `ask`, and every decision is logged to `~/.nodloop/guard.jsonl` without the command itself.

A correction can become a veto too. Propose a judgment with a veto, and once someone approves it, the guard enforces it. Retire the item and the veto goes with it. Other agents can check a call with `nodloop guard call`, the MCP tool `check_call`, or `CheckCall` in Go.

<details>
<summary>Install details and where veto files are found</summary>

`guard install` backs up `~/.claude/settings.json` before adding the hook, and `guard uninstall` removes it. Run `guard install` again to fix a hook whose binary is gone or out of date. `guard check` lists the veto files it found and whether the hook is installed, disabled by `disableAllHooks`, or stale.

A `.claude/nodloop/vetoes.yaml` applies to its directory and everything below. The guard looks from the working directory up to the repo root, or up to home outside a repo, and the nearer file wins on the same id. If a veto file under home stops being valid YAML, the guard blocks everything except reading and editing veto files, so one bad save can't switch your vetoes off.

</details>

## Records and settings

Every record is an append-only JSON line, so the full history of every item stays. A line broken by a crash is skipped and reported, and `nodloop doctor --repair` moves it aside.

| What | Where |
|---|---|
| Runs, verdicts, outcomes, items | `~/.nodloop/records`, or `--record-dir`, `NODLOOP_RECORD_DIR`, `record_dir` |
| Approved vetoes | `~/.claude/nodloop/vetoes.approved.<hash>.yaml` |
| Approved items for a CLAUDE.md import | `approved.md` in the record directory (`nodloop knowledge export`) |
| Guard decisions, background drafting log | `~/.nodloop/guard.jsonl`, `~/.nodloop/hook.log` |

`nodloop config` lists every setting with its value and where it came from: session mode, approver, holdout, decision points, server keys, the claude binary and model (`sonnet` by default), PostgreSQL and the OTLP endpoint.

## Privacy

No telemetry. Your records leave your machine only when:

| When | What goes where |
|---|---|
| A lesson or compaction is drafted, checked or replayed | the run outputs, verdicts and items involved, to `claude -p` under your Claude Code login |
| A decision point asks your classifier | the text to judge, to the endpoint you set |
| The plugin or an SDK needs its binary | a download of the release archive and checksums from GitHub |
| You run `nodloop export otel` | runs and verdicts, plus outputs with `--with-output`, to the endpoint you name |

Outputs are redacted of keys, tokens and passwords before they're stored or sent.

## Supported

macOS and Linux, or Windows through WSL. Python 3.10+ for the Python SDK. For Codex, Cursor or another MCP client, `go install github.com/jeon-jihyeon/nodloop/cmd/nodloop@latest` and run `nodloop mcp`. Those clients don't get the plugin's hooks, so they call `run` and `knowledge_for` themselves.

## Limits

Session labels are the repo and directory, matched as exact strings, so an item scoped to a repo reaches every prompt there regardless of the task. We don't yet measure whether an item reached a run it shouldn't have. A verdict inferred from your words can be wrong, which is why it only ever drafts a candidate and never approves one.

## Versioning

nodloop is 0.x. Until 1.0:

| Surface | Promise |
|---|---|
| Record files | newer versions read everything older versions wrote |
| MCP tools and CLI | patch releases only add optional fields and flags; removals wait for a minor release |
| Go package and SDKs | may change in a minor release, listed under Changed in the [changelog](CHANGELOG.md) |

The plugin, both binaries and both SDKs share one version number.

## License

MIT. The nodloop name and logo aren't covered by the license, so please use your own for a fork.

---

## Resources

- [Seed vetoes](examples/vetoes.yaml): guard vetoes for common shell mistakes
- [Changelog](CHANGELOG.md): what changed in each release
- [Contributing](CONTRIBUTING.md): how to build, test and send changes
- [Code of conduct](CODE_OF_CONDUCT.md): how we treat each other
- [Security](SECURITY.md): how to report a vulnerability
- [Releases](https://github.com/jeon-jihyeon/nodloop/releases): the archives the plugin downloads
- [Issues](https://github.com/jeon-jihyeon/nodloop/issues): bug reports and feature requests
