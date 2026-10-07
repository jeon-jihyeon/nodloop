// OpenAI Agents SDK hooks for nodloop
// 1. instructions puts the approved items of the place after the instructions of the agent
// 2. attach records the final output of the agent as a run naming the items it got
// 3. vetoGuardrail checks every tool call against the approved vetoes and rejects a blocked one with the reason

import { defineToolInputGuardrail, ToolGuardrailFunctionOutputFactory, type Runner } from "@openai/agents";
import type { Client, Item, Labels } from "./client.ts";

// Use one instance per run because it keeps the items and the run id of that run
export class NodloopHooks {
  applied: Item[] = [];
  // The run the final output was recorded as so a verdict can cite it
  run = "";

  private readonly client: Client;
  private readonly producer: string;
  private readonly labels: Labels;

  constructor(client: Client, producer: string, labels: Labels = {}) {
    this.client = client;
    this.producer = producer;
    this.labels = labels;
  }

  // Dynamic instructions: the base and the approved items under it
  instructions(base: string): () => Promise<string> {
    return async () => {
      const knowledge = await this.client.knowledge(this.producer, this.labels);
      this.applied = knowledge.items;
      return knowledge.text ? `${base}\n\n${knowledge.text}` : base;
    };
  }

  // Records the output of an agent that ended as a run
  async record(output: unknown): Promise<string> {
    const text = typeof output === "string" ? output : JSON.stringify(output);
    this.run = await this.client.record({ producer: this.producer, output: text, labels: this.labels, applied: this.applied });
    return this.run;
  }

  // The recording the last agent_end started so a caller can await it before reading run
  pending?: Promise<string>;

  // Records every agent_end the runner emits
  attach(runner: Runner): void {
    runner.on("agent_end", (_context, _agent, output) => {
      this.pending = this.record(output);
    });
  }
}

// A tool input guardrail for tool({ inputGuardrails: [...] }) checking the vetoes approved for the producer
export function vetoGuardrail(client: Client, producer = "") {
  return defineToolInputGuardrail({
    name: "nodloop_veto",
    run: async ({ toolCall }) => {
      const decision = await client.checkCall(toolCall.name, JSON.parse(toolCall.arguments || "{}"), producer);
      if (decision.action === "allow") return ToolGuardrailFunctionOutputFactory.allow();
      return ToolGuardrailFunctionOutputFactory.rejectContent(`nodloop veto ${decision.veto} stopped this call: ${decision.reason}`);
    },
  });
}
