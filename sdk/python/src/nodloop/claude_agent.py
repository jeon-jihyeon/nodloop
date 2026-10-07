"""Claude Agent SDK hooks for nodloop, the same loop the Claude Code plugin runs

1. UserPromptSubmit adds the approved items of the place as context
2. Stop records the last answer as a run naming the items it got
3. PreToolUse denies a call an approved veto blocks and asks about one it hands to a person
"""

from __future__ import annotations

from typing import Any

from claude_agent_sdk import HookMatcher

from .client import Client, Item

Labels = dict[str, list[str]]


class NodloopHooks:
    """Pass hooks() as the hooks of ClaudeAgentOptions

    Use one instance per conversation because it keeps the items of the turn and the id of the last run
    """

    def __init__(self, client: Client, producer: str, labels: Labels | None = None) -> None:
        self.client = client
        self.producer = producer
        self.labels = labels or {}
        self.applied: list[Item] = []
        # The run the last answer was recorded as so a verdict can cite it
        self.run: str = ""

    def hooks(self) -> dict[str, list[HookMatcher]]:
        return {
            "UserPromptSubmit": [HookMatcher(hooks=[self.prompt])],
            "Stop": [HookMatcher(hooks=[self.stop])],
            "PreToolUse": [HookMatcher(hooks=[self.pre_tool_use])],
        }

    async def prompt(self, input_data: dict[str, Any], tool_use_id: str | None, context: Any) -> dict[str, Any]:
        knowledge = await self.client.knowledge(self.producer, self.labels)
        self.applied = knowledge.items
        if not knowledge.text:
            return {}
        return {"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": knowledge.text}}

    async def stop(self, input_data: dict[str, Any], tool_use_id: str | None, context: Any) -> dict[str, Any]:
        # Claude Code sends the last answer under last_assistant_message
        # A turn without one records nothing
        answer = input_data.get("last_assistant_message") or ""
        if answer.strip():
            self.run = await self.client.record(self.producer, answer, labels=self.labels, applied=self.applied)
        return {}

    async def pre_tool_use(self, input_data: dict[str, Any], tool_use_id: str | None, context: Any) -> dict[str, Any]:
        decision = await self.client.check_call(input_data["tool_name"], input_data.get("tool_input") or {}, self.producer)
        if decision.allowed:
            return {}
        return {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny" if decision.action == "block" else "ask",
                "permissionDecisionReason": f"nodloop veto {decision.veto}: {decision.reason}",
            }
        }
