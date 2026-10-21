"""The client of the nodloop MCP tools over a local binary or a nodloop server"""

from __future__ import annotations

import json
import os
from contextlib import AsyncExitStack
from dataclasses import dataclass, field
from typing import Any, Literal

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.client.streamable_http import create_mcp_http_client, streamable_http_client

from . import binary
from .records import AnyRecord, InterventionRecord, RecordKind

__all__ = [
    "Approval",
    "Candidate",
    "Client",
    "Decision",
    "Intervention",
    "InterventionAction",
    "Item",
    "Knowledge",
    "Page",
    "Ref",
    "Report",
    "ReportName",
    "Result",
    "ToolError",
]

# The binary release the client fetches when no nodloop is installed
VERSION = "0.9.0"


# The reports report reads
# Deprecated: page the raw records with records instead since they carry the published contract
ReportName = Literal["loop", "extract", "critic", "effect", "health", "replay"]

# The actions of the write side
# A test checks them against the generated record type of an intervention
InterventionAction = Literal["judge", "answer", "dismiss", "pause", "resume", "knob", "reanchor", "digest", "reverse"]

# What a real check found for a run
Result = Literal["confirmed", "refuted", "inconclusive"]


class ToolError(RuntimeError):
    """A tool answered an error such as a refused proposal"""


@dataclass(frozen=True)
class Ref:
    """One knowledge version as a run names it"""

    id: str
    version: int


@dataclass(frozen=True)
class Item:
    """One approved knowledge version a run receives"""

    id: str
    version: int
    kind: str
    content: str
    scope: dict[str, Any] = field(default_factory=dict)

    @property
    def ref(self) -> dict[str, Any]:
        return {"id": self.id, "version": self.version}


@dataclass(frozen=True)
class Decision:
    """What a tool call may do: allow, block or ask"""

    action: str
    veto: str = ""
    reason: str = ""

    @property
    def allowed(self) -> bool:
        return self.action == "allow"


@dataclass(frozen=True)
class Candidate:
    """A proposed knowledge version that waits for a person to approve it"""

    id: str
    version: int
    status: str
    # The current items of the same kind that one run may carry with the candidate, each as the ledger records it
    overlaps: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class Approval:
    """An approved knowledge version or the agent proposal of a call that proves no person while the version stays as it was"""

    id: str
    version: int
    status: str
    # The approver the records keep, which a server sets to the key's name
    approver: str
    # Whether the version carries a veto the guard applies once approved
    veto: bool = False
    # Why approved.md or the veto file could not be written, while the approval itself stands
    export_error: str = ""
    # Why the items one run carries with it could not be read, while the approval itself stands
    folder_error: str = ""
    # The intervention recorded as an agent proposal when the call proved no person
    # Empty when it approved
    proposal: str = ""


@dataclass(frozen=True)
class Knowledge:
    """The approved items a run receives and the text that carries them into its prompt"""

    items: list[Item]
    # Empty without items so a prompt gains nothing
    text: str
    # The id and version of each item the holdout kept from the run
    # Empty unless the holdout drew the key
    # Pass them to record as withheld so the run counts in the withheld arm of report effect
    withheld: list[Ref] = field(default_factory=list)


@dataclass(frozen=True)
class Report:
    """One report of the records with the start of the runs it read

    Deprecated: page the raw records with records instead since they carry the published contract
    """

    # The report as JSON
    report: Any
    # The RFC3339 start of the window and empty when the report read every run
    since: str = ""


@dataclass(frozen=True)
class Page:
    """One page of raw records in the published schema of their kind, in the order they were appended"""

    # Each record as the TypedDict of its kind in nodloop.records
    records: list[AnyRecord]
    # The cursor to pass as since for the next page
    # A page with nothing new still answers one to pass again
    next: str = ""


@dataclass(frozen=True)
class Intervention:
    """What one intervention of the write side recorded"""

    # The intervention record in its published schema and None when the person declined the form
    intervention: InterventionRecord | None
    # person when a key a person holds decided and agent for a proposal the person is asked about
    authority: str = ""
    proof: str = ""
    # The knowledge records it wrote, each with id, version and status
    records: list[dict[str, Any]] = field(default_factory=list)
    # The answer event an answer wrote
    answer: str = ""
    # The cursor of a digest to pass as since to the records of answer, intervention, decide, expose and knowledge
    since: str = ""
    # The decide that asks the person about a proposal
    question: str = ""
    # The person declined the form so nothing was recorded
    declined: bool = False
    export_error: str = ""


def _ref(v: Item | Ref | dict[str, Any]) -> dict[str, Any]:
    """The id and version the run tool takes from an item or a ref"""
    if isinstance(v, dict):
        return {"id": v["id"], "version": v["version"]}
    return {"id": v.id, "version": v.version}


class Client:
    """The nodloop tools of one process or of a nodloop server

    1. by default it starts `nodloop mcp` over stdio, so nothing runs as a server
    2. record_dir names the records and else NODLOOP_RECORD_DIR, config.json or ~/.nodloop/records decide as in the CLI
    3. url and key connect to `nodloop-server serve` instead, where the key decides the tenant and the role
    4. use it as an async context manager so the process or the connection ends with the block
    """

    def __init__(
        self,
        record_dir: str | None = None,
        binary_path: str | None = None,
        url: str | None = None,
        key: str | None = None,
    ) -> None:
        self._record_dir = record_dir
        self._binary = binary_path
        self._url = url
        self._key = key
        self._stack = AsyncExitStack()
        self._session: ClientSession | None = None

    async def __aenter__(self) -> Client:
        try:
            await self._connect()
        except BaseException:
            # A failed handshake such as a refused key closes what it opened since __aexit__ never runs
            await self._stack.aclose()
            raise
        return self

    async def _connect(self) -> None:
        if self._url:
            http = create_mcp_http_client(headers={"Authorization": f"Bearer {self._key}"} if self._key else None)
            await self._stack.enter_async_context(http)
            streams = await self._stack.enter_async_context(streamable_http_client(self._url, http_client=http))
        else:
            env = dict(os.environ)
            if self._record_dir:
                env["NODLOOP_RECORD_DIR"] = os.path.abspath(self._record_dir)
            params = StdioServerParameters(command=self._binary or binary.find(VERSION), args=["mcp"], env=env)
            streams = await self._stack.enter_async_context(stdio_client(params))
        read, write = streams[0], streams[1]
        self._session = await self._stack.enter_async_context(ClientSession(read, write))
        await self._session.initialize()

    async def __aexit__(self, *exc: object) -> None:
        await self._stack.aclose()
        self._session = None

    async def call(self, name: str, arguments: dict[str, Any]) -> Any:
        """The structured answer of one tool, raising ToolError when the tool refused"""
        if self._session is None:
            raise RuntimeError("use the client inside async with")
        result = await self._session.call_tool(name, arguments)
        text = "\n".join(getattr(c, "text", "") for c in result.content)
        # mcp 2 names the fields in snake case and mcp 1 in camel case
        if getattr(result, "is_error", None) or getattr(result, "isError", None):
            raise ToolError(text)
        structured = getattr(result, "structured_content", None) or getattr(result, "structuredContent", None)
        if structured is not None:
            return structured
        return json.loads(text) if text else None

    async def knowledge(self, producer: str, labels: dict[str, list[str]] | None = None, holdout_key: str = "") -> Knowledge:
        """The approved items a run of the producer with the labels receives and the text to put in its prompt

        holdout_key is a string kept for one run such as a service and an hour block and the holdout share of config.json draws on it
        A drawn run gets no item and withheld instead
        Without a key nothing is withheld
        """
        args: dict[str, Any] = {"producer": producer, "labels": labels or {}}
        if holdout_key:
            args["holdout_key"] = holdout_key
        answer = await self.call("knowledge_for", args)
        items = [Item(i["id"], i["version"], i["kind"], i["content"], i.get("scope", {})) for i in answer.get("items") or []]
        withheld = [Ref(r["id"], r["version"]) for r in answer.get("withheld") or []]
        return Knowledge(items, answer.get("context", ""), withheld)

    async def record(
        self,
        producer: str,
        output: Any,
        *,
        labels: dict[str, list[str]] | None = None,
        applied: list[Item] | None = None,
        subject: str = "",
        withheld: list[Item | Ref] | None = None,
        holdout_key: str = "",
        model: str = "",
        duration_ms: int = 0,
    ) -> str:
        """Records one output and returns the run id a verdict cites

        withheld takes the refs knowledge answered for a run the holdout drew or the items themselves
        Never together with applied
        holdout_key is the key the run passed to knowledge
        report effect counts a run without it only as undrawn
        model is the one model the producer runs and the OTLP export names it
        Leave it out when the producer may switch models
        duration_ms is how long the run took up to this call so its OTLP span has a length
        """
        args: dict[str, Any] = {"producer": producer, "output": output, "labels": labels or {}}
        if applied:
            args["applied"] = [_ref(i) for i in applied]
        if withheld:
            args["withheld"] = [_ref(r) for r in withheld]
        if subject:
            args["subject"] = subject
        if holdout_key:
            args["holdout_key"] = holdout_key
        if model:
            args["model"] = model
        if duration_ms:
            args["duration_ms"] = duration_ms
        return (await self.call("run", args))["trace_id"]

    async def judge(
        self,
        run: str,
        verdict: str,
        *,
        reviewer: str,
        reason: str = "",
        reason_code: str = "",
        edited: Any = None,
    ) -> None:
        """Records a verdict on a run: approve, edit with the corrected output, reject with what was wrong, or withdraw an earlier one

        reviewer names who judged so a verdict without a name never reads as a person's
        nodloop is refused since only the capture of a session records verdicts under it
        Every argument after verdict is keyword only
        So a call of an earlier release that passed the reason third fails instead of naming it the reviewer
        """
        args: dict[str, Any] = {"trace_id": run, "verdict": verdict}
        for key, value in (("reason", reason), ("reason_code", reason_code), ("reviewer", reviewer)):
            if value:
                args[key] = value
        if edited is not None:
            args["edited_output"] = edited
        await self.call("feedback", args)

    async def outcome(self, run: str, result: Result, confirmed_cause: str = "", note: str = "", reviewer: str = "") -> None:
        """Records what a real check found for a run: confirmed, refuted or inconclusive

        A different fact from the verdict on the output
        confirmed_cause only goes with confirmed
        reviewer names who checked and is required over stdio while a server key names itself
        """
        args: dict[str, Any] = {"trace_id": run, "result": result}
        for key, value in (("confirmed_cause", confirmed_cause), ("note", note), ("reviewer", reviewer)):
            if value:
                args[key] = value
        await self.call("outcome", args)

    async def report(self, name: ReportName) -> Report:
        """One report of the records as JSON with the start of the window it read

        Deprecated: page the raw records with records instead since they carry the published contract
        report is removed in the next release
        """
        answer = await self.call("report", {"name": name})
        return Report(answer["report"], answer.get("since", ""))

    async def records(self, kind: RecordKind, since: str = "", limit: int = 0, content: bool = False) -> Page:
        """One page of the raw records of a kind in its published schema, in the order they were appended after the cursor since

        Pass next back as since for the following page, so a record appended late with an earlier time still comes once
        limit is 100 by default and at most 1000
        The free text of the kind such as outputs, reasons and lesson content is left out unless content is true
        """
        args: dict[str, Any] = {"kind": kind}
        if since:
            args["since"] = since
        if limit:
            args["limit"] = limit
        if content:
            args["content"] = True
        answer = await self.call("records", args)
        return Page(answer.get("records") or [], answer.get("next", ""))

    async def propose(
        self,
        kind: str,
        content: str,
        from_run: str = "",
        producer: str = "",
        labels: dict[str, list[str]] | None = None,
        new_labels: bool = False,
        item_id: str = "",
        veto: dict[str, Any] | None = None,
    ) -> Candidate:
        """Proposes a candidate a person approves later, from a corrected run or for a producer and labels

        veto names a tool call the judgment forbids as tool, when and example, as the MCP tool propose takes it
        """
        args: dict[str, Any] = {"kind": kind, "content": content}
        if veto:
            args["veto"] = veto
        for key, value in (("from", from_run), ("producer", producer), ("id", item_id)):
            if value:
                args[key] = value
        if labels:
            args["labels"] = labels
        if new_labels:
            args["new_labels"] = True
        answer = await self.call("propose", args)
        return Candidate(answer["id"], answer["version"], answer["status"], answer.get("overlaps") or [])

    async def approve(self, item_id: str, version: int, approver: str, *, labels: dict[str, list[str]] | None = None) -> Approval:
        """Approves a candidate under a key of nodloop-server that a person holds

        Any other call such as one over stdio records an agent proposal the person is asked about: the status stays and proposal names it
        labels approves it for the runs carrying them instead of the proposed scope and an empty mapping for every run of its producer
        """
        args: dict[str, Any] = {"id": item_id, "version": version, "approver": approver}
        if labels is not None:
            args["scope"] = {"labels": labels}
        answer = await self.call("approve", args)
        return Approval(
            answer["id"],
            answer["version"],
            answer["status"],
            answer["approver"],
            veto=bool(answer.get("veto")),
            export_error=answer.get("export_error", ""),
            folder_error=answer.get("folder_error", ""),
            proposal=answer.get("proposal", ""),
        )

    async def intervene(
        self,
        action: InterventionAction,
        target: str = "",
        *,
        choice: str = "",
        labels: dict[str, list[str]] | None = None,
        value: float | None = None,
        session: str = "",
    ) -> Intervention:
        """Submits one intervention of the write side: judge, answer, dismiss, pause, resume, knob, reanchor, digest or reverse

        target is an item version as id:version, a compaction, the decide of a question, the change kind of a pause,
        the pause a resume ends, a knob such as audit_tier1, the producer of a place or the intervention a reverse takes back
        Under a key of nodloop-server that a person holds it is the person's decision at once
        Any other call such as one over stdio records an agent proposal the person is asked about and changes nothing
        A digest needs no proof and answers since, the cursor after the person's last decision
        """
        args: dict[str, Any] = {"action": action}
        for key, given in (("target", target), ("choice", choice), ("session", session)):
            if given:
                args[key] = given
        if labels is not None:
            args["labels"] = labels
        if value is not None:
            args["value"] = value
        answer = await self.call("intervene", args)
        return Intervention(
            answer.get("intervention"),
            authority=answer.get("authority", ""),
            proof=answer.get("proof", ""),
            records=answer.get("records") or [],
            answer=answer.get("answer", ""),
            since=answer.get("since", ""),
            question=answer.get("question", ""),
            declined=bool(answer.get("declined")),
            export_error=answer.get("export_error", ""),
        )

    async def check_call(self, tool: str, arguments: dict[str, Any], producer: str = "") -> Decision:
        """Whether a veto approved for the producer blocks or asks about a tool call before the agent runs it

        An empty producer checks against the vetoes of every producer
        """
        answer = await self.call("check_call", {"tool": tool, "input": arguments, "producer": producer})
        return Decision(answer["action"], answer.get("veto", ""), answer.get("reason", ""))
