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
  <img src="https://img.shields.io/badge/license-MIT-green" alt="license">
  <img src="https://img.shields.io/badge/API%20key-none-lightgrey" alt="API key">
</div>

<br>

You correct an AI answer, the session ends, and next week in the same repository the same mistake comes back. nodloop closes that loop. Each answer is recorded as a run with where it was made, your verdict on it is recorded as a nod, and a correction you approve by name becomes an item that the next prompt in that place receives. Nothing reaches a prompt that a person did not approve, and nothing reaches a place its scope does not cover.

## Quickstart

```
/plugin marketplace add jeon-jihyeon/nodloop
/plugin install nodloop@nodloop
```

On first run the plugin downloads its binary. From then on it records every answer of the conversation as a run labeled with the repository and directory you work in. Work as usual, and when an answer was wrong, say so:

```
> /nodloop:nod you ran cd before git again. Use git -C <dir> instead
> approve it, approver <your name>
```

The nod records an edit on that answer's run, proposes one sentence of what it taught scoped to this repository, and approves it only when you name yourself. Before every later prompt in this repository a hook adds the approved items as context, so the next answer follows the correction without being told again. `~/.nodloop/bin/nodloop knowledge for --producer session --label repo=<repo>` lists what a prompt there receives.

> [!TIP]
> Set `NODLOOP_SESSION=off` where Claude Code starts to keep the plugin without recording conversations. Answers are stored on your machine under `~/.nodloop/records` with keys, tokens and passwords redacted.

## Why nodloop

A CLAUDE.md or a memory file you edit by hand carries corrections too. nodloop adds four things to them.

| | Hand kept notes | nodloop |
|---|---|---|
| Approval | whatever was written | only versions a named person approved, with their history |
| Scope | every session reads every line | an item reaches only runs whose labels it matches |
| Measurement | none | for each item, the runs that received it, how many you approved, how many you corrected again for the same reason |
| Enforcement | a request in text | a forbidden tool call is blocked by the guard hook before it runs |

## How it works

```
answer → run with labels → your nod → proposed item → approved by name
   ↑                                                         ↓
   ← ← ← the next prompt in the same place receives it ← ← ←
```

A run is one output of a producer with the labels of its situation. The built in producer is the Claude Code session, whose labels are `repo`, the directory holding `.git` above where you work, and `dir`, the path below it. A nod is approve, edit with the corrected output, or reject with what was wrong. An item is a `meaning`, how to read something in this place, or a `judgment`, what to do or not do, and its scope names a producer, labels a run must carry and labels it must not. A label must be one a recorded run already carries, so a misspelled label fails instead of making an item that matches nothing.

<details>
<summary>How items are approved, kept within budget and kept fresh</summary>

An item is a candidate until a named person approves it. A new version may not reach runs the approved one never reached, and one that drops a veto is refused. All approved items one run may carry together are held to a size cap, and approval is refused when it would push them past it.

When one run would carry more than five items, `approve` says a compaction is due. A compaction drafts fewer items that say each fact once and lose none, and the ledger refuses a draft that reaches runs an old item never reached, two new items of one kind for the same runs, or an old item no new item names. Before approval a coverage check reads each old item against the new ones and lists any fact they lose, through `nodloop knowledge check` with a separate model call or through the MCP tool `check_compaction`. Nothing is approved until the check passes and a person names themselves.

When you record what a real check found with `outcome`, `nodloop knowledge health` shows the items whose runs were refuted as retire candidates and the stated items whose runs were confirmed as promotion candidates. `knowledge narrow <id> --version <n> --key dir` proposes a version that stops reaching the directories where it was refuted, and `knowledge promote` proposes one with basis verified. An approved version is stale 90 days after its approval or last reaffirm.

`nodloop report loop` shows per approved item how many runs received it, how many of those you approved, how many you corrected again for the reason that taught it, and how long the correction took to become an item. `nodloop queue` lists the runs that wait for a verdict, with a random audit share.

</details>

## Any producer

The loop is not tied to Claude Code. Any tool can record what it made as a run with the labels of its situation, take a nod on it and get back the approved items for its next run.

```
~/.nodloop/bin/nodloop run record --producer review-bot --label repo=api --label task=review --output review.json
~/.nodloop/bin/nodloop feedback add --trace <run id> --verdict edit --edited corrected.json --reason "a nil map read is not a panic"
~/.nodloop/bin/nodloop knowledge propose --from <run id> --kind meaning --content "Reading a nil map in Go returns the zero value"
~/.nodloop/bin/nodloop knowledge approve <id> --version 1 --approver <your name>
~/.nodloop/bin/nodloop knowledge for --producer review-bot --label repo=api --label task=review
```

The MCP server offers the same as tools: `run`, `knowledge_for`, `feedback`, `outcome`, `propose` with `from` set to a corrected run, `approve`, the compaction tools, `queue`, `knowledge_health` and `reaffirm`. A producer never depends on nodloop and nodloop never depends on a producer.

## Guard

`nodloop guard` blocks tool calls you've vetoed before they run. It's a PreToolUse hook, so the model can't talk its way past it.

```
~/.nodloop/bin/nodloop guard install
curl -fsSL --create-dirs https://raw.githubusercontent.com/jeon-jihyeon/nodloop/main/examples/vetoes.yaml -o ~/.claude/nodloop/vetoes.yaml
~/.nodloop/bin/nodloop guard check
```

The seed vetoes for Bash read `commands`, which the guard derives from the Bash or Monitor `command` with a shell parser: one line per simple command, also inside loops, pipes and substitutions, without quotes, so a word inside a quoted argument or a heredoc never counts as a command. The header of [vetoes.yaml](examples/vetoes.yaml) states what it still can't follow, such as variables and the string given to `bash -c`, which the seeds block outright.

A veto you write by hand can say `action: ask` to hand the call to you with its reason instead of blocking it, and a matching veto that blocks always wins over one that asks. Every block and ask is logged to `~/.nodloop/guard.jsonl` with the veto, the tool and the directory but never the command, and `nodloop guard log` prints the newest ones.

A correction can become a veto too. Propose it as a judgment with a veto, and once someone approves it, nodloop writes it to an approved veto file under `~/.claude/nodloop` and the guard blocks that call from then on. Such a judgment acts only through the guard, so no prompt receives it as text and vetoes never use up the budget of a run. Retiring the knowledge removes the veto, a new version that drops it is refused, and a veto you write by hand wins over an approved one with the same id.

<details>
<summary>Hook install, health check and where veto files are found</summary>

`guard install` backs up `~/.claude/settings.json` before adding the hook, and `guard uninstall` removes it. It registers `~/.nodloop/bin/nodloop`, which the plugin points at the binary it ran last, a PATH build included. The plugin runs a PATH build of another version only when `NODLOOP_ALLOW_PATH` is 1, because an older one lacks tools the skills call. Running `guard install` again repairs a hook whose binary is gone. `guard check` lists the veto files it found and ends with whether the hook is installed, turned off by `disableAllHooks`, or stale because it still runs an older plugin binary after an update, which `guard install` repairs. A `.claude/nodloop/vetoes.yaml` applies anywhere below its directory. The guard looks for one from the working directory up to the nearest directory holding `.git`, or up to home when there is none and only in the working directory outside both, and the nearer file wins on the same id. While a veto file under home isn't valid YAML, the guard blocks every call except reading and editing veto files, so one bad write can't turn your vetoes off.

</details>

## Records

| What | Where |
|---|---|
| Runs, verdicts, outcomes, knowledge | `~/.nodloop/records`, or `--record-dir`, `NODLOOP_RECORD_DIR` or `record_dir` in `~/.nodloop/config.json` |
| Approved vetoes | `~/.claude/nodloop/vetoes.approved.<hash>.yaml` |
| Approved items as rules a CLAUDE.md may import | `approved.md` in the record directory, which `nodloop knowledge export` prints the import line for |
| Guard decisions | `~/.nodloop/guard.jsonl` |

Every record file is append only JSON lines. A status change of an item is a new record, so the history of every version stays.

## Supported

macOS and Linux, or Windows through WSL. It runs as a Claude Code plugin. For Codex, Cursor or another MCP client, install it with `go install github.com/jeon-jihyeon/nodloop/cmd/nodloop@latest` and serve it with `nodloop mcp`. Such a client has no hooks from this plugin, so a producer there calls `run` and `knowledge_for` itself.

## Limits

The session labels are the repository and the directory, compared as exact strings. An item scoped to a repository reaches every prompt there, whatever the task. Whether an item reached a run it should not have is not measured yet. Versions before 0.6.0 reviewed incident data from an events file. That data review leaves this repository for a plugin of its own, and its knowledge records still load but reach no run.

## License

MIT. The nodloop name and logo are not part of the license. Please use your own name and logo for a fork.

---

## Resources

- [Seed vetoes](examples/vetoes.yaml): guard vetoes for common shell mistakes
- [Contributing](CONTRIBUTING.md): how to build, test and send changes
- [Security](SECURITY.md): how to report a vulnerability
- [Releases](https://github.com/jeon-jihyeon/nodloop/releases): darwin and linux archives the plugin downloads
- [Issues](https://github.com/jeon-jihyeon/nodloop/issues): bug reports and feature requests
