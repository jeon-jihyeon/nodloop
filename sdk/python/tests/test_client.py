import pytest
from mcp.shared.exceptions import MCPError

from nodloop import Client, ToolError

ACME = {"tenant": ["acme"], "task": ["refund"]}
NO_RM = {
    "tool": "Bash",
    "when": [{"field": "commands", "match": "(?m)^rm .*/data"}],
    "example": {"command": "rm -rf /data"},
}


async def test_loop(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", reason="the refund window was missing", reason_code="scope")
        proposed = await c.propose("judgment", "Quote the refund window before the steps", from_run=run, item_id="refund-window")
        approved = await c.approve(proposed.id, proposed.version, "ann")

        acme = await c.knowledge("support-bot", ACME)
        globex = await c.knowledge("support-bot", {"tenant": ["globex"], "task": ["refund"]})
        nxt = await c.record("support-bot", "The window is 30 days. Steps follow", labels=ACME, applied=acme.items)

    assert (approved.veto, approved.compaction_due, approved.export_error, approved.folder_error) == (False, False, "", "")
    assert [i.id for i in acme.items] == ["refund-window"]
    assert acme.text.startswith("nodloop: corrections a person approved")
    assert "- [refund-window v1 judgment] Quote the refund window before the steps" in acme.text
    assert globex.items == [] and globex.text == ""
    assert nxt


async def test_refusal_is_a_tool_error(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "answer", labels=ACME)
        with pytest.raises(ToolError, match="needs an edit or a reject"):
            await c.propose("judgment", "x", from_run=run)


@pytest.mark.parametrize(
    ("tool", "arguments", "action"),
    [
        ("Bash", {"command": "cd / && rm -rf /data"}, "block"),
        ("Bash", {"command": "ls /data"}, "allow"),
        ("Read", {"file_path": "/data"}, "allow"),
    ],
)
async def test_check_call(binary: str, records: str, tool: str, arguments: dict, action: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("ops-bot", "ran rm -rf /data", labels={"env": ["prod"]})
        await c.judge(run, "reject", reason="never delete data")
        proposed = await c.propose("judgment", "Never delete under /data", from_run=run, item_id="no-rm-data", veto=NO_RM)
        approved = await c.approve(proposed.id, proposed.version, "ann")

        decision = await c.check_call(tool, arguments)

    assert approved.veto
    assert decision.action == action
    assert decision.allowed == (action == "allow")


async def test_server(server: tuple[str, str]) -> None:
    url, key = server
    async with Client(url=url, key=key) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", reason="the refund window was missing")
        proposed = await c.propose("judgment", "Quote the refund window", from_run=run, item_id="refund-window")
        approved = await c.approve(proposed.id, proposed.version, "mallory")
        acme = await c.knowledge("support-bot", ACME)

    assert (approved.id, approved.version, approved.status, approved.approver) == ("refund-window", 1, "approved", "ann")
    assert (proposed.id, proposed.status) == ("refund-window", "candidate")
    assert [i.id for i in acme.items] == ["refund-window"]


async def test_server_refuses_a_wrong_key(server: tuple[str, str]) -> None:
    url, _ = server
    with pytest.raises(MCPError):
        async with Client(url=url, key="nl_wrong") as c:
            await c.knowledge("support-bot", ACME)


async def test_record_takes_keywords_after_output() -> None:
    with pytest.raises(TypeError):
        await Client().record("support-bot", "answer", ACME)  # type: ignore[misc]


async def test_approval_says_a_compaction_is_due(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", reason="the refund window was missing")
        approvals = []
        for n in range(6):
            proposed = await c.propose("judgment", f"Refund rule number {n}", from_run=run, item_id=f"rule-{n}")
            approvals.append(await c.approve(proposed.id, proposed.version, "ann"))

    assert [a.compaction_due for a in approvals] == [False] * 5 + [True]
    assert all(a.folder_error == "" for a in approvals)


async def test_approve_in_picked_labels(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.record("support-bot", "ok", labels={"tenant": ["globex"]})
        await c.judge(run, "reject", reason="too long", reason_code="form")
        proposed = await c.propose("judgment", "Answer in three lines", from_run=run)
        approved = await c.approve(proposed.id, proposed.version, "ann", labels={})
        globex = await c.knowledge("support-bot", {"tenant": ["globex"]})

    assert approved.status == "approved"
    assert [i.id for i in globex.items] == [proposed.id]
