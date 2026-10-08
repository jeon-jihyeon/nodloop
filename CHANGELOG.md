# Changelog

Every release tag publishes the plugin, the darwin and linux binaries of `nodloop` and, from 0.7.0, `nodloop-server`, and both SDKs under one version. Versions before 0.6.0 reviewed incident data, which left this repository in 0.6.0.

## Unreleased

### Changed
- The drafts waiting for approval are asked about when you start or resume Claude Code through a new SessionStart hook, `nodloop hook start`. The first prompt of a session id asked before, so a resumed session never asked. `/clear`, `/compact` and a fork ask nothing

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
