import json
from collections.abc import Callable
from pathlib import Path

import pytest
from mcp.shared.exceptions import MCPError

from nodloop import Client, Ref, ToolError

# Approves a version as the person at a terminal of their own, the approve fixture of conftest
Approve = Callable[..., None]

ACME = {"tenant": ["acme"], "task": ["refund"]}
NO_RM = {
    "tool": "Bash",
    "when": [{"field": "commands", "match": "(?m)^rm .*/data"}],
    "example": {"command": "rm -rf /data"},
}


async def test_loop(binary: str, records: str, approve: Approve) -> None:
    """An SDK call proposes the approval and the next run receives the item once the person approved it at their terminal"""
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", "ann", reason="the refund window was missing", reason_code="scope")
        proposed = await c.propose("judgment", "Quote the refund window before the steps", from_run=run, item_id="refund-window")
        proposal = await c.approve(proposed.id, proposed.version, "ann")
        before = await c.knowledge("support-bot", ACME)
        approve(proposed.id, proposed.version)

        acme = await c.knowledge("support-bot", ACME)
        globex = await c.knowledge("support-bot", {"tenant": ["globex"], "task": ["refund"]})
        nxt = await c.record("support-bot", "The window is 30 days. Steps follow", labels=ACME, applied=acme.items)

    assert (proposal.status, proposal.approver, bool(proposal.proposal)) == ("candidate", "", True)
    assert before.items == []
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


async def test_judge_names_its_reviewer(binary: str, records: str) -> None:
    """A verdict without a reviewer is refused over stdio, where no key names one"""
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "answer", labels=ACME)
        with pytest.raises(ToolError, match="a reviewer is required"):
            await c.judge(run, "approve", "")
        with pytest.raises(TypeError):
            await c.judge(run, "approve")  # type: ignore[call-arg]


async def test_judge_refuses_the_name_of_the_capture(binary: str, records: str) -> None:
    """nodloop is reserved for the capture of a session, so a verdict or an outcome under it is refused and nothing is recorded"""
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "answer", labels=ACME)
        with pytest.raises(ToolError, match="reserved for what the capture reads"):
            await c.judge(run, "reject", "nodloop", reason="wrong")
        with pytest.raises(ToolError, match="reserved for what the capture reads"):
            await c.outcome(run, "confirmed", reviewer="nodloop")
    written = [p.name for p in Path(records).glob("*.jsonl") if p.read_text()]
    assert "feedback.jsonl" not in written and "outcomes.jsonl" not in written


@pytest.mark.parametrize(
    ("tool", "arguments", "action"),
    [
        ("Bash", {"command": "cd / && rm -rf /data"}, "block"),
        ("Bash", {"command": "ls /data"}, "allow"),
        ("Read", {"file_path": "/data"}, "allow"),
    ],
)
async def test_check_call(binary: str, records: str, approve: Approve, tool: str, arguments: dict, action: str) -> None:
    async with Client(records, binary) as c:
        run = await c.record("ops-bot", "ran rm -rf /data", labels={"env": ["prod"]})
        await c.judge(run, "reject", "ann", reason="never delete data")
        proposed = await c.propose("judgment", "Never delete under /data", from_run=run, item_id="no-rm-data", veto=NO_RM)
        proposal = await c.approve(proposed.id, proposed.version, "ann")
        approve(proposed.id, proposed.version)

        decision = await c.check_call(tool, arguments)

    assert proposal.veto and proposal.proposal
    assert decision.action == action
    assert decision.allowed == (action == "allow")


async def test_server(server: tuple[str, str, str]) -> None:
    """A key a person holds approves under its name and a key of a service only proposes"""
    url, key, service = server
    async with Client(url=url, key=service) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", "ci", reason="the refund window was missing")
        proposed = await c.propose("judgment", "Quote the refund window", from_run=run, item_id="refund-window")
        proposal = await c.approve(proposed.id, proposed.version, "mallory")
    async with Client(url=url, key=key) as c:
        approved = await c.approve(proposed.id, proposed.version, "mallory")
        acme = await c.knowledge("support-bot", ACME)
        with pytest.raises(ToolError, match="reserved for what the capture reads"):
            await c.judge(run, "approve", "nodloop")

    assert (proposal.status, bool(proposal.proposal)) == ("candidate", True)
    assert (approved.id, approved.version, approved.status, approved.approver) == ("refund-window", 1, "approved", "ann")
    assert approved.proposal == ""
    assert (proposed.id, proposed.status) == ("refund-window", "candidate")
    assert [i.id for i in acme.items] == ["refund-window"]


async def test_intervene_under_a_person_key(server: tuple[str, str, str]) -> None:
    """An intervention under a key a person holds decides at once, its reverse takes it back and a key of a service only proposes"""
    url, key, service = server
    async with Client(url=url, key=service) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", "ci", reason="the refund window was missing")
        proposed = await c.propose("judgment", "Quote the refund window", from_run=run, item_id="refund-window")
        proposal = await c.intervene("judge", f"{proposed.id}:{proposed.version}", choice="keep")
    async with Client(url=url, key=key) as c:
        kept = await c.intervene("judge", f"{proposed.id}:{proposed.version}", choice="keep")
        received = await c.knowledge("support-bot", ACME)
        assert kept.intervention is not None
        back = await c.intervene("reverse", kept.intervention["id"])
        after = await c.knowledge("support-bot", ACME)

    assert (proposal.authority, proposal.records) == ("agent", [])
    assert (kept.authority, kept.proof, kept.records) == ("person", "key", [{"id": "refund-window", "version": 1, "status": "approved"}])
    assert kept.intervention["record_kind"] == "intervention"
    assert [i.id for i in received.items] == ["refund-window"]
    assert [r["status"] for r in back.records] == ["retired", "candidate"]
    assert after.items == []


async def test_intervene_over_stdio(binary: str, records: str) -> None:
    """Over stdio an intervention is a proposal, a digest answers its cursor and a knob outside its range is a ToolError"""
    async with Client(record_dir=records, binary_path=binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", "ann", reason="the refund window was missing")
        proposed = await c.propose("judgment", "Quote the refund window", from_run=run, item_id="refund-window")
        proposal = await c.intervene("judge", f"{proposed.id}:{proposed.version}", choice="keep", session="s-1")
        digest = await c.intervene("digest", "support-bot", labels=ACME)
        with pytest.raises(ToolError, match="names no target"):
            await c.intervene("knob", "audit_tier1", value=0.9)
        received = await c.knowledge("support-bot", ACME)

    assert (proposal.authority, proposal.proof, proposal.records, proposal.declined) == ("agent", "none", [], False)
    assert (digest.authority, digest.since) == ("agent", "")
    assert received.items == []


async def test_server_refuses_a_wrong_key(server: tuple[str, str, str]) -> None:
    url, _, _ = server
    with pytest.raises(MCPError):
        async with Client(url=url, key="nl_wrong") as c:
            await c.knowledge("support-bot", ACME)


async def test_record_takes_keywords_after_output() -> None:
    with pytest.raises(TypeError):
        await Client().record("support-bot", "answer", ACME)  # type: ignore[misc]


async def test_eleventh_item_under_the_run_chars_approves(binary: str, records: str, approve: Approve) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.judge(run, "reject", "ann", reason="the refund window was missing")
        for n in range(11):
            proposed = await c.propose("judgment", f"Refund rule number {n}", from_run=run, item_id=f"rule-{n}")
            approve(proposed.id, proposed.version)
        acme = await c.knowledge("support-bot", ACME)

    assert len(acme.items) == 11


async def test_approve_in_picked_labels(binary: str, records: str, approve: Approve) -> None:
    async with Client(records, binary) as c:
        run = await c.record("support-bot", "Here are the refund steps", labels=ACME)
        await c.record("support-bot", "ok", labels={"tenant": ["globex"]})
        await c.judge(run, "reject", "ann", reason="too long", reason_code="form")
        proposed = await c.propose("judgment", "Answer in three lines", from_run=run)
        proposal = await c.approve(proposed.id, proposed.version, "ann", labels={})
        with pytest.raises(ToolError, match="no run of support-bot carries tenant=initech"):
            await c.approve(proposed.id, proposed.version, "ann", labels={"tenant": ["initech"]})
        approve(proposed.id, proposed.version, "--everywhere")
        globex = await c.knowledge("support-bot", {"tenant": ["globex"]})

    assert (proposal.status, bool(proposal.proposal)) == ("candidate", True)
    assert [i.id for i in globex.items] == [proposed.id]


async def test_holdout_key_withholds_and_record_joins_the_withheld_arm(binary: str, records: str, home: Path, approve: Approve) -> None:
    (home / ".nodloop").mkdir(exist_ok=True)
    (home / ".nodloop" / "config.json").write_text('{"holdout": 0.5}')
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag rose", labels=ACME)
        await c.judge(run, "reject", "ann", reason="name the partition")
        proposed = await c.propose("judgment", "Name the partition", from_run=run, item_id="partition")
        approve(proposed.id, proposed.version)
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


async def test_record_takes_items_as_withheld(binary: str, records: str, approve: Approve) -> None:
    """Items a producer passes as withheld are recorded by id and version like applied ones"""
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag rose", labels=ACME)
        await c.judge(run, "reject", "ann", reason="name the partition")
        proposed = await c.propose("judgment", "Name the partition", from_run=run, item_id="partition")
        approve(proposed.id, proposed.version)
        items = (await c.knowledge("stream-bot", ACME)).items

        recorded = await c.record("stream-bot", "lag rose again", labels=ACME, withheld=items)
        effect = (await c.report("effect")).report

    runs = [json.loads(line) for line in (Path(records) / "traces.jsonl").read_text().splitlines() if line.strip()]
    run_input = next(t["input"] for t in runs if t["id"] == recorded)
    assert run_input["applied"] == [] and run_input["withheld"] == [{"id": "partition", "version": 1}]
    # Recorded without a holdout key the run counts in no arm and only as undrawn
    assert [(r["runs"], r["undrawn"]) for r in effect if r.get("producer") == "stream-bot"] == [(0, 1), (0, 1)]


async def test_record_names_the_model(binary: str, records: str) -> None:
    """The model of a run lands in its trace and a run without one records none"""
    async with Client(records, binary) as c:
        named = await c.record("stream-bot", "lag rose", model="gpt-4o-mini")
        bare = await c.record("stream-bot", "lag rose again")

    runs = {t["id"]: t for t in (json.loads(line) for line in (Path(records) / "traces.jsonl").read_text().splitlines() if line.strip())}
    assert runs[named]["model"] == "gpt-4o-mini"
    assert "model" not in runs[bare]


async def test_record_names_the_duration(binary: str, records: str) -> None:
    """The duration of a run lands in its trace so its span has a length"""
    async with Client(records, binary) as c:
        timed = await c.record("stream-bot", "lag rose", duration_ms=1500)
        bare = await c.record("stream-bot", "lag rose again")

    runs = {t["id"]: t for t in (json.loads(line) for line in (Path(records) / "traces.jsonl").read_text().splitlines() if line.strip())}
    assert runs[timed]["duration_ms"] == 1500
    assert runs[bare]["duration_ms"] == 0


async def test_outcome_and_report(binary: str, records: str) -> None:
    # A prune another surface ran sets the window every report reads
    Path(records).mkdir()
    (Path(records) / "prune.json").write_text('{"at":"2020-01-02T00:00:00Z","cutoff":"2020-01-01T00:00:00Z"}')
    async with Client(records, binary) as c:
        run = await c.record("stream-bot", "lag comes from partition 7", labels=ACME)
        await c.outcome(run, "confirmed", confirmed_cause="partition 7 was hot", note="checked the broker", reviewer="ann")
        health = await c.report("health")
        loop = await c.report("loop")
        with pytest.raises(ToolError):
            await c.outcome(run, "refuted", confirmed_cause="partition 7 was hot", reviewer="ann")
        with pytest.raises(ToolError, match="a reviewer is required"):
            await c.outcome(run, "refuted")

    assert isinstance(health.report, (list, dict)) and loop.report["totals"]["runs"] == 1
    assert loop.since == "2020-01-01T00:00:00Z"


FIRST, SECOND = "019a0c8fcb9e00000001", "019a0c8fcb9e00000002"
USAGE = {"input_tokens": 0, "output_tokens": 0, "cache_read": 0, "cache_create": 0, "cost_usd": 0}


def seed_records(records: Path) -> None:
    """Two runs and a verdict recorded long before the test so every page holds them, each with text a page leaves out"""
    records.mkdir(parents=True, exist_ok=True)
    runs = [
        {"id": run_id, "name": "run", "time": at, "input": {"applied": []}, "output": output, "usage": USAGE, "duration_ms": 0}
        | {"producer": "support-bot", "schema_version": 1}
        for run_id, at, output in (
            (FIRST, "2026-10-01T09:00:00Z", "refund steps"),
            (SECOND, "2026-10-01T09:00:01Z", "the window is 30 days"),
        )
    ]
    verdict = {
        "trace_id": FIRST,
        "time": "2026-10-01T09:00:02Z",
        "verdict": "reject",
        "reason": "the window was missing",
        "reviewer": "ann",
    }
    (records / "traces.jsonl").write_text("".join(json.dumps(r) + "\n" for r in runs))
    (records / "feedback.jsonl").write_text(json.dumps(verdict | {"proof": "none", "schema_version": 1}) + "\n")


async def test_records_page_across_a_cursor(binary: str, records: str) -> None:
    """The records of a kind page oldest first across a cursor in their published schema with the text left out unless asked"""
    seed_records(Path(records))
    async with Client(records, binary) as c:
        first = await c.records("run", limit=1)
        second = await c.records("run", since=first.next)
        end = await c.records("run", since=second.next)
        shown = await c.records("run", limit=1, content=True)
        verdicts = await c.records("verdict")

    assert [r["record_id"] for r in first.records] == [FIRST]
    assert [r["record_id"] for r in second.records] == [SECOND]
    assert (end.records, end.next) == ([], second.next)
    assert "output" not in first.records[0] and shown.records[0]["output"] == "refund steps"
    assert [(v["record_kind"], v["trace_id"], "reason" in v) for v in verdicts.records] == [("verdict", FIRST, False)]


async def test_records_refuse_a_cursor_no_page_answered(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        with pytest.raises(ToolError, match="the cursor names no record"):
            await c.records("run", since="%%%")


async def test_records_over_the_server(server: tuple[str, str, str], tmp_path: Path) -> None:
    """A key of the server pages the records of its tenant"""
    url, key, _ = server
    seed_records(tmp_path / "records" / "tenants" / "acme")
    async with Client(url=url, key=key) as c:
        page = await c.records("run", content=True)

    assert [(r["record_id"], r["output"]) for r in page.records] == [(FIRST, "refund steps"), (SECOND, "the window is 30 days")]
