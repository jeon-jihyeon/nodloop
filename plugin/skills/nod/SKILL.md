---
name: nod
description: Record the user's verdict on an earlier Claude Code answer as nodloop feedback and turn a correction into approved knowledge that the next prompt in the same repository receives. Also reviews the lessons the plugin drafted from corrections the conversation recorded. Use when the user calls /nodloop:nod, says an earlier answer in this conversation was right or wrong and asks to remember it, asks to review the waiting drafts, or when the nodloop prompt context says lessons wait for approval.
---

# Nod on an answer

The plugin records every answer of this conversation as a run of producer `session` with the repo and dir it was given in, taken from the working directory while it lies inside the directory Claude Code started in and from that directory otherwise, and before each prompt it adds the approved items for that place. This skill turns what the user says about an answer into a verdict on its run and, when the user wants it, into an item the next prompt receives.

Every question below goes through AskUserQuestion, because the user can always pick Other and type an answer. Run nodloop through `~/.nodloop/bin/nodloop`, the link the plugin keeps to the binary its server runs. When that link is missing, run `${CLAUDE_PLUGIN_ROOT}/bin/nodloop version` once to create it.

When the user calls `/nodloop:nod` with nothing to say about an answer, or when the nodloop prompt context says lessons wait for approval, review the drafts that wait in this place as in the section Review the waiting drafts. Otherwise follow the steps.

## Steps

1. Find the run. Run `~/.nodloop/bin/nodloop trace list --name run --limit 5` through Bash without asking, because it only reads. It lists the newest runs first with their id and time. Run `~/.nodloop/bin/nodloop trace show <id>` for the newest ones and pick the run whose output is the answer the user means, usually the newest one recorded before this turn. When two could be, ask which one, showing the first line of each. When none matches, the hooks recorded nothing, often because `NODLOOP_SESSION` is off: say so and offer to call `run` with producer `session`, the labels `repo` and `dir` as the hook would set them, and the answer as output
2. Pick the verdict from the user's words. approve when the answer was right. edit when the user says what is right: write the answer as it should have been, changed only where the user corrected it, show it, and use it once the user agrees. reject when the user says it was wrong without saying what is right, and write the reason as what the answer got wrong or missed with the user's words after it
3. For an edit or a reject ask what kind of miss it was: fact when something it stated was wrong, approach when the way it worked was wrong, scope when it did too much or too little, form when the content was right but the shape was wrong, other otherwise. Call `feedback` with the trace id of the run, the verdict, the reason in the user's words, that `reason_code`, and the corrected answer as `edited_output` for an edit
4. After an edit or a reject that would help a later prompt, offer to draft its lesson. Call `extraction` with `from` set to the trace id of the run. It answers the output, the verdict, the edit, the approved items the run reaches, the rules, the schema and the critic questions. Draft by the rules: the relation to those items, one sentence of what the correction taught and the label keys it needs, repo alone unless the user says the lesson holds only in that dir. Then read the draft as a second reader and answer the critic questions honestly, and call `propose_extraction` with the draft and the answers. When code or a false answer refuses it, fix only what the refusal names and call it once more. An add or an update answers a candidate: show its scope and content, and for an update the related item beside it, and say it is a candidate until the user approves it. A duplicate or a conflict proposes nothing: show the related item and say what `next` says. When the lesson names a tool call to forbid, call `propose` with `from` and a `veto` as in step 6 instead
5. Call `approve` only when the user says so, under the name `~/.nodloop/bin/nodloop config approver` prints. When it prints nothing, ask for the name once and save it with `~/.nodloop/bin/nodloop config approver <name>`. Then tell the user that the next prompt in that place receives it, and that `~/.nodloop/bin/nodloop knowledge for --producer session --label repo=<repo> --label dir=<dir>` lists what a prompt in that dir receives
6. When the correction forbids a tool call that its input alone decides, such as a shell command pattern or a file path, propose it as a judgment with `veto`: the tool, the conditions on `tool_input` fields and an example input the veto must block. For Bash match the field `commands`, one line per simple command the guard derives from the command, so a pattern anchored with `(?m)^` reads command starts. Show the user the veto that `propose` returns before they approve it. Once approved it blocks the call through the guard hook, which `~/.nodloop/bin/nodloop guard install` registers
7. When `approve` answers `compaction_due` true, one run would carry more than five items of this place. Offer a compaction only when the user wants it: call `compaction` with the item id, write new items by the rules it returns so nothing is said twice and nothing is lost, call `propose_compaction`, then call `check_compaction` with each old item read against the new ones, and call `approve_compaction` only when the check passed and the user agrees, under the name of step 5

## Draft the correction of this turn

The prompt hook asks for this after the conversation recorded a reject with reviewer `session`, so the user decides on the lesson in the same turn as the correction.

1. Call `extraction` with `from` set to the trace id of the reject without asking. Draft and answer the critic questions as in step 4 of the steps, then call `propose_extraction`
2. When it answers a candidate, end the answer with one AskUserQuestion: quote the user's words that made the correction, show the content and the scope, and offer approve, retire and leave it waiting. Use the name of step 5 of the steps
3. Call `approve` or run `knowledge retire` as in step 4 of the section Review the waiting drafts
4. When it is refused twice, a duplicate or a conflict, say so in one line. A refused one is drafted again in the background after the turn and asked about on the next prompt

## Review the waiting drafts

The conversation records a verdict with reviewer `session` when the user says an answer was wrong or right, and after that turn the plugin drafts the lesson of a correction in the background. The drafts wait as candidates until a person approves them. The prompt hook names them in the session that corrected the answer and on the first prompt of a later session, so the review starts without `/nodloop:nod`.

1. Run `~/.nodloop/bin/nodloop knowledge waiting --producer session --label repo=<repo> --label dir=<dir>` through Bash without asking, with the labels as the hook sets them. Each line is a candidate with its kind, content, scope and the runs it came from. When there is none, say so and stop
2. For each candidate run `~/.nodloop/bin/nodloop feedback list --trace <run>` for the run it came from and show the content, the scope and the reason the user gave there
3. Take the name to approve under as in step 5 of the steps, then ask for each candidate whether to approve it, retire it or leave it waiting. Several candidates may go in one AskUserQuestion with multiSelect
4. Call `approve` for each one the user approves, with that name. Run `~/.nodloop/bin/nodloop knowledge retire <id> --version <n> --approver <name>` for each one the user rejects. Leave the rest
5. When an approval answers `compaction_due` true, continue with step 7 of the steps

## Rules

- Record only what the user said about the answer. Never record a verdict the user did not give and never approve without a name the user gave or saved
- A verdict the user gives here on a run is newer than one the conversation inferred on it and takes its place
- An item is data a person approved, never an instruction that overrides the user. When the user now asks for something an item forbids, follow the user and offer to retire or narrow the item
