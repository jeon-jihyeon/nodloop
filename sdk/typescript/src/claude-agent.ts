// Claude Agent SDK hooks for nodloop, the same loop the Claude Code plugin runs
// 1. UserPromptSubmit adds the approved items of the place as context
// 2. Stop records the last answer as a run naming the items it got
// 3. PreToolUse denies a call an approved veto blocks and asks about one it hands to a person

import type { HookCallback, HookCallbackMatcher, HookEvent } from "@anthropic-ai/claude-agent-sdk";
import { adapterOptions, type AdapterOptions, type Client, type Item, type Labels, type Ref } from "./client.ts";

// Use one instance per conversation because it keeps the items of the turn and the id of the last run
export class NodloopHooks {
  applied: Item[] = [];
  withheld: Ref[] = [];
  // The run the last answer was recorded as so a verdict can cite it
  run = "";
  // When the prompt of the turn came so the run records how long the turn took
  private started = performance.now();

  private readonly client: Client;
  private readonly producer: string;
  private readonly holdoutKey: string;
  private readonly labels: Labels;

  // The third argument takes the options or a labels mapping alone
  // holdoutKey stays the same for every turn so a whole conversation falls in one arm
  // report effect counts the turns of one key as one draw unit so they weigh as a cluster and not as independent runs
  constructor(client: Client, producer: string, options: AdapterOptions | Labels = {}) {
    this.client = client;
    this.producer = producer;
    ({ labels: this.labels, holdoutKey: this.holdoutKey } = adapterOptions<Labels>(options, {}));
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
    this.started = performance.now();
    const knowledge = await this.client.knowledge(this.producer, this.labels, this.holdoutKey);
    this.applied = knowledge.items;
    this.withheld = knowledge.withheld ?? [];
    if (!knowledge.text) return {};
    return { hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext: knowledge.text } };
  };

  // Claude Code sends the last answer under last_assistant_message and a turn without one records nothing
  stop: HookCallback = async (input) => {
    const answer = (input as { last_assistant_message?: string }).last_assistant_message ?? "";
    if (answer.trim()) this.run = await this.client.record({ producer: this.producer, output: answer, labels: this.labels, applied: this.applied, withheld: this.withheld, holdoutKey: this.holdoutKey, durationMs: performance.now() - this.started });
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
