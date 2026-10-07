// The client of the nodloop MCP tools over a local binary

import { Client as McpClient } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { resolve } from "node:path";
import { find } from "./binary.ts";

/** The binary release the client fetches when no nodloop is installed */
export const version = "0.7.0";

export type Labels = Record<string, string[]>;

/** One approved knowledge version a run receives */
export interface Item {
  id: string;
  version: number;
  kind: string;
  content: string;
  scope?: Record<string, unknown>;
}

/**
 * The approved items a run receives and the text that carries them into its prompt
 * The text is empty without items so a prompt gains nothing
 */
export interface Knowledge {
  items: Item[];
  text: string;
}

/** What a tool call may do */
export interface Decision {
  action: "allow" | "block" | "ask";
  veto?: string;
  reason?: string;
}

/** A proposed knowledge version that waits for a person to approve it */
export interface Candidate {
  id: string;
  version: number;
  status: string;
  /** The current items of the same kind that one run may carry with the candidate, each as the ledger records it */
  overlaps?: { id: string; version: number; [field: string]: unknown }[];
}

/** An approved knowledge version */
export interface Approval {
  id: string;
  version: number;
  status: string;
  /** The approver the records keep, which a server sets to the key's name */
  approver: string;
  /** Whether the approved version carries a veto the guard now applies */
  veto: boolean;
  /** Whether one run would carry more than five items so a compaction is due */
  compactionDue: boolean;
  /** Why approved.md or the veto file could not be written, while the approval itself stands */
  exportError: string;
  /** Why the items one run carries with it could not be read, while the approval itself stands */
  folderError: string;
}

/** One output to record */
export interface Run {
  producer: string;
  /** Text or any JSON value */
  output: unknown;
  labels?: Labels;
  /** The items the run received, as knowledge returned them */
  applied?: Item[];
  /** What the run was about in a few words */
  subject?: string;
}

/** A tool answered an error such as a refused proposal */
export class ToolError extends Error {}

export interface Options {
  /** The records to use, else NODLOOP_RECORD_DIR, config.json or ~/.nodloop/records decide as in the CLI */
  recordDir?: string;
  /** The binary to run, else the one find picks */
  binary?: string;
  /** A nodloop server to connect to instead of a local binary, where the key decides the tenant and the role */
  url?: string;
  key?: string;
}

/**
 * The nodloop tools of one process or of a nodloop server
 * open starts `nodloop mcp` over stdio so nothing runs as a server, or connects to url with key, and close ends it
 */
export class Client {
  private readonly mcp: McpClient;

  private constructor(mcp: McpClient) {
    this.mcp = mcp;
  }

  /** Starts a local `nodloop mcp` or connects to a server */
  static async open(options: Options = {}): Promise<Client> {
    const mcp = new McpClient({ name: "nodloop-sdk", version });
    await mcp.connect(options.url ? Client.http(options.url, options.key) : await Client.stdio(options));
    return new Client(mcp);
  }

  private static http(url: string, key?: string): StreamableHTTPClientTransport {
    const headers: Record<string, string> = key ? { Authorization: `Bearer ${key}` } : {};
    return new StreamableHTTPClientTransport(new URL(url), { requestInit: { headers } });
  }

  private static async stdio(options: Options): Promise<StdioClientTransport> {
    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(process.env)) if (v !== undefined) env[k] = v;
    if (options.recordDir) env.NODLOOP_RECORD_DIR = resolve(options.recordDir);
    return new StdioClientTransport({ command: options.binary ?? (await find(version)), args: ["mcp"], env });
  }

  /** Ends the process or the connection */
  async close(): Promise<void> {
    await this.mcp.close();
  }

  /** The structured answer of one tool, throwing ToolError when the tool refused */
  async call(name: string, args: Record<string, unknown>): Promise<any> {
    const result = await this.mcp.callTool({ name, arguments: args });
    const text = ((result.content as { type: string; text?: string }[]) ?? []).map((c) => c.text ?? "").join("\n");
    if (result.isError) throw new ToolError(text);
    return result.structuredContent ?? (text ? JSON.parse(text) : undefined);
  }

  /** The approved items a run of the producer with the labels receives and the text to put in its prompt */
  async knowledge(producer: string, labels: Labels = {}): Promise<Knowledge> {
    const answer = await this.call("knowledge_for", { producer, labels });
    return { items: answer.items ?? [], text: answer.context ?? "" };
  }

  /** Records one output and returns the run id a verdict cites */
  async record(run: Run): Promise<string> {
    const args: Record<string, unknown> = { producer: run.producer, output: run.output, labels: run.labels ?? {} };
    if (run.applied?.length) args.applied = run.applied.map((i) => ({ id: i.id, version: i.version }));
    if (run.subject) args.subject = run.subject;
    return (await this.call("run", args)).trace_id;
  }

  /** Records a verdict on a run: approve, edit with the corrected output, reject with what was wrong, or withdraw an earlier one */
  async judge(
    run: string,
    verdict: "approve" | "edit" | "reject" | "withdraw",
    options: { reason?: string; reasonCode?: string; edited?: unknown; reviewer?: string } = {},
  ): Promise<void> {
    const args: Record<string, unknown> = { trace_id: run, verdict };
    if (options.reason) args.reason = options.reason;
    if (options.reasonCode) args.reason_code = options.reasonCode;
    if (options.reviewer) args.reviewer = options.reviewer;
    if (options.edited !== undefined) args.edited_output = options.edited;
    await this.call("feedback", args);
  }

  /** Proposes a candidate a person approves later, from a corrected run or for a producer and labels */
  async propose(proposal: {
    kind: "judgment" | "meaning";
    content: string;
    from?: string;
    producer?: string;
    labels?: Labels;
    newLabels?: boolean;
    id?: string;
    veto?: { tool: string; when: { field: string; match: string; unless?: string }[]; example: Record<string, unknown> };
  }): Promise<Candidate> {
    const { newLabels, ...rest } = proposal;
    return this.call("propose", newLabels ? { ...rest, new_labels: true } : rest);
  }

  /** Approves a candidate on behalf of the named person */
  async approve(id: string, version: number, approver: string): Promise<Approval> {
    const answer = await this.call("approve", { id, version, approver });
    return {
      id: answer.id,
      version: answer.version,
      status: answer.status,
      approver: answer.approver,
      veto: Boolean(answer.veto),
      compactionDue: Boolean(answer.folder?.compaction_due),
      exportError: answer.export_error ?? "",
      folderError: answer.folder_error ?? "",
    };
  }

  /**
   * Whether a veto approved for the producer blocks or asks about a tool call before the agent runs it
   * An empty producer checks against the vetoes of every producer
   */
  async checkCall(tool: string, input: Record<string, unknown>, producer = ""): Promise<Decision> {
    return this.call("check_call", { tool, input, producer });
  }
}
