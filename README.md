<img alt="nodloop" src=".github/logo.svg" width="360">

Runbook-grounded incident reviews inside Claude Code. Your corrections become approved knowledge that feeds the next review.

[![release](https://img.shields.io/github/v/release/jeon-jihyeon/nodloop?display_name=tag)](https://github.com/jeon-jihyeon/nodloop/releases) [![test](https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/nodloop/test.yml?branch=main&label=test)](https://github.com/jeon-jihyeon/nodloop/actions/workflows/test.yml) [![go report](https://goreportcard.com/badge/github.com/jeon-jihyeon/nodloop)](https://goreportcard.com/report/github.com/jeon-jihyeon/nodloop) ![license](https://img.shields.io/badge/license-MIT-green) ![API key](https://img.shields.io/badge/API%20key-none-lightgrey)

![nodloop demo: a review, a correction, an approval, the next review applying it, and the guard hook blocking a forbidden command](.github/demo.gif)

## Quickstart

```
/plugin marketplace add jeon-jihyeon/nodloop
/plugin install nodloop@nodloop
```

On first run, the plugin downloads its binary and unpacks a demo set of 24 synthetic ad-traffic events. Then run one full loop:

```
> review event tq-023 with nodloop
> That is attribution lag. Conversions arrive up to 4 hours after the click, so the newest 4 hours always read low. Record that as feedback and propose it as knowledge.
> approve it, approver <your name>
> review event tq-024 with nodloop
```

The second review applies the knowledge you approved. When you are done with the demo, ask Claude Code to point nodloop at your own events and runbooks.

## How it works

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/loop-dark.svg">
  <img alt="observe, context, select and record on the tool row; feedback, propose and approve on the human row; the loop returns to context" src=".github/loop-light.svg" width="760">
</picture>

Claude Code writes the review with its own model, so there's no API key to set up. nodloop gives it the numbers, computed from your events by plain code, and the runbook paragraphs it can cite. A cause without a citation puts the review on hold.

When you correct a review, the correction can become a knowledge item. It's used only after someone approves it, and only on events that match its scope. If approving an item would make the knowledge a review carries outgrow its budget, nodloop refuses and asks you to retire an item, replace one or narrow the scope, instead of cutting text you never see. Reviews, verdicts and knowledge versions are all kept in `~/.nodloop/records`.

## Measured

The eval holds out 12 of the 24 demo events and reviews them with Sonnet. With no feedback, 10 of 12 got the right status. With corrections or approved knowledge, all 12 did. Applying every knowledge item regardless of scope also gets 12, but drags in 15 items that don't belong.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/measured-dark.svg">
  <img alt="status accuracy 0.83 with feedback off and 1.00 with corrections, scoped knowledge and unscoped knowledge; unscoped knowledge lands 15 items the label does not expect" src=".github/measured-light.svg" width="760">
</picture>

| condition | events | status acc | hold acc | citation p | citation r | required checks | first check | knowledge hit | misapplied | revised | mean cost usd |
|---|---|---|---|---|---|---|---|---|---|---|---|
| seed | 12 | 0.83 | 1.00 | 0.43 | 0.92 | 0.69 | 0.38 | 0.00 | 0 | 0 | 0.0661 |
| feedback:off | 12 | 0.83 | 1.00 | 0.51 | 0.92 | 0.94 | 0.62 | 0.00 | 0 | 1 | 0.0516 |
| feedback:on | 12 | 1.00 | 1.00 | 0.71 | 1.00 | 1.00 | 0.62 | 0.00 | 0 | 0 | 0.0434 |
| knowledge:on | 12 | 1.00 | 1.00 | 0.57 | 1.00 | 0.94 | 0.50 | 1.00 | 0 | 0 | 0.0375 |
| knowledge:all | 12 | 1.00 | 1.00 | 0.48 | 0.92 | 0.81 | 0.50 | 1.00 | 15 | 0 | 0.0423 |

Run `nodloop eval seed`, `holdout` and `report` to reproduce the table.

### Beyond the demo

Nothing in the core knows about ads. The same loop ran on 300 events built from the public [Tennessee Eastman Process data of Rieth et al.](https://doi.org/10.7910/DVN/6C3JR1), a simulated chemical plant with seeded faults. Five runbooks, four planted traps and a scripted reviewer stood in for a plant team, so no human took part. 150 events were seed and 150 held out, all reviewed with Sonnet.

| condition | events | status acc | hold acc | citation p | citation r | required checks | first check | knowledge hit | misapplied | revised | mean cost usd |
|---|---|---|---|---|---|---|---|---|---|---|---|
| seed | 150 | 0.75 | 0.63 | 0.49 | 0.97 | 0.85 | 0.85 | 0.00 | 0 | 6 | 0.0458 |
| feedback:off | 150 | 0.74 | 0.60 | 0.48 | 0.95 | 0.85 | 0.85 | 0.00 | 0 | 4 | 0.0454 |
| feedback:on | 150 | 0.99 | 1.00 | 0.69 | 0.98 | 1.00 | 1.00 | 0.00 | 0 | 2 | 0.0424 |
| knowledge:on | 150 | 1.00 | 1.00 | 0.55 | 1.00 | 1.00 | 0.98 | 1.00 | 0 | 6 | 0.0253 |
| knowledge:all | 150 | 0.99 | 1.00 | 0.49 | 0.98 | 0.99 | 0.98 | 1.00 | 548 | 1 | 0.0469 |

In 36 held-out events a trap decides the status. With no feedback, none of them got it right. With corrections or scoped knowledge, all 36 did, and scoped knowledge never landed on an event outside its scope. Applying every item regardless of scope landed 548 items that don't belong.

## Guard

`nodloop guard` blocks tool calls you've vetoed before they run. It's a PreToolUse hook, so the model can't talk its way past it.

```
~/.nodloop/bin/nodloop guard install
curl -fsSL --create-dirs https://raw.githubusercontent.com/jeon-jihyeon/nodloop/main/examples/vetoes.yaml -o ~/.claude/nodloop/vetoes.yaml
~/.nodloop/bin/nodloop guard check
```

`guard install` backs up `~/.claude/settings.json` before adding the hook, and `guard uninstall` removes it.

A correction can become a veto too. Propose it as a judgment with a veto, and once someone approves it, nodloop writes it to an approved veto file under `~/.claude/nodloop` and the guard blocks that call from then on. Retiring the knowledge removes the veto, and a veto you write by hand wins over an approved one with the same id.

## Supported

macOS and Linux, or Windows through WSL. It runs as a Claude Code plugin. For Codex, Cursor or another MCP client, install it with `go install github.com/jeon-jihyeon/nodloop/cmd/nodloop@latest` and run `nodloop mcp`.

## Limits

Every runbook paragraph goes to the model, so a large set of runbooks means a large context.

## License

MIT. The nodloop name and logo are not part of the license. Forks must use their own name and logo.
