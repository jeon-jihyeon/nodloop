// LangChain agents middleware for nodloop
// 1. every model call gets the approved items of the place in its system message
// 2. the final answer of the agent is recorded as a run naming the items it got
// 3. every tool call is checked against the approved vetoes and a blocked one answers the model with the reason

import { AIMessage, createMiddleware, SystemMessage, ToolMessage } from "langchain";
import type { BaseMessage } from "@langchain/core/messages";
import type { Client, Item, Labels } from "./client.ts";

// Use one instance per agent run because it keeps the items and the run id of that run
// labels is a mapping or a function of the agent state that returns one
export class NodloopMiddleware {
  applied: Item[] = [];
  // The run the final answer was recorded as so a verdict can cite it
  run = "";

  private readonly client: Client;
  private readonly producer: string;
  private readonly labels: Labels | ((state: unknown) => Labels);

  constructor(client: Client, producer: string, labels: Labels | ((state: unknown) => Labels) = {}) {
    this.client = client;
    this.producer = producer;
    this.labels = labels;
  }

  // The middleware option of createAgent
  middleware() {
    return createMiddleware({
      name: "NodloopMiddleware",
      wrapModelCall: async (request, handler) => {
        const knowledge = await this.client.knowledge(this.producer, this.labelsOf(request.state));
        this.applied = knowledge.items;
        if (!knowledge.text) return handler(request);
        const base = request.systemMessage?.text ?? "";
        const systemMessage = new SystemMessage(base ? `${base}\n\n${knowledge.text}` : knowledge.text);
        return handler({ ...request, systemMessage });
      },
      afterAgent: async (state) => {
        await this.record(state);
      },
      wrapToolCall: async (request, handler) => {
        const call = request.toolCall;
        const decision = await this.client.checkCall(call.name, call.args ?? {}, this.producer);
        if (decision.action === "allow") return handler(request);
        return new ToolMessage({
          content: `nodloop veto ${decision.veto} stopped this call: ${decision.reason}`,
          tool_call_id: call.id ?? "",
          status: "error",
        });
      },
    });
  }

  // Records the last answer of the agent state as a run and returns its id
  // A state without an answer records nothing
  async record(state: { messages: BaseMessage[] }): Promise<string> {
    const answers = state.messages.filter((m) => AIMessage.isInstance(m));
    if (!answers.length) return "";
    const output = answers[answers.length - 1].text;
    this.run = await this.client.record({ producer: this.producer, output, labels: this.labelsOf(state), applied: this.applied });
    return this.run;
  }

  private labelsOf(state: unknown): Labels {
    return typeof this.labels === "function" ? this.labels(state) : this.labels;
  }
}
