# Changelog

Every release tag publishes the plugin, the darwin and linux binaries and both SDKs under one version. Versions before 0.6.0 reviewed incident data, which left this repository in 0.6.0.

## Unreleased

### Added
- `Item.Ref` and `Items.Refs` in the Go package, and a runnable example of the loop
- The LangChain middleware in the TypeScript SDK as `nodloop/langchain`
- READMEs for the PyPI and npm pages
- `nodloop help [<command>]`, `-h` and `--help`, printing the usage of one command or action
- `nodloop config` with no action lists every setting with its value and source
- `nodloop doctor` names corrupt record lines and `--repair` moves them to `<file>.corrupt`
- `nodloop export otel` sends runs as OpenTelemetry GenAI spans and verdicts as `gen_ai.evaluation.result` events in OTLP JSON over HTTP, with no new dependency
- Reports for other tools to draw: `--json` on `report loop`, `extract`, `critic` and `effect`, the MCP tool `report`, and `GET /v1/reports/{name}` and `GET /healthz` on `nodloop server serve`

### Changed
- `Client.Items` and `Client.Waiting` return `Items`
- The TypeScript `record` takes one object: `record({ producer, output, labels, applied, subject })`
- `propose` and `approve` return `Candidate` and `Approval` in both SDKs instead of untyped objects
- The durations of the loop report are `decide_ns` and `settle_ns` in JSON
- A CLI error names its command once, and a usage error prints the usage of that command alone
- The hooks read only the newest run of their session, from the end of the file and back one week at most. With 50,000 runs the prompt hook went from 503 ms to 0.8 ms and the stop hook from 539 ms to 2.4 ms
- An append reads only the last line of the file instead of the whole file
- A corrupt record line is skipped and named instead of failing every read, so a hook still adds the other items
- The holdout draws a turn by the session and its previous run instead of the count of its runs
- Release archives carry a build provenance attestation and an SPDX SBOM. CI tests on macOS too, with the race detector on every package and actions pinned by commit

### Fixed
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
