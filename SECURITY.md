# Security

## Reporting a vulnerability

Report privately through GitHub: open the Security tab of this repository and choose "Report a vulnerability". Do not open a public issue for anything that could be exploited before a fix ships.

Include what you can of the following. A proof of concept is welcome but not required.

- The command or tool involved, such as `nodloop guard`, `nodloop mcp` or a plugin skill
- The version from the release tag or the commit hash
- Steps to reproduce and the impact you expect

You will get an acknowledgement within 7 days and a fix or a decision within 30 days. Credit goes in the release notes unless you ask otherwise.

## Scope

nodloop runs locally and touches three things worth attention.

| Surface | What it does | What is out of bounds |
|---|---|---|
| `nodloop guard` | Reads Claude Code hook input on stdin and blocks calls that match a veto | A veto file is trusted configuration. A malicious veto file the user installed is not a vulnerability. A way for a tool call to bypass a matching veto is |
| `nodloop mcp` | Serves review tools on stdio to the MCP client that started it | The client is trusted. Path traversal or command execution through tool arguments is in scope |
| Reference data and records | Reads `events.csv` and runbooks from the data directory set by `nodloop setup --data-dir`, writes traces and feedback to the record directory | Crafted data files that escape those directories or execute anything are in scope |

The `plugin/bin/nodloop` launcher downloads a release archive from this repository over HTTPS. A way to make it fetch or run something else is in scope.

Model output is untrusted. Every diagnosis and every proposed knowledge item waits for a human verdict before it is used again. That gate is a design property, not a security boundary. Do not rely on it to contain hostile model output.

## Supported versions

Only the latest release receives fixes.
