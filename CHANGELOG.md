# Changelog

Every release tag publishes the plugin, the darwin and linux binaries of `nodloop` and, from 0.7.0, `nodloop-server`, and both SDKs under one version. Versions before 0.6.0 reviewed incident data, which left this repository in 0.6.0.

## Unreleased

### Breaking
- A write counts as your decision only when the call proves a person: `nodloop` at a terminal of your own outside an agent session, or an approver key of nodloop-server added with `--person`. Any other approve, retire, reaffirm or compaction approval is recorded as an agent proposal, an `intervention` trace with `authority` agent and `proof` none, and the knowledge stays as it was, since a model in a session approved four lessons nobody approved in two of seven headless chains
- Go package: `Approve`, `ApproveIn` and `Retire` record an agent proposal and return it as an `Intervention` with `Authority`, `Proof` and the version it names, and the ledger stays as it is. A Go service that serves a person sends the approval to nodloop-server under the person's key. `Judge` and `Outcome` without `Reviewer` fail with `ErrReviewerRequired`, and `nodloop` as an approver fails with `ErrApproverReserved`
- Python and TypeScript SDKs: `approve` records a proposal unless the client holds a key of nodloop-server added with `--person`, and answers the status unchanged with `proposal` naming the intervention. `judge` takes the reviewer as its third argument, so `judge(run, verdict, reviewer, ...)` in both, and `outcome` over stdio without a reviewer fails
- MCP: the plugin starts `nodloop mcp --agent`, which lists no `approve`, `reaffirm` or `approve_compaction` and records every verdict and outcome with reviewer `session` whatever the call names. Over stdio `approve`, `reaffirm` and `approve_compaction` always record a proposal, each answering `proposal`. `feedback` and `outcome` over stdio without a reviewer fail
- nodloop-server: only a key of role approver added with `key add --person` decides, under its name. Every key saved before records proposals until you remove it and add it again with `--person`. `key list` adds a column naming who holds each key, and a key named `nodloop` is refused when added and when `serve` starts
- CLI: `knowledge approve`, `retire`, `reaffirm`, `approve-compaction`, `knowledge import` and `config approver <name>` decide only at an interactive terminal outside an agent session. From a script or a Bash call of Claude Code the first four print a `proposal` line and change nothing, and the last two fail. `--approver` falls back to `approver` of config.json and then to `person`. `feedback add` and `feedback outcome` without `--reviewer` fail. `knowledge ask`, `knowledge defer` and `hook wake` are gone, and `immediate` reads as `deferred` from `NODLOOP_SESSION` and config.json while `config session_mode immediate` is refused
- Session: no hook names or asks the waiting drafts any more, so a draft waits until you ask `/nodloop:nod` or run `knowledge waiting`
- The name `nodloop` is reserved for what nodloop carries out: no approve, retire, reaffirm or compaction approval takes it from any caller

### Added
- `nodloop llm set chat --base-url <u> --model <m> [--key-env <ENV>]` points every command that calls a model, such as `knowledge extract`, `knowledge compact` and `llm probe`, at an OpenAI compatible Chat Completions endpoint like Ollama, vLLM, OpenAI, Gemini or a LiteLLM proxy, so background drafts need no claude login. `llm set claude` and `llm unset` go back to `claude -p`. The key is read from the env named and never saved. Every answer is checked against the schema and sent back once when it fails. A config.json that names chat without a model fails like one without a base URL. `nodloop config` lists the backend. The refusal a draft is resent with says the relation is add when the run reaches no item or candidate and lists the label keys the run carries, since a small local model kept the same mistake on the resend until it did
- Every extract trace of `knowledge extract` carries the usage of the drafter and critic calls summed, cost included, and the wall time of the extraction in `duration_ms`
- An extraction reads the waiting candidates the run reaches beside the approved items. A correction that repeats a candidate adds the run to its evidence and keeps its text, with conclusion `corroborated`, so it asks no new question. An update can name a candidate and a conflict names it too. The MCP `extraction` answer adds `candidates`, `propose_extraction` answers `corroborated`, and the extract trace adds `related` with the id, version and status it named
- When approving the candidate of `knowledge extract` would pass the 9000 chars of a run, the same process drafts a compaction of the fullest folder it reaches, checks its coverage and prints a `compaction` line. A pending compaction of that folder is reused and checked when it has no coverage yet
- `nodloop knowledge consolidate [--daily] [--model <m>]` asks the model which waiting candidates say the same lesson in other words, one call per cluster of candidates one run may carry together, and prints one line per group. It only records: a `consolidate` trace per producer with the groups, the candidates read, usage and duration, which `trace list --name consolidate` shows and retention keeps. No candidate is hidden, merged or approved. The stop hook starts `--daily` in a process of its own at most once a day, kept in `consolidate.json`, once two candidates wait
- `report critic` adds a row per member of the reaction point: answers asked and sure, and how they matched the verdict the conversation recorded on the same run as agree, disagree, missed correction, missed approval and deferred. A run whose verdict the reaction point recorded itself counts only as asked
- Every MCP tool lists an outputSchema, and every answer is checked against it before the client reads it. The answers keep their keys
- A session start after `/compact` or `/clear` adds every fitting item of the place again, since the context lost them, and records them as a `refill` trace so the next prompt adds none of them a second time, though `/clear` starts a new session id
- The new `hook subagent` SubagentStart hook adds every fitting item of the place to a subagent, since no prompt hook runs inside one. A session the holdout withholds gives its subagents no item either
- The nod skill approves, retires and reaffirms nothing: when you pick one of those it shows the one `nodloop knowledge` line to run in a terminal of your own
- `nodloop mcp --agent` serves the reviewer role: every tool but `approve`, `reaffirm` and `approve_compaction`, with every verdict and outcome recorded as the session's. `--agent --list` prints its tools
- Every decision and every proposal of the commands and tools that decide is an `intervention` trace naming the action `judge`, the version or compaction, the choice keep, undo or scope, the labels, its `authority`, its `proof` and the person it was recorded under, with the records it wrote, so `trace list --name intervention` shows who decided what
- `nodloop-server key add --person` marks a key a person holds
- Codex runs the plugin through `plugin/.codex-plugin/plugin.json`: the same launcher, skills and MCP server, and the hooks of `plugin/hooks/codex.json`, which are those of Claude Code with `--host codex`. Add it with `codex plugin marketplace add jeon-jihyeon/nodloop` and `codex plugin add nodloop@nodloop`, then trust its hooks in `/hooks`, since Codex skips plugin hooks until you do
- Cursor and Copilot CLI run the session start and the stop from the hook files in `examples/hooks`, copied into `.cursor/hooks.json` or `.github/hooks/`. The session start adds every fitting item, since neither host carries context on a prompt. Neither stop input holds the answer, so their stop records no run
- `nodloop hook <action> --host claude-code|codex|cursor|copilot` reads the input and writes the answer of that agent, and every run of a session carries a `host` label, `claude-code` when the flag is absent. An item scoped without `host` still reaches every host, and `--label host=codex` scopes one to Codex. A host refuses a hook it never runs
- `nodloop otel set --endpoint <url> [--protocol http/protobuf|http/json] [--headers-env <ENV>] [--no-logs] [--content]` saves an OTLP target in config.json once, and from then on every record reaches your tracing tool with no command: the stop hook starts `export otel --pending` in a process of its own after every recorded run, `nodloop mcp` after every run, verdict, outcome, approval and reaffirm, and `knowledge extract` and `knowledge consolidate` after their trace. Claude Code removes every `OTEL_*` variable from hooks and MCP servers, so the standard variables alone start nothing. Headers are read from the variable `--headers-env` names at each export and never saved. `--no-logs` suits Langfuse, Phoenix and Datadog, which ingest no OTLP logs. `nodloop otel unset` stops it and `nodloop otel` prints it
- `export otel` sends OTLP protobuf by default, which Phoenix and Datadog need, or JSON with `--protocol http/json`, with no new dependency. Each record goes once as its own span with ids derived from the record, so an export again after a failure never counts anything twice: a trace per turn as an `invoke_agent <producer>` span from the prompt to the stop with the session as `session.id` and `gen_ai.conversation.id`, the items applied or withheld and the holdout arm, unit and share. Each verdict, outcome and approve or retire of a candidate is an `EVALUATOR` span linked to what it judged, with the GenAI evaluation and the OpenInference annotation keys, plus a `gen_ai.evaluation.result` log record parented to that span. Every extraction, classify, coverage check, compaction draft, replay and consolidate is a trace of its own with an `invoke_agent nodloop.<name>` span and a `chat <model>` child carrying `gen_ai.usage.*` with cache read and write and `llm.cost.total`, linked to the run it judged and to the verdict that caused it. `--pending` sends each record after the mark in `export.json` once and moves the mark
- Outputs, reasons, notes and candidate text go only with `--content` on `otel set` or `--with-content` on `export otel`, which still reads `--with-output`
- The prompt hook records the start of each turn as a `turn` trace with no text, and the stop hook stores how long the turn took as the run's `duration_ms`. A turn a stop hook continued counts from that stop. Retention removes `turn` traces past its cutoff
- `duration_ms` on the MCP tool `run`, `Run.Duration` in Go, `duration_ms` on `record` in Python and `durationMs` in TypeScript give a run its length, and every SDK adapter passes the time from its knowledge fetch to its record. A negative duration fails with `ErrDurationNegative`
- Model call traces name the model the backend answered with, such as `claude-haiku-4-5-20251001` for `haiku`: extract, replay, consolidate and a coverage check through a model. A coverage check records its usage and duration, a replay its duration, and every compaction draft through a model records a `compact` trace with its usage, duration and error, which retention keeps
- `model` on the MCP tool `run`, `Run.Model` in Go and `model` on `record` in both SDKs name the one model a producer runs. A session records none since a conversation may switch models
- The decision point `judge` asks whether your recorded decisions settle a change nodloop prepared and whether it goes against one of them. Every member answers and none is a fallback, since their disagreement is what turns a change into a question for you: three claude variants of one judge configuration, which differ in the order of the anchor lines and in a few-shot set of your earlier decisions drawn once when the configuration starts, plus an endpoint of another model family that `classifier set judge --url <u>` adds. Every variant runs at effort `high` passed to claude as `--effort`, since a default the host lowers, as Opus 5.5 did, would move the answers with nothing on record, and claude takes no temperature for variety. One classify trace keeps every member with the model it answered with, its effort and its answers, and the change and the anchor only as an HMAC. The answers come out as conform, against, undecided or split with the share of conforming members and a confidence bin
- An endpoint in a classify trace names its model: the one it answered with, or else the one it was sent. An answer of NaN is refused like one past 1

### Changed
- `export otel` changed shape for tracing tools: protobuf is the default, a verdict or an outcome is its own evaluation span linked to its run instead of an event on a run span sent again, every verdict and outcome record goes once instead of the newest one, verdict reasons and outcome notes stay home without `--with-content`, `--endpoint` sends traces only, and the line reads `exported <r> runs, <v> verdicts, <o> outcomes, <c> model calls, <q> review questions and <d> decisions`. The span ids of runs stay the same
- The stop hook runs synchronously, so the run of an answer is recorded before the next prompt reads it
- Only the 9000 chars of a run are capped. The cap of ten items per run and the compaction offered at six items are gone, so an eleventh short item approves. The MCP folder answer drops `item_budget` and `compaction_due`, `over_budget` counts chars only, `knowledge approve` prints no compaction line, and `Approval` drops `compaction_due` in Python and `compactionDue` in TypeScript
- `llm probe` asks the configured backend, with haiku as the default model on claude and the configured model on a chat endpoint
- `knowledge waiting` lists one candidate per id, the newest version, with those taught by more corrections first and then the most recently proposed or corroborated
- `report critic --json`, the MCP `report` answer and `GET /v1/reports/critic` hold `{critics, reactions}` instead of an array of critic rows
- A prompt adds only the items the conversation does not hold yet: the first prompt of a session adds every fitting item, and a later one adds only new items and new versions under a lead that says a line replaces an earlier line of the same id. An item approved during a turn is added at the next prompt. The run of each answer still records every fitting item as applied, so the holdout and `report effect` read as before. On the local runs this cut the item text a session received from about 54 times its distinct item text to 1.3 times, counting withheld runs
- The `next` line of `knowledge extract` and the `next` of the MCP `propose_extraction` name no command or tool that decides: a candidate waits for you and is asked once unless your earlier decisions settle it, since a model that read the approve command approved without asking

### Fixed
- A propose from a corrected run without labels, and the keys of a background draft, no longer scope the lesson to the `host` of that run, so a lesson approved in Claude Code reaches the same place in Codex. Name `host` in the labels to keep one to a host
- The classifier rows of `report critic` count only answers of the critic point, so a reaction endpoint no longer shows up as a critic
- A correction you type right after a turn a stop hook continued now lands on the answer it corrects. The stop hook marks the run of such a turn with `continued` in its input, and the prompt note, the reaction point and the background extraction name the newest answer no stop hook continued. Before, they named the continuation's run, and 10 of 10 such corrections in headless sessions landed on the question instead of the answer. A later prompt also counts the items in the context from that answer, so an item approved before the first stop of a continued turn is no longer left out

## 0.9.0 - 2026-10-15

### Added
- A producer outside Claude Code joins the holdout: `holdout_key` on the MCP tool `knowledge_for` and on `run`, `knowledge for --holdout-key` and `run record --withheld --holdout-key`, `Draw`, `WithHoldout` and `Run.HoldoutKey` in Go, and `holdout_key` in Python and `holdoutKey` in TypeScript on `knowledge`, `record` and the adapters. A drawn run records its `unit`, a hash of the key, so every run of one key counts as one draw
- Retention: `nodloop config retention <days>`, `nodloop records prune [--dry-run | --daily]` and a daily prune after a recorded run, `WithRetention`, `WithPruneError` and `WaitPrune` in Go, and a daily prune of every tenant in nodloop-server. A run with a verdict, an outcome or a knowledge citation stays, and so do the first run that received and the newest run that applied each approved item. The stop hook starts the daily prune in a process of its own and the Go client in a goroutine, so neither waits for it. A verdict recorded while a prune waits for the trace file keeps its run. A prune decodes only the fields it reads, which keeps its memory on a 1.66 GB record file under 1 GB. `records prune` refuses `--dry-run` with `--daily` and stops with `another prune is running` when another process holds `prune.json`
- `report effect` rows add `since`, `units`, `expected_share`, `rate` with a 95% Wilson interval, `judged_rate`, audit and outcome counts, `undrawn`, the sample ratio p value `srm_p`, `difference` with a 95% Newcombe interval, `insufficient` and `warnings`. The split check counts draw units, and the rates, their intervals and the judged rate check allow for the runs of one unit being alike, so the turns of a conversation never pass for independent runs. The text adds a first window line and the same columns after the producer
- `report online` adds a `withheld` cohort
- `Outcome`, `Report` and the `Result` type in Go and `outcome` and `report` in both SDKs. `report` returns the report with `since`, the start of the runs it read. Python adds the `Ref`, `Report`, `ReportName` and `Result` types and TypeScript exports `Ref`, `Report`, `ReportName` and `AdapterOptions`
- `export otel` sends `nodloop.holdout.arm`, the applied or withheld items and the newest outcome as an evaluation event
- The MCP `report` answer carries `since` when the report reads a window

### Changed
- `report effect` has two rows per producer with a `producer` field, and counts only runs recorded with a holdout draw. Runs recorded before this release count as `undrawn`
- The conversation hooks draw the holdout once per session instead of once per turn, so a whole conversation is one arm
- Every report reads from the later of the retention cutoff and the cutoff of the last prune of the records. The text of `report loop`, `report critic` and `report effect` opens with a `window` line, `all` when it read every run
- The queue ranks a withheld run on the items it would have received and draws its audit samples from each arm in turn. `new_knowledge` marks only the first run that ever received a version, whatever window the queue reads
- `report critic` counts the classify traces of the retention window
- nodloop-server checks every tenant for a due prune at start and every hour from the mark kept with its records
- The `export otel` line counts the outcome events it sent, one per run
- A TypeScript adapter takes `{labels, holdoutKey}` as its third argument and still takes a plain labels object
- The TypeScript `record` takes `{ id, version }` refs in `applied` as well as items

### Fixed
- A rewrite of a record file by `doctor --repair` writes a temp file and renames it over the file, so a crash or a reader never sees it empty or cut short. A backup an earlier release left from a stopped rewrite is merged back when the records open. Every writer also locks the data file on macOS and Linux, as earlier releases did, so a process of an earlier release never appends while a rewrite reads the file. Restart every nodloop process, the MCP server, Claude Code sessions and any Go service, on this release before the first prune or `doctor --repair`, since a process of an earlier release can still append to the file it opened before the rename and that run is lost

## 0.8.0 - 2026-10-08

### Added
- You pick where a lesson applies when you approve it. The review asks it as a second question, offering every place and the labels of the run it came from, and typed words become the condition of the lesson. `knowledge approve --label` or `--everywhere`, `scope` on the MCP tool `approve`, `ApproveIn` in Go and a `labels` option on `approve` in both SDKs
- `nodloop knowledge ask` prints the review questions of each waiting candidate as AskUserQuestion input, with its content, its scope and what you said on each run that taught it, and the nod skill asks them one candidate at a time

### Changed
- A drafted lesson with no label keys now reaches every run of its producer instead of keeping every label of the run, so a lesson about how you want answers written is no longer tied to one repository
- The drafts waiting for approval are asked about when you start or resume Claude Code through a new SessionStart hook, `nodloop hook start`. The first prompt of a session id asked before, so a resumed session never asked. `/clear`, `/compact` and a fork ask nothing

### Fixed
- The run caps of an approval count the items the fullest run a lesson reaches carries, so a lesson for every repository no longer counts the lessons of all repositories together and is refused for runs that never carry them all

## 0.7.0 - 2026-10-07

### Added
- `Item.Ref` and `Items.Refs` in the Go package, and a runnable example of the loop
- The LangChain middleware in the TypeScript SDK as `nodloop/langchain`
- READMEs for the PyPI and npm pages
- `nodloop help [<command>]`, `-h` and `--help`, printing the usage of one command or action
- `nodloop config` with no action lists every setting with its value and source, and shows a setting it cannot read, such as an invalid `NODLOOP_SESSION`, as an error in its own row
- `nodloop doctor` names corrupt record lines and `--repair` moves them to `<file>.corrupt`, keeping the whole file as `<file>.bak` only until the rewrite is synced
- `nodloop knowledge replay` judges a lesson in one model call against the corrected answers it cites and the newest ten approved answers of its scope recorded before it, and flags a lesson that reaches too far. `knowledge waiting` shows the latest replay, `report replay` lists them, and the nod skill runs it before asking for approval
- `nodloop knowledge check <compaction> --replay` replays every new item of a compaction and totals what it misses and where it reaches too far
- `nodloop export otel` sends runs as OpenTelemetry GenAI spans and the newest verdict of each run as a `gen_ai.evaluation.result` event in OTLP JSON over HTTP, with no new dependency. A run whose newest verdict is a withdraw gets no event
- Reports for other tools to draw: `--json` on `report loop`, `extract`, `critic`, `effect` and `replay`, the MCP tool `report`, and on `nodloop-server serve` `GET /v1/reports/{name}` for these and `health` to a reviewer or approver key, and `GET /healthz` without a key

### Changed
- `Client.Items` and `Client.Waiting` of the Go package return `Items` instead of `[]Item`
- The TypeScript `record` takes one object: `record({ producer, output, labels, applied, subject })`
- `propose` and `approve` return `Candidate` and `Approval` in both SDKs instead of untyped objects. `Approval` carries whether the item holds a veto, whether a compaction is due and the export and folder errors the approve tool answers, as `compaction_due` in Python and `compactionDue` in TypeScript
- The Python `record` takes `labels`, `applied` and `subject` as keywords only: `record(producer, output, labels=..., applied=...)`
- The LangChain middleware of both SDKs fetches the items once per run and keeps them until the run is recorded, and adds them to the system message without dropping its content blocks
- The durations of the loop report are `decide_ns` and `settle_ns` in JSON
- A CLI error names its command once, and a usage error prints the usage of that command alone
- The hooks read only the newest run of their session, from the end of the file and back one week at most, so a session resumed after a week starts without a previous run. With 50,000 runs on an Apple M1 Pro the prompt hook went from 505 ms to 0.7 ms and the stop hook from 528 ms to 1.4 ms, while the first turn of a new session reads the whole week and takes 115 ms in either hook
- An append reads only the last line of the file instead of the whole file
- A corrupt record line is skipped and named instead of failing every read, so a hook still adds the other items
- The holdout draws a turn by the session and its previous run instead of the count of its runs
- A run receives its items most specific first, then the ones a person approved or reaffirmed last, instead of by id, so a cut at the size limit drops the general and stale ones
- Release archives carry a build provenance attestation and an SPDX SBOM. CI tests on macOS too, with the race detector on every package and actions pinned by commit
- The server is a binary of its own, `nodloop-server`, in a Go module of its own under `server/`, so the CLI, the plugin and the Go package carry no server code and no PostgreSQL driver. `nodloop-server key` and `nodloop-server serve` take the flags, keys in config.json and records of the old commands. The release ships `nodloop-server_<os>_<arch>.tar.gz` beside the CLI
- A decision point asks one endpoint and falls back to claude when the endpoint fails or is less than 0.8 sure of any answer. `nodloop classifier set`, `unset`, `list` and `probe <point>` manage it under the config key `decision_points`. A config before 0.7.0 is read as the endpoint each point asked first, an endpoint no decision used stays off, and a parallel setup or a cascade with a threshold other than 0.8 fails with how to set it again. The first `set` or `unset` writes `decision_points` and drops `classifiers` and `decisions` in one write

### Removed
- `nodloop server key` and `nodloop server serve`, now in `nodloop-server`. `nodloop server` prints where they moved
- `nodloop classifier add`, `use`, `reset` and `remove`, with cascades of several endpoints and parallel setups

### Fixed
- The CLI and `nodloop-server` write config.json under the lock file `config.json.lock` beside it, so a key one saves never drops a key the other saved at the same time
- A command named `(` or `)` alone renders quoted for the guard, so it can never read as the start or end of a subshell. Found by fuzzing

## 0.6.6 - 2026-10-07

### Added
- Session modes `deferred`, `immediate`, `manual` and `off` through `config session_mode` or `NODLOOP_SESSION`
- A `withdraw` verdict that takes back the verdict before it, in the CLI, MCP, the Go package and the SDKs
- The SDKs on PyPI and npm, published by every release tag through trusted publishing

### Fixed
- Classifier states lead with the text they judge so an endpoint that truncates long input still sees it
- A lesson update that turns an item into a list of cases is refused

## 0.6.5 - 2026-10-07

### Added
- PostgreSQL stores for `nodloop server serve` through `--postgres` or `NODLOOP_POSTGRES`

### Changed
- Approved vetoes apply only to the producer that approved them

## 0.6.4 - 2026-10-06

### Added
- The Go package `github.com/jeon-jihyeon/nodloop` with `CheckCall`, the MCP tool `check_call` and `nodloop guard call`
- Python and TypeScript SDKs with adapters for LangChain, the OpenAI Agents SDK and the Claude Agent SDK
- `nodloop server serve` over streamable HTTP with hashed keys, tenants and the roles producer, reviewer and approver

## 0.6.3 - 2026-10-06

### Added
- An `extract` trace for every extraction attempt and `report extract` by plugin version and path
- The plugin version on conversation runs and the drafts funnel in `report loop`
- `report critic`, comparing critic judgments with what people decided on the same run
- The scope line in `report loop`, and idle and contested items in `knowledge health`
- `config holdout` and `report effect`
- The classifier point `reaction`, judging the user's message in the prompt hook

## 0.6.2 - 2026-10-06

### Added
- The prompt hook asks about drafts waiting in the session that corrected them
- Classifiers over HTTP at the critic point, managed with `nodloop classifier`
- `immediate` drafting: a correction drafted in the same turn and asked about at the end of the answer
- `config approver` and the totals line of `report loop`

### Fixed
- Session labels come from `CLAUDE_PROJECT_DIR` when the working directory left the project
- A person's verdict stops the background extraction of the same run

## 0.6.1 - 2026-10-03

### Added
- Verdicts the conversation infers from the user's words, with lessons drafted after the turn

## 0.6.0 - 2026-10-02

### Added
- Runs of any producer with labels, verdicts on them and knowledge scoped by producer and labels
- The Claude Code session as the built in producer, `/nodloop:nod` and `report loop`
- Narrowing, promotion and compaction of items with a coverage check
- Lessons drafted from a correction and checked by a critic

### Removed
- The incident data review, which became a plugin of its own
