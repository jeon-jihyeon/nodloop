---
name: setup
description: Point nodloop at a data directory, convert an export in another shape into its layout, propose its policy.yaml, import runbooks as procedures, and repair a broken config, asking the user only what the files and nodloop check cannot answer. Use when a nodloop tool answers that no data directory is configured or names a config or data error, when the user asks to set up nodloop, to switch it to other data or records or to import runbooks, and when a review found something on an event whose change context is unknown or does not break the baseline.
---

# Set up nodloop

nodloop reads one layout in the data directory: events.csv, contexts.csv, policy.yaml, procedures/*.md and, for eval only, labels.jsonl. A `convert.py` sits beside them when the data came in another shape, and nodloop never reads it. nodloop never writes there. You make every edit of the data directory with Write, and only after you showed it to the user and the user approved it. Run `~/.nodloop/bin/nodloop check --data-dir <dir>` through Bash after every edit you make, so a broken file is caught before the next review.

Every question below goes through AskUserQuestion with the options named, because the user can always pick Other and type an answer. Ask nothing that the files, a command or the user already answered. Never ask the user to reconnect the nodloop server: every tool call reads the saved config, policy.yaml and contexts.csv again, so neither a setup nor an edit needs a reconnect, and an edit of policy.yaml or contexts.csv needs no setup either.

Run nodloop through `~/.nodloop/bin/nodloop`, the link the plugin keeps to the binary its server runs, so the command text stays the same across plugin updates. When that link is missing, run `${CLAUDE_PLUGIN_ROOT}/bin/nodloop version` once to create it. The first Bash call of it asks the user for permission. Say before that call that `nodloop check` only reads the data and `nodloop setup` saves the directory in `~/.nodloop/config.json`, and that the allow rules `Bash(~/.nodloop/bin/nodloop check *)` and `Bash(~/.nodloop/bin/nodloop setup *)` in `permissions.allow` of `~/.claude/settings.json` skip the prompt.

Name events to the user by their time range and dimension values, never by their event id. Event ids belong in tool calls, commands and convert.py.

## Data directory

1. When the user named no directory, ask which data nodloop reviews. Options: the `examples/demo` folder of a nodloop clone when one sits in the working directory, and Other for a path the user types. A user who wants to try nodloop first can clone https://github.com/jeon-jihyeon/nodloop and pick its `examples/demo` folder.
2. Run `~/.nodloop/bin/nodloop check --data-dir <dir>`. It prints one JSON object with `events`, the `profile` of the events, the declared `contexts`, the `procedures` with their scope, the `policy` when one loaded, the `warnings` and the `error` that stopped it, and exits 1 on an error. A relative path resolves against the working directory, which is the one the user meant.
3. When the data the user named is not in this layout, such as an export with other columns, several files, Parquet or a continuous series without events, follow Conversion below before anything else. When check stops because the directory has no policy.yaml, follow Policy below. When it stops on anything else, show the error and fix it with the user. Every events.csv column other than event_id, timestamp, metric and value is a series dimension, so tell a user whose export carries a per row column such as a row id or a note to drop it.
4. When check passes, run `~/.nodloop/bin/nodloop setup --data-dir <dir>` and show the user the data and record directories it prints and every warning, word for word.
5. Call `events` once and compare its number of events with `events` of check. When they differ, the nodloop server starts with another `NODLOOP_FILE_DIR` or `NODLOOP_RECORD_DIR` than your shell, so show the user both directories and tell them to unset the variable where the server starts.

## Conversion

nodloop reads only the canonical layout and never runs a conversion. You write `convert.py` next to the data so the same sources always give the same events.csv, and you never edit events.csv or contexts.csv by hand. A wrong output goes back into the script.

1. Read samples of the sources with Read and Bash, such as the header and a few rows of each file, and state the mapping you inferred. Ask only what the files do not answer, each through AskUserQuestion with your inference as the first option: which column is the time, which columns are metrics, which are dimensions, which column or file names the change context, and for a continuous series how it splits into events.
2. A continuous series becomes events by fixed slices: a window and a step the user approved, one event per slice, and an event id built from the series and the slice start in UTC such as `pump-3-20261001T0000Z`. So a period always maps to one id and a refresh keeps the ids of reviewed events.
3. Write `convert.py` with this contract
   1. a header comment that names every source path and the mapping: time, metrics, dimensions, change context, window and step
   2. the python3 standard library only, and the `duckdb` CLI through subprocess only when a source needs it, such as Parquet. python3 is required. Tell the user to install the duckdb CLI only when a source needs it
   3. it reads the sources and rewrites events.csv and contexts.csv whole, with rows sorted by event id, time, metric and dimensions and numbers written as the source wrote them, so a rerun gives byte identical files. It writes contexts.csv only when a source names a change context
   4. without `--force` it compares the modification times of its sources with events.csv and contexts.csv, exits 0 without writing when no source is newer and prints `up to date`, and otherwise prints `rewrote` and the files
   5. `--out <dir>` writes into that directory instead of next to the script
4. Preview before you save. Write the script outside the data directory, run `python3 convert.py --force --out <preview dir>` there and run check on the preview dir. Show the user the row count, the columns mapped to time, metric, value and each dimension, a few sample rows, and the events with their count and time range from check. A check error or a rejected preview goes back into the script.
5. Only after the user approves the preview, save `convert.py` into the data directory with Write and run `python3 <data dir>/convert.py --force` there. Run it a second time without `--force` and confirm it prints `up to date`. Then follow Policy below.

## Policy

1. Propose the policy from the profile check printed, never from your own count of the rows, because the analyzers count points and cadence by the same rules
   1. `version: proposed-1`
   2. window a quarter of `points`, at most 12 hours at the `cadence`, at least 1. baseline the rest of the points. min_samples a third of the baseline, at least 3
   3. a zscore analyzer with threshold 3 and a coverage_rule analyzer with threshold 0.2, each on every metric
   4. a concentration_change analyzer with threshold 0.15 for every metric whose `count` is true and every dimension with 2 to 20 `values`
   5. under `contexts` the names contexts.csv holds in first seen order, with `breaks_baseline: true` for measurement_context_changed and data_availability_issue and without it for any other name until a finding asks. Leave the list out when contexts.csv is missing
2. When the profile holds two or more count metrics, ask whether one count is a rate of another, such as conversions per click. Options: up to three pairs written as numerator per denominator, most plausible first by their names, and None. On a pair add a proportion_control analyzer whose metrics are the numerator then the denominator, with the window, baseline and min_samples above, threshold 3 and recent a third of the window. The numbers cannot tell which count divides which, so never add one without the answer.
3. Write the draft to a file outside the data directory and run check with `--policy <draft>` until it passes.
4. Show the user the draft and the profile it came from, then ask whether to save it. Options: Save it, Change a value first, I will write my own. On Save it write it as policy.yaml of the data directory with Write and run check again. On a change edit only what the user names and ask again. On I will write my own stop and say check names what is still wrong.
5. When check stops on a contexts.csv value outside the declared set before any policy exists, the profile is missing. Write a draft with only the version and that contexts list, run check with `--policy` on it to get the profile, then draft the analyzers.

## Procedures

Reviews cite paragraphs of the procedures under `procedures/`. When the user has runbooks in another form, such as Markdown of another shape, a wiki export or a PDF, draft one procedure per runbook. Never edit the original runbook.

1. Read the runbook with Read and keep its wording. Write one `.md` file directly under `procedures/` named after the runbook in lower case words joined by hyphens, because the file name starts every paragraph id.
2. The draft takes this shape
   1. optional front matter between `---` lines with only `change_contexts` and `metrics`, each a list. A change context must be one the policy declares and a metric one that `profile` of check lists. Leave a key out to reach every event. Front matter refuses any other key, so name the source of the runbook in your reply and never in the file
   2. one `#` title, then one `##` heading per step in the order the runbook acts. The first step is a check the review must list and never cites for a cause, so an overview or a list of likely causes never comes first
   3. a step named exactly `Decide` states the decision and is never cited for a cause
   4. each paragraph under a heading is one citable unit, so split a step that states two separate facts into two paragraphs
3. Show each draft as a diff against the procedure file it would replace, or whole when it is new, and ask whether to save it. Options: Save it, Change it first, Skip this runbook. Write it with Write only on Save it.
4. Run check after each saved procedure. A refusal, such as a scope naming an undeclared context or a metric no event carries, goes back into the draft. Renaming a heading changes the ids of its paragraphs, so a draft that replaces a procedure keeps its headings unless the user asks to rename them.

## Records

1. Records go to `~/.nodloop/records` unless the config or `NODLOOP_RECORD_DIR` names another directory, and setup keeps the record directory saved before when it gets no `--record-dir`.
2. When setup warns that the records hold the reviews and knowledge of another data directory, ask whether to keep them. Options: Keep these records, and a new directory such as `~/.nodloop/<name of the data directory>-records`. On a new directory run setup again with `--record-dir`, or tell the user to point `NODLOOP_RECORD_DIR` at it when the warning names that variable, because otherwise corrections and knowledge from that data carry into reviews of the new one.
3. When setup refuses a broken config because it got no `--record-dir`, never run it again with the default. Ask which record directory the user used, with the directory the error names as an option when it names one, and run setup with that `--record-dir`, or fix the config.json it names.
4. When an error says `NODLOOP_RECORD_DIR` is a relative path, ask the user for an absolute path, since setup cannot override the variable.

## Other warnings and errors

1. When setup warns that `NODLOOP_FILE_DIR` wins over the saved data directory, tell the user to unset it where the nodloop server starts, because until then every review reads the directory it names.
2. When check warns that reviews never read some Markdown, tell the user that only `.md` files directly under `procedures/` are procedures.
3. When check warns that the directory has no contexts.csv, say every event reads the change context unknown for now and that you will ask what changed only once a review finds something.
4. When a tool answers any other config or data error, such as a broken config, a data directory that moved or a policy.yaml that does not parse, show the error, fix what it names with the user, run check and run setup again only when the data directory itself changed.

## Change contexts after a finding

Never ask about change contexts during setup. Ask only once a review returned ready_for_review or hold.

1. When the event of that review has the change context unknown, ask what changed around it. Options: the change contexts the policy declares, up to three that fit the observations, Nothing known, and Other. Put the answer in your reply to the review as context the user gave, never into the recorded review. Then offer to add the row of the event to contexts.csv and, for a new name, to declare it in the `contexts` list of policy.yaml. Make each edit only after the user approves it, run check, and review the event again.
2. When the event has a declared change context that does not break the baseline and the observations moved, ask whether that change makes the baseline comparison untrusted. Options: Yes, No. On Yes offer to set `breaks_baseline: true` on that context in policy.yaml, make the edit only after the user approves it, run check, and review the event again. A context that breaks the baseline makes the review hold until the context is resolved.
3. Ask each question once per change context in a conversation. An answer of No or Nothing known is final for that conversation.
