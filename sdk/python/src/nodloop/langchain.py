"""LangChain agents middleware for nodloop

1. every model call of a run gets the approved items of the place in its system message, fetched once per run
2. the final answer of the agent is recorded as a run naming the items it got
3. every tool call is checked against the approved vetoes and a blocked one answers the model with the reason
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from langchain.agents.middleware import AgentMiddleware
from langchain_core.messages import AIMessage, SystemMessage, ToolMessage

from .client import Client, Item, Knowledge

Labels = dict[str, list[str]]


class NodloopMiddleware(AgentMiddleware):
    """Use one instance per agent run because it keeps the items and the run id of that run

    labels is a mapping or a function of the agent state that returns one
    """

    def __init__(self, client: Client, producer: str, labels: Labels | Callable[[Any], Labels] | None = None) -> None:
        super().__init__()
        self.client = client
        self.producer = producer
        self.labels = labels or {}
        self.applied: list[Item] = []
        # The items of the current run, fetched on its first model call and dropped once the run is recorded
        self._knowledge: Knowledge | None = None
        # The run the final answer was recorded as so a verdict can cite it
        self.run: str = ""

    def _labels(self, state: Any) -> Labels:
        return self.labels(state) if callable(self.labels) else self.labels

    async def awrap_model_call(self, request: Any, handler: Callable[[Any], Any]) -> Any:
        if self._knowledge is None:
            self._knowledge = await self.client.knowledge(self.producer, self._labels(request.state))
            self.applied = self._knowledge.items
        text = self._knowledge.text
        if not text:
            return await handler(request)
        return await handler(request.override(system_message=_with_text(request.system_message, text)))

    async def aafter_agent(self, state: Any, runtime: Any) -> None:
        answers = [m for m in state["messages"] if isinstance(m, AIMessage)]
        if answers:
            self.run = await self.client.record(self.producer, answers[-1].text, labels=self._labels(state), applied=self.applied)
        self._knowledge = None

    async def awrap_tool_call(self, request: Any, handler: Callable[[Any], Any]) -> Any:
        call = request.tool_call
        decision = await self.client.check_call(call["name"], call.get("args") or {}, self.producer)
        if decision.allowed:
            return await handler(request)
        return ToolMessage(
            content=f"nodloop veto {decision.veto} stopped this call: {decision.reason}",
            tool_call_id=call["id"],
            status="error",
        )


def _with_text(system: SystemMessage | None, text: str) -> SystemMessage:
    """The system message with the text after its content, keeping content blocks as they are

    The text of blocks joins them as they are, so the new text leads with the blank line
    """
    if system is None:
        return SystemMessage(content=text)
    after = f"\n\n{text}" if system.text else text
    if isinstance(system.content, str):
        return system.model_copy(update={"content": system.content + after})
    return system.model_copy(update={"content": [*system.content, {"type": "text", "text": after}]})
