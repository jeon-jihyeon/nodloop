"""OpenAI Agents SDK hooks for nodloop

1. instructions puts the approved items of the place after the instructions of the agent
2. the hooks record the final output of the agent as a run naming the items it got
3. veto_guardrail checks every tool call against the approved vetoes and rejects a blocked one with the reason
"""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

from agents import RunHooks, ToolGuardrailFunctionOutput, tool_input_guardrail

from .client import Client, Item

Labels = dict[str, list[str]]


class NodloopHooks(RunHooks):
    """Pass it as hooks to Runner.run and its instructions as the instructions of the agent

    Use one instance per run because it keeps the items and the run id of that run
    """

    def __init__(self, client: Client, producer: str, labels: Labels | None = None) -> None:
        self.client = client
        self.producer = producer
        self.labels = labels or {}
        self.applied: list[Item] = []
        # The run the final output was recorded as so a verdict can cite it
        self.run: str = ""

    def instructions(self, base: str) -> Callable[[Any, Any], Any]:
        """Dynamic instructions: the base and the approved items under it"""

        async def build(context: Any, agent: Any) -> str:
            knowledge = await self.client.knowledge(self.producer, self.labels)
            self.applied = knowledge.items
            return f"{base}\n\n{knowledge.text}" if knowledge.text else base

        return build

    async def on_agent_end(self, context: Any, agent: Any, output: Any) -> None:
        text = output if isinstance(output, str) else json.dumps(output, default=str)
        self.run = await self.client.record(self.producer, text, self.labels, self.applied)


def veto_guardrail(client: Client) -> Any:
    """A tool input guardrail for function_tool(tool_input_guardrails=[...])"""

    @tool_input_guardrail
    async def nodloop_veto(data: Any) -> ToolGuardrailFunctionOutput:
        arguments = json.loads(data.context.tool_arguments or "{}")
        decision = await client.check_call(data.context.tool_name, arguments)
        if decision.allowed:
            return ToolGuardrailFunctionOutput.allow()
        return ToolGuardrailFunctionOutput.reject_content(f"nodloop veto {decision.veto} stopped this call: {decision.reason}")

    return nodloop_veto
