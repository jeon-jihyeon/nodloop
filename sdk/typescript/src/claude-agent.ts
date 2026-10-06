// Claude Agent SDK hooks for nodloop, the same loop the Claude Code plugin runs
// 1. UserPromptSubmit adds the approved items of the place as context
// 2. Stop records the last answer as a run naming the items it got
// 3. PreToolUse denies a call an approved veto blocks and asks about one it hands to a person

import type { HookCallback, HookCallbackMatcher, HookEvent } from "@anthropic-ai/claude-agent-sdk";
import type { Client, Item, Labels } from "./client.ts";

// Use one instance per conversation because it keeps the items of the turn and the id of the last run
export class NodloopHooks {
  applied: Item[] = [];
  // The run the last answer was recorded as so a verdict can cite it
  run = "";

  private readonly client: Client;
  private readonly producer: string;
  private readonly labels: Labels;

  constructor(client: Client, producer: string, labels: Labels = {}) {
    this.client = client;
    this.producer = producer;
    this.labels = labels;
  }

  // The hooks option of query
  hooks(): Partial<Record<HookEvent, HookCallbackMatcher[]>> {
    return {
      UserPromptSubmit: [{ hooks: [this.prompt] }],
      Stop: [{ hooks: [this.stop] }],
      PreToolUse: [{ hooks: [this.preToolUse] }],
    };
  }

  prompt: HookCallback = async () => {
    const knowledge = await this.client.knowledge(this.producer, this.labels);
    this.applied = knowledge.items;
    if (!knowledge.text) return {};
    return { hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext: knowledge.text } };
  };

  // Claude Code sends the last answer under last_assistant_message and a turn without one records nothing
  stop: HookCallback = async (input) => {
    const answer = (input as { last_assistant_message?: string }).last_assistant_message ?? "";
    if (answer.trim()) this.run = await this.client.record(this.producer, answer, this.labels, this.applied);
    return {};
  };

  preToolUse: HookCallback = async (input) => {
    const call = input as { tool_name: string; tool_input?: Record<string, unknown> };
    const decision = await this.client.checkCall(call.tool_name, call.tool_input ?? {}, this.producer);
    if (decision.action === "allow") return {};
    return {
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: decision.action === "block" ? "deny" : "ask",
        permissionDecisionReason: `nodloop veto ${decision.veto}: ${decision.reason}`,
      },
    };
  };
}
