from dataclasses import dataclass, field, replace
from types import SimpleNamespace
from typing import Any

import pytest
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage, ToolMessage

from nodloop import Client
from nodloop.claude_agent import NodloopHooks as ClaudeHooks
from nodloop.langchain import NodloopMiddleware
from nodloop.openai_agents import NodloopHooks as OpenAIHooks
from nodloop.openai_agents import veto_guardrail

ACME = {"tenant": ["acme"]}
NO_RM = {"tool": "Bash", "when": [{"field": "commands", "match": "(?m)^rm "}], "example": {"command": "rm -rf /data"}}


async def seed(c: Client) -> None:
    """An approved item for acme and an approved veto on rm"""
    run = await c.record("bot", "answer", labels=ACME)
    await c.judge(run, "reject", reason="missing the window")
    for item_id, content, veto in (("window", "Quote the refund window", None), ("no-rm", "Never run rm", NO_RM)):
        p = await c.propose("judgment", content, from_run=run, item_id=item_id, veto=veto)
        await c.approve(p.id, p.version, "ann")


async def runs(c: Client) -> list[str]:
    """The ids of the runs that wait for a verdict"""
    return [i["trace_id"] for i in (await c.call("queue", {"limit": 50}))["items"]]


@dataclass
class ModelRequest:
    """The fields of a langchain ModelRequest the middleware reads"""

    state: dict[str, Any] = field(default_factory=dict)
    system_message: SystemMessage | None = None

    def override(self, **changes: Any) -> "ModelRequest":
        return replace(self, **changes)


async def test_langchain_middleware(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        await seed(c)
        mw = NodloopMiddleware(c, "bot", ACME)
        seen: list[ModelRequest] = []

        async def model(request: ModelRequest) -> AIMessage:
            seen.append(request)
            return AIMessage("ok")

        async def tool(request: Any) -> ToolMessage:
            return ToolMessage("ran", tool_call_id=request.tool_call["id"])

        await mw.awrap_model_call(ModelRequest(system_message=SystemMessage("You help with refunds")), model)
        first = mw.applied
        blocks = [{"type": "text", "text": "You help"}, {"type": "text", "text": "with refunds"}]
        await mw.awrap_model_call(ModelRequest(system_message=SystemMessage(content=blocks)), model)
        fetched_once = mw.applied is first
        blocked = await mw.awrap_tool_call(SimpleNamespace(tool_call={"id": "t1", "name": "Bash", "args": {"command": "rm -rf /x"}}), tool)
        allowed = await mw.awrap_tool_call(SimpleNamespace(tool_call={"id": "t2", "name": "Bash", "args": {"command": "ls"}}), tool)
        await mw.aafter_agent({"messages": [HumanMessage("refund?"), AIMessage("The window is 30 days")]}, None)
        recorded = await runs(c)

    assert seen[0].system_message.content.startswith("You help with refunds\n\nnodloop: corrections")
    assert "[window v1 judgment] Quote the refund window" in seen[0].system_message.content
    assert fetched_once and [i.id for i in first] == ["window"]
    assert seen[1].system_message.content[:2] == blocks
    assert seen[1].system_message.content[2]["text"].startswith("\n\nnodloop: corrections")
    assert blocked.status == "error" and "no-rm" in blocked.content
    assert allowed.content == "ran"
    assert mw.run in recorded


async def test_openai_agents_hooks(binary: str, records: str) -> None:
    async with Client(records, binary) as c:
        await seed(c)
        hooks = OpenAIHooks(c, "bot", ACME)
        guard = veto_guardrail(c, "bot")

        instructions = await hooks.instructions("You help with refunds")(None, None)
        await hooks.on_agent_end(None, None, "The window is 30 days")
        blocked = await guard.guardrail_function(
            SimpleNamespace(context=SimpleNamespace(tool_name="Bash", tool_arguments='{"command":"rm -rf /x"}'))
        )
        allowed = await guard.guardrail_function(
            SimpleNamespace(context=SimpleNamespace(tool_name="Bash", tool_arguments='{"command":"ls"}'))
        )
        recorded = await runs(c)

    assert instructions.startswith("You help with refunds\n\nnodloop: corrections")
    assert blocked.behavior["type"] == "reject_content" and "no-rm" in blocked.behavior["message"]
    assert allowed.behavior["type"] == "allow"
    assert hooks.run in recorded


@pytest.mark.parametrize(("command", "decision"), [("rm -rf /x", "deny"), ("ls", None)])
async def test_claude_agent_hooks(binary: str, records: str, command: str, decision: str | None) -> None:
    async with Client(records, binary) as c:
        await seed(c)
        hooks = ClaudeHooks(c, "bot", ACME)

        prompt = await hooks.prompt({"prompt": "refund?"}, None, None)
        pre = await hooks.pre_tool_use({"tool_name": "Bash", "tool_input": {"command": command}}, "t1", None)
        await hooks.stop({"last_assistant_message": "The window is 30 days"}, None, None)
        recorded = await runs(c)

    assert "[window v1 judgment]" in prompt["hookSpecificOutput"]["additionalContext"]
    assert pre.get("hookSpecificOutput", {}).get("permissionDecision") == decision
    assert hooks.run in recorded
    assert set(hooks.hooks()) == {"UserPromptSubmit", "Stop", "PreToolUse"}


async def test_langchain_create_agent(binary: str, records: str) -> None:
    """The middleware runs inside a real langchain agent with a scripted model that calls rm and then answers"""
    from langchain.agents import create_agent
    from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
    from langchain_core.tools import tool

    prompts: list[str] = []

    class Scripted(GenericFakeChatModel):
        def bind_tools(self, tools: Any, **kwargs: Any) -> "Scripted":
            return self

        async def _agenerate(self, messages: Any, *args: Any, **kwargs: Any) -> Any:
            prompts.append(messages[0].text)
            return await super()._agenerate(messages, *args, **kwargs)

    ran: list[str] = []

    @tool
    def bash(command: str) -> str:
        """Run a shell command"""
        ran.append(command)
        return "done"

    script = iter(
        [AIMessage("", tool_calls=[{"id": "c1", "name": "bash", "args": {"command": "rm -rf /x"}}]), AIMessage("I could not delete it")]
    )
    async with Client(records, binary) as c:
        await seed(c)
        # A veto names the tool as this agent calls it
        run = await c.record("bot", "ran rm", labels=ACME)
        await c.judge(run, "reject", reason="never rm")
        veto = {"tool": "bash", "when": [{"field": "commands", "match": "(?m)^rm "}], "example": {"command": "rm -rf /data"}}
        p = await c.propose("judgment", "Never run rm through bash", from_run=run, item_id="no-rm-bash", veto=veto)
        await c.approve(p.id, p.version, "ann")
        mw = NodloopMiddleware(c, "bot", ACME)
        agent = create_agent(Scripted(messages=script), tools=[bash], system_prompt="You help", middleware=[mw])

        result = await agent.ainvoke({"messages": [HumanMessage("delete /x")]})
        recorded = await runs(c)

    tool_messages = [m for m in result["messages"] if isinstance(m, ToolMessage)]
    assert ran == []
    assert tool_messages and "nodloop veto" in tool_messages[0].content
    assert mw.run in recorded
    assert len(prompts) == 2 and all(p.startswith("You help\n\nnodloop: corrections") for p in prompts)
    assert [i.id for i in mw.applied] == ["window"]
