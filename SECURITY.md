# Security

## Reporting a vulnerability

Report privately through GitHub: open the Security tab of this repository and choose "Report a vulnerability". Do not open a public issue for anything that could be exploited before a fix ships.

Include what you can of the following. A proof of concept is welcome but not required.

- The command or tool involved, such as `nodloop guard`, `nodloop mcp` or a plugin skill
- The version from the release tag or the commit hash
- Steps to reproduce and the impact you expect

You will get an acknowledgement within 7 days and a fix or a decision within 30 days. Credit goes in the release notes unless you ask otherwise.

## Scope

nodloop runs locally. These are the surfaces worth attention.

| Surface | What it does | What is in scope |
|---|---|---|
| `nodloop guard` | Reads Claude Code hook input on stdin and blocks calls that match a veto. `guard install` and `guard uninstall` rewrite `~/.claude/settings.json` after writing a `.bak` backup | A veto file is trusted configuration, so a malicious veto file the user installed is not a vulnerability. A way for a tool call to bypass a matching veto is. So is a change to `settings.json` beyond the nodloop hook |
| `nodloop mcp` | Serves the tools of runs, verdicts and knowledge on stdio to the MCP client that started it. Approving a judgment with a veto writes an approved veto file under `~/.claude/nodloop` that the guard then loads | The client is trusted. Path traversal or command execution through tool arguments is in scope, and so is a veto file written without a named approver |
| Conversation hooks | `nodloop hook prompt` adds approved items to a prompt and `nodloop hook stop` records the last answer as a run with secrets redacted | A way for an answer or a label to make a hook write outside the record directory, block a prompt or keep a secret the redaction should remove is in scope |
| Input files | Reads files named by `run record --output`, `knowledge import --file` and `feedback add --edited` | A crafted file that escapes its path or executes anything is in scope |
| Records and config | Writes runs, feedback, outcomes, knowledge and coverage checks to the record directory and reads `~/.nodloop/config.json` | A write outside those paths is in scope |
| Model calls | `llm probe`, `knowledge compact`, `knowledge check` and `knowledge extract` run `claude -p`, or the binary `NODLOOP_CLAUDE_BIN` names | A way for a record or model output to change that command line is in scope |

The `plugin/bin/nodloop` launcher downloads a release archive from this repository over HTTPS into `~/.nodloop/bin` and checks it against `checksums.txt`. A way to make it fetch or run something else is in scope.

Model output is untrusted. Every proposed knowledge item waits for a named approval before a prompt receives it. That gate is a design property, not a security boundary. Do not rely on it to contain hostile model output.

## Supported versions

Only the latest release receives fixes.
