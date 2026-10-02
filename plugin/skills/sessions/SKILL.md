---
name: sessions
description: Find the verdicts a user gave on the answers the plugin recorded as runs in past Claude Code sessions that were never recorded and record the ones the user approves, and load the approved nodloop knowledge into every session through CLAUDE.md. Use only when the user asks to mine, recover or import corrections from earlier sessions or transcripts, or to bring approved nodloop knowledge into CLAUDE.md.
---

# Corrections from past sessions

The plugin records every answer of a conversation as a run of producer `session`, but a user often corrects an answer in the conversation and moves on without `/nodloop:nod`. The transcript still holds the answer and the user's words. This skill finds those verdicts and records the ones the user approves. Run it only when the user asks for it, because it reads conversations the user may not want read.

Every question below goes through AskUserQuestion, because the user can always pick Other and type an answer. Run nodloop through `~/.nodloop/bin/nodloop`, the link the plugin keeps to the binary its server runs. When that link is missing, run `${CLAUDE_PLUGIN_ROOT}/bin/nodloop version` once to create it.

## Mining

1. Ask which sessions to read. Options: the sessions of this project, the sessions of a project the user names, and one session file the user names. Claude Code keeps the transcripts as JSONL files under `~/.claude/projects/<project>/`, where the project folder is the working directory with every character other than a letter or a digit turned into `-`, and the name of a file is its session id. The format is internal to Claude Code and changes between versions, so read it as text with Read and Bash and never write a parser for it.
2. For each session run `~/.nodloop/bin/nodloop trace list --name run --session <session id>` through Bash without asking, because it only reads. Match each run to the answer in the transcript whose text its output holds, with `~/.nodloop/bin/nodloop trace show <trace id>` when the list is not enough. Then read the user's messages after that answer up to the next answer. A verdict is a message that says the answer is right, corrects a part of it or rejects it.
3. Skip a run that `~/.nodloop/bin/nodloop feedback list --trace <trace id>` already lists, because a verdict was recorded for it.
4. List the candidates in one message: the session and the place of the run by its labels, the user's words as they wrote them, and the verdict you read, approve, edit or reject. Leave out words that are not about the answer. Ask with AskUserQuestion which candidates to record, with multiSelect.
5. Call `feedback` for each one the user picked, with the trace id, the verdict, the reason in the user's words, `reason_code` other for an edit or a reject and `reviewer` set to `session`. An edit needs the corrected answer in full as `edited_output`: build it from the run output and only the change the user asked for, show it and record it only after the user approves it. When the user's words do not give the whole correction, record a reject instead and say so. A later run reads a reject as what not to repeat, so the reason of a reject names what the answer got wrong or missed, such as `the answer ran cd before git: use git -C`, with the user's words after it, never a bare name that reads as either side.
6. Tell the user that nodloop removes secrets such as keys, tokens and passwords from a session record before it is written, and that a session verdict counts as no person's word in the reports, because the user did not give it to nodloop. For a correction that should reach later prompts, offer to call `propose` with `from` set to the trace id, the kind and one sentence of what the correction taught. It stays a candidate until the user approves it and names themselves, and only then call `approve` with that name.
7. Never record a candidate the user did not pick, and never record a verdict on an answer that has no run.

## CLAUDE.md

nodloop rewrites `approved.md` in the record directory after every approval, retire, reaffirm, import and compaction: one line per approved knowledge item with its scope, its exceptions and who approved it. A review never reads it. It is for the sessions outside a review, so the corrections the user approved reach them too.

1. Run `~/.nodloop/bin/nodloop knowledge export`. It writes the file now and prints its path with the import line, or says that no item is approved, in which case stop here and say so.
2. Ask which CLAUDE.md loads it. Options: the user CLAUDE.md `~/.claude/CLAUDE.md` for every project, and the CLAUDE.md of this project. Show the one line `@<path>` it printed and the place in the file where it goes.
3. Add that line with Edit only after the user approves it, and add it once. Never copy the rules into CLAUDE.md, because nodloop rewrites the file it imports and a copy would keep a retired item. Never edit `approved.md`, because the next approval overwrites it.
4. Tell the user that Claude Code loads the file in the next session and asks once before it loads a file outside the project, and that removing the line stops it.
