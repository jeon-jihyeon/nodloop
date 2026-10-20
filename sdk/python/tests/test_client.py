import json
from pathlib import Path

import pytest
from mcp.shared.exceptions import MCPError

from nodloop import Client, Ref, ToolError

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

    assert (approved.veto, approved.export_error, approved.folder_error) == (False, "", "")
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


async def test_holdout_key_withholds_and_record_joins_the_withheld_arm(binary: str, records: str, home: Path) -> None:
    (home / ".nodloop").mkdir(exist_ok=True)
    (home / ".nodloop" / "config.json").write_text('{"holdout": 0.5}')
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag rose", labels=ACME)
        await c.judge(run, "reject", reason="name the partition")
        proposed = await c.propose("judgment", "Name the partition", from_run=run, item_id="partition")
        await c.approve(proposed.id, proposed.version, "ann")
        drawn = [await c.knowledge("stream-bot", ACME, holdout_key=f"window-{n}") for n in range(200)]
        unkeyed = await c.knowledge("stream-bot", ACME)
        n, withheld = next((n, k) for n, k in enumerate(drawn) if k.withheld)
        await c.record("stream-bot", "lag rose again", labels=ACME, withheld=withheld.withheld, holdout_key=f"window-{n}")
        effect = (await c.call("report", {"name": "effect"}))["report"]

    assert 60 <= sum(1 for k in drawn if k.withheld) <= 140
    assert all((k.items == [] and k.text == "") for k in drawn if k.withheld)
    assert withheld.withheld == [Ref("partition", 1)]
    assert unkeyed.withheld == [] and [i.id for i in unkeyed.items] == ["partition"]
    rows = [r for r in effect if r.get("producer") == "stream-bot" and r["arm"] == "withheld"]
    assert [(r["runs"], r["judged"], r["undrawn"]) for r in rows] == [(1, 0, 0)]


async def test_record_takes_items_as_withheld(binary: str, records: str) -> None:
    """Items a producer passes as withheld are recorded by id and version like applied ones"""
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag rose", labels=ACME)
        await c.judge(run, "reject", reason="name the partition")
        proposed = await c.propose("judgment", "Name the partition", from_run=run, item_id="partition")
        await c.approve(proposed.id, proposed.version, "ann")
        items = (await c.knowledge("stream-bot", ACME)).items

        recorded = await c.record("stream-bot", "lag rose again", labels=ACME, withheld=items)
        effect = (await c.report("effect")).report

    runs = [json.loads(line) for line in (Path(records) / "traces.jsonl").read_text().splitlines() if line.strip()]
    run_input = next(t["input"] for t in runs if t["id"] == recorded)
    assert run_input["applied"] == [] and run_input["withheld"] == [{"id": "partition", "version": 1}]
    # Recorded without a holdout key the run counts in no arm and only as undrawn
    assert [(r["runs"], r["undrawn"]) for r in effect if r.get("producer") == "stream-bot"] == [(0, 1), (0, 1)]


async def test_outcome_and_report(binary: str, records: str) -> None:
    # A prune another surface ran sets the window every report reads
    Path(records).mkdir()
    (Path(records) / "prune.json").write_text('{"at":"2020-01-02T00:00:00Z","cutoff":"2020-01-01T00:00:00Z"}')
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag comes from partition 7", labels=ACME)
        await c.outcome(run, "confirmed", confirmed_cause="partition 7 was hot", note="checked the broker")
        health = await c.report("health")
        loop = await c.report("loop")
        with pytest.raises(ToolError):
            await c.outcome(run, "refuted", confirmed_cause="partition 7 was hot")

    assert isinstance(health.report, (list, dict)) and loop.report["totals"]["runs"] == 1
    assert loop.since == "2020-01-01T00:00:00Z"
