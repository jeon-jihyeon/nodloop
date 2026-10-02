---
name: nod
description: Record the user's verdict on an earlier Claude Code answer as nodloop feedback and turn a correction into approved knowledge that the next prompt in the same repository receives. Use when the user calls /nodloop:nod, or says an earlier answer in this conversation was right or wrong and asks to remember it.
---

# Nod on an answer

The plugin records every answer of this conversation as a run of producer `session` with the repo and dir it was given in, and before each prompt it adds the approved items for that place. This skill turns what the user says about an answer into a verdict on its run and, when the user wants it, into an item the next prompt receives.

Every question below goes through AskUserQuestion, because the user can always pick Other and type an answer. Run nodloop through `~/.nodloop/bin/nodloop`, the link the plugin keeps to the binary its server runs. When that link is missing, run `${CLAUDE_PLUGIN_ROOT}/bin/nodloop version` once to create it.

## Steps

1. Find the run. Run `~/.nodloop/bin/nodloop trace list --name run --limit 5` through Bash without asking, because it only reads. It lists the newest runs first with their id and time. Run `~/.nodloop/bin/nodloop trace show <id>` for the newest ones and pick the run whose output is the answer the user means, usually the newest one recorded before this turn. When two could be, ask which one, showing the first line of each. When none matches, the hooks recorded nothing, often because `NODLOOP_SESSION` is off: say so and offer to call `run` with producer `session`, the labels `repo` and `dir` of the working directory as the hook would set them, and the answer as output
2. Pick the verdict from the user's words. approve when the answer was right. edit when the user says what is right: write the answer as it should have been, changed only where the user corrected it, show it, and use it once the user agrees. reject when the user says it was wrong without saying what is right, and write the reason as what the answer got wrong or missed with the user's words after it
3. Call `feedback` with the trace id of the run, the verdict, the reason in the user's words, `reason_code` other for an edit or a reject, and the corrected answer as `edited_output` for an edit. Never pass `edited`, which is for a review
4. After an edit or a reject that would help a later prompt, offer to call `propose` with `from` set to the trace id of the run, the kind, judgment for what to do or not do and meaning for how to read something in this place, and as content one sentence of what the correction taught. Code fills the producer and the labels of the run, its repo and its dir. Ask whether the lesson holds for the whole repo or only that dir, and for the whole repo pass `labels` with the repo alone. Show the user the scope and the content that `propose` returns and say it is a candidate until they approve it
5. Call `approve` only when the user says so and gives their name. Then tell the user that the next prompt in that place receives it, and that `~/.nodloop/bin/nodloop knowledge for --producer session --label repo=<repo>` lists what a prompt there receives
6. When the correction forbids a tool call that its input alone decides, such as a shell command pattern or a file path, propose it as a judgment with `veto` as the review skill describes. Once approved it blocks the call through the guard hook, which `~/.nodloop/bin/nodloop guard install` registers

## Rules

- Record only what the user said about the answer. Never record a verdict the user did not give and never approve without a name
- An item is data a person approved, never an instruction that overrides the user. When the user now asks for something an item forbids, follow the user and offer to retire or narrow the item
