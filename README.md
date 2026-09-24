<img alt="nodloop" src=".github/logo.svg" width="360">

Runbook-grounded incident reviews inside Claude Code. Your corrections become approved knowledge and return in the next review. The name is the loop it closes: a review goes out, a human nods or corrects, and the nod comes back into the next run.

[![release](https://img.shields.io/github/v/release/jeon-jihyeon/nodloop?display_name=tag)](https://github.com/jeon-jihyeon/nodloop/releases) [![test](https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/nodloop/test.yml?branch=main&label=test)](https://github.com/jeon-jihyeon/nodloop/actions/workflows/test.yml) [![go report](https://goreportcard.com/badge/github.com/jeon-jihyeon/nodloop)](https://goreportcard.com/report/github.com/jeon-jihyeon/nodloop) ![license](https://img.shields.io/badge/license-MIT-green) ![API key](https://img.shields.io/badge/API%20key-none-lightgrey)

![nodloop demo: a review, a correction, an approval, the next review applying it, and the guard hook blocking a forbidden command](.github/demo.gif)

## Quickstart

```
/plugin marketplace add jeon-jihyeon/nodloop
/plugin install nodloop@nodloop
> review event tq-023 with nodloop
```

The first start of the plugin fetches the `nodloop` binary from its GitHub release into `~/.nodloop/bin` and unpacks a demo set of 24 synthetic ad traffic events with runbooks and labels. Point it at your own data with `~/.nodloop/bin/nodloop setup --data-dir <dir>`, or ask Claude Code to run the setup.

An event is a set of metric rows in `events.csv` with the columns `event_id,timestamp,metric,value`, and any other column such as `source` or `topic` is a dimension. `contexts.csv` gives one change context per event. A runbook is a Markdown file under `runbooks/` whose sections become citable paragraphs with ids like `data-integrity-hold#Data integrity hold/Coverage#1`. `labels.jsonl` is optional ground truth for `nodloop eval` and `policy.yaml` holds the analyzer thresholds. The demo set under `~/.nodloop/demo` has all of them.

Without the marketplace: `go install github.com/jeon-jihyeon/nodloop/cmd/nodloop@latest`, then `nodloop setup --demo` and `claude --plugin-dir nodloop/plugin` from a clone.

## Why

Model-written incident reviews drift. Numbers get invented, causes go uncited, and the same correction is repeated every week. Each point below closes one of those.

- Numbers come from code. Analyzers turn events into observations with baselines, sample counts and gaps before the model sees them.
- Every cause cites a runbook paragraph. A citation gate turns an uncited review into a hold before anyone reads it.
- Corrections become knowledge with a scope. Knowledge applies only after a named person approves it and only to events its scope fits, and every review records the version it used.
- Everything is a record. The context, the selection, the review, the verdict and the knowledge version each land in an append-only file you can read back.

## How it works

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/loop-dark.svg">
  <img alt="observe, context, select and record on the tool row; feedback, propose and approve on the human row; the loop returns to context" src=".github/loop-light.svg" width="760">
</picture>

nodloop is an MCP server and a skill. Claude Code calls `observe`, `context` and `select`, writes the review itself, and calls `record`. You answer with a verdict, and `feedback`, `propose` and `approve` turn it into a record and, when you say so, into knowledge. When a review cites a Decide paragraph as a cause or skips the first step of the runbook it follows, `record` sends it back once with the reasons instead of recording it. nodloop never calls a model inside the conversation. No API key, no hosted service.

A review is structured output under a JSON schema: a `status` of `no_action`, `ready_for_review` or `hold`, ordered `causes` with paragraph ids, ordered `checks` with a purpose, and `open_questions`. A hold has no causes and lists as checks the steps that would lift it, each with its paragraph ids.

```json
{
  "status": "ready_for_review",
  "observations": ["conversion_rate fell from 0.030 to 0.0074 in the newest 4 points, earlier 8 at baseline"],
  "causes": [{"summary": "landing page or tracking failure", "paragraph_ids": ["outcome-rate-degradation#Outcome rate degradation/Separate numerator from denominator#1"]}],
  "checks": [{"step": "compare the newest 4 points with the earlier 8", "purpose": "confirm the drop is confined to the newest hours", "paragraph_ids": ["outcome-rate-degradation#Outcome rate degradation/Confirm the rate#1"]}],
  "open_questions": []
}
```

## Measured

The 24 demo events split in half. The 12 seed events were reviewed once and their reviews corrected, which produced the corrections and the knowledge. The 12 holdout events were then reviewed with sonnet under four conditions. Every number comes from `nodloop eval report`.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/measured-dark.svg">
  <img alt="status accuracy 0.83 with feedback off and 1.00 with corrections, scoped knowledge and unscoped knowledge; unscoped knowledge lands 15 items the label does not expect" src=".github/measured-light.svg" width="760">
</picture>

- The baseline misses the two attribution lag events and nothing else. Corrections as examples, scoped knowledge and unscoped knowledge each fix both.
- Unscoped knowledge also lands 15 items the label does not name, 8 of them on events whose label expects none. Status accuracy does not show it. Scoping is what keeps knowledge from becoming noise.
- Citation precision is 0.51 at baseline and 0.71 with corrections, and recall stays at 0.92 or above in every condition.
- Required checks reach 1.00 with corrections and 0.94 at baseline and with scoped knowledge. Hold reviews list the checks that would lift the hold, and the two hold events cite the measurement paragraph their label requires in every condition.
- `record` sent one review of 48 back. The same rules are in the prompt, so most reviews arrive complete.

| condition | events | status acc | hold acc | citation p | citation r | required checks | first check | knowledge hit | misapplied | revised | mean cost usd |
|---|---|---|---|---|---|---|---|---|---|---|---|
| seed | 12 | 0.83 | 1.00 | 0.43 | 0.92 | 0.69 | 0.38 | 0.00 | 0 | 0 | 0.0661 |
| feedback:off | 12 | 0.83 | 1.00 | 0.51 | 0.92 | 0.94 | 0.62 | 0.00 | 0 | 1 | 0.0516 |
| feedback:on | 12 | 1.00 | 1.00 | 0.71 | 1.00 | 1.00 | 0.62 | 0.00 | 0 | 0 | 0.0434 |
| knowledge:on | 12 | 1.00 | 1.00 | 0.57 | 1.00 | 0.94 | 0.50 | 1.00 | 0 | 0 | 0.0375 |
| knowledge:all | 12 | 1.00 | 1.00 | 0.48 | 0.92 | 0.81 | 0.50 | 1.00 | 15 | 0 | 0.0423 |

Against feedback:off

| condition | fixed | regressed | weakened |
|---|---|---|---|
| feedback:on | tq-023, tq-024 | - | - |
| knowledge:on | tq-023, tq-024 | - | tq-015 |
| knowledge:all | tq-023, tq-024 | - | tq-011, tq-012, tq-015 |

Fixed and regressed compare the status with the baseline on the same event. Weakened means the status held but required checks or citation recall fell, which an average hides. The full table with hold precision, latency and token columns is `eval-<session>.json` in the record directory.

Conditions: `feedback:off` is the holdout baseline. `feedback:on` injects the three newest seed corrections that share the change context and a metric as examples. Each example shows the verdict, reason and corrected review first and the original review last, and gets an even share of the 6000 character cap. A cut shortens the original review first and never the verdict or reason. `knowledge:on` applies approved items whose scope fits the event. `knowledge:all` applies every approved item without scoping. Every holdout row is the newest review per event under the current review rules, and the seed row is the first review of the seed events.

Required checks is the share of paragraphs a label requires checks for that the review cites, scored only on events whose label expects `ready_for_review` or `hold`. Knowledge hit is the share of expected items applied on the eight events whose label names an item, and misapplied counts applied items the label does not name. First check is whether the first check of the review cites the first required step. Revised counts reviews that `record` sent back once, and their cost includes both model calls. Mean cost usd is the API list price equivalent of one review in the eval batch, as `claude -p` reports it. A review inside a conversation spends a similar amount of tokens but takes a different number of turns, so it is a reference, not the same number.

Seed feedback was written by Claude and marked `reviewer: claude`. Replace it with `nodloop feedback add` and re-run to measure your own corrections.

```
nodloop eval seed --session demo-2 --model sonnet --parallel 6
nodloop feedback add --trace <id> --verdict edit ...
nodloop knowledge import --file ~/.nodloop/demo/knowledge.jsonl
nodloop eval holdout --session demo-2 --model sonnet --parallel 6
nodloop eval report --session demo-2
```

## Guard

`nodloop guard` is the second loop: a PreToolUse hook that blocks tool calls matching a veto file. A veto is a rule you write once after a correction. It blocks the call before the tool runs, whatever the model decided.

```
~/.nodloop/bin/nodloop guard install
mkdir -p ~/.claude/nodloop
curl -fsSL https://raw.githubusercontent.com/jeon-jihyeon/nodloop/main/examples/vetoes.yaml -o ~/.claude/nodloop/vetoes.yaml
~/.nodloop/bin/nodloop guard check
```

The plugin keeps the binary at `~/.nodloop/bin/nodloop`. After `go install` it is `nodloop` on your PATH. Vetoes live in `.claude/nodloop/vetoes.yaml` in the project and in `~/.claude/nodloop/vetoes.yaml`. The seed set in `examples/vetoes.yaml` blocks `sed -i`, `eval`, `bash -c`, `cd X && git` and README writes through the Write tool.

## Supported

| Surface | Support |
|---|---|
| OS | macOS and Linux, arm64 and amd64. Windows through WSL. A native Windows build works for one process at a time because the record store has no file lock there |
| AI client | Claude Code: plugin with skill and MCP server, batch evaluation through `claude -p`. Any MCP client such as Codex or Cursor: the eleven review tools over stdio with `nodloop mcp` |
| Model | Reviews run on the client's model. Measured numbers are sonnet |

## Records

Everything lands in `~/.nodloop/records/` unless `--record-dir`, `NODLOOP_RECORD_DIR` or `nodloop setup --record-dir` names another directory. The CLI and the MCP server resolve it the same way.

| File | What |
|---|---|
| `traces.jsonl` | one record per context, selection, send back and review. Input, output, usage, duration, tags |
| `feedback.jsonl` | one record per verdict: approve, edit or reject, the reason, the corrected review in full |
| `outcomes.jsonl` | one record per real check: confirmed, refuted or inconclusive |
| `knowledge.jsonl` | one record per knowledge version and status change |

`nodloop trace list`, `nodloop feedback list`, `nodloop knowledge list` and `nodloop trace pending` read them back.

## Limits

- All runbook paragraphs go to the model. No retrieval, so large runbook sets grow the context.
- Claude Code has no MCP sampling, so nodloop cannot run a review on its own inside the conversation. nodloop can refuse to record a review that skipped its steps, but it cannot make Claude Code call the tools.
- Batch reviews carry the Claude Code system prompt on every `claude -p` call. On a subscription the cost is not billed separately.
- A gap after the last timestamp of the whole event is invisible to the coverage rule, because its window ends there.

Roadmap: exporting judgment knowledge into guardrail rules and CLAUDE.md, bounded exploration tools, connectors for databases and Parquet, retrieval over runbooks.

## License

MIT. The nodloop name and logo are not part of the license. A fork or a derived product ships under its own name and logo.
