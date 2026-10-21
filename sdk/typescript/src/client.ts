// The client of the nodloop MCP tools over a local binary

import { Client as McpClient } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { resolve } from "node:path";
import { find } from "./binary.ts";
import type { AnyRecord, InterventionRecord, RecordKind, Records } from "./records.ts";

/** The binary release the client fetches when no nodloop is installed */
export const version = "0.10.0";

export type Labels = Record<string, string[]>;

/** One approved knowledge version a run receives */
export interface Item {
  id: string;
  version: number;
  kind: string;
  content: string;
  scope?: Record<string, unknown>;
}

/** One knowledge version as a run names it */
export interface Ref {
  id: string;
  version: number;
}

/**
 * The approved items a run receives and the text that carries them into its prompt
 * The text is empty without items so a prompt gains nothing
 */
export interface Knowledge {
  items: Item[];
  text: string;
  /**
   * The items the holdout kept from the run
   * Empty unless the holdout drew the key and absent from a Knowledge built without one
   * Pass them to record as withheld so the run counts in the withheld arm of report effect
   */
  withheld?: Ref[];
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

/** An approved knowledge version or the agent proposal of a call that proves no person while the version stays as it was */
export interface Approval {
  id: string;
  version: number;
  status: string;
  /** The approver the records keep, which a server sets to the key's name */
  approver: string;
  /** Whether the version carries a veto the guard applies once approved */
  veto: boolean;
  /** Why approved.md or the veto file could not be written, while the approval itself stands */
  exportError: string;
  /** Why the items one run carries with it could not be read, while the approval itself stands */
  folderError: string;
  /** The intervention recorded as an agent proposal when the call proved no person and empty when it approved */
  proposal: string;
}

/** One output to record */
export interface Run {
  producer: string;
  /** Text or any JSON value */
  output: unknown;
  /** correlation labels preserve an external id without entering inferred knowledge scope */
  labels?: Labels;
  /** The items the run received as knowledge returned them, or their refs */
  applied?: (Item | Ref)[];
  /**
   * The items the holdout kept from the run as knowledge returned them in withheld, or the items themselves
   * Never together with applied
   */
  withheld?: (Item | Ref)[];
  /** What the run was about in a few words */
  subject?: string;
  /**
   * The key the run passed to knowledge
   * report effect counts a run without it only as undrawn
   */
  holdoutKey?: string;
  /**
   * The one model the producer runs and the OTLP export names
   * Leave it out when the producer may switch models
   */
  model?: string;
  /** How many milliseconds the run took up to this call so its OTLP span has a length */
  durationMs?: number;
}

/** The input of an intervention as the published schema of the record types it */
type InterventionInput = InterventionRecord["input"];

/**
 * The actions of the write side
 * Read from the generated record type so a new action of the published schema reaches the client
 */
export type InterventionAction = InterventionInput["action"];

/** One intervention of the write side as MCP intervene and POST /v1/interventions take it */
export interface InterventionRequest {
  action: InterventionAction;
  /**
   * What it acts on: an item version as id:version, a compaction, the decide of a question, a change kind of a pause,
   * the pause a resume ends, a knob such as audit_tier1, the producer of a place or the intervention a reverse takes back
   */
  target?: string;
  /** keep, undo or scope for judge, or one of the two choices of the question for answer */
  choice?: NonNullable<InterventionInput["choice"]>;
  /** The labels of a scope choice, a pause, a reanchor or a digest */
  labels?: Labels;
  /** The value of a knob within its range */
  value?: number;
  /** The session a proposal is asked in */
  session?: string;
}

/** What one intervention recorded */
export interface Intervention {
  /**
   * The intervention record in its published schema
   * Absent when the person declined the form
   */
  intervention?: InterventionRecord;
  /** person when a key that proves a person decided and agent for a proposal the person is asked about */
  authority?: InterventionInput["authority"];
  proof?: InterventionInput["proof"];
  /** The knowledge records it wrote */
  records: { id: string; version: number; status: string }[];
  /** The answer event an answer wrote */
  answer?: string;
  /** The cursor of a digest to pass as since to the records of answer, intervention, decide, expose and knowledge */
  since?: string;
  /** The decide that asks the person about a proposal */
  question?: string;
  /** The person declined the form so nothing was recorded */
  declined?: boolean;
  exportError?: string;
}

/**
 * The reports report reads
 * @deprecated Page the raw records with records instead since they carry the published contract
 * report is removed in the next release
 */
export type ReportName = "loop" | "extract" | "critic" | "effect" | "health" | "replay";

/**
 * One report of the records with the start of the runs it read
 * @deprecated Page the raw records with records instead
 */
export interface Report {
  /** The report as JSON */
  report: any;
  /** The RFC3339 start of the window and absent when the report read every run */
  since?: string;
}

/** One page of raw records in the published schema of their kind, in the order they were appended */
export interface Page<T = AnyRecord> {
  records: T[];
  /**
   * The cursor to pass as since for the next page
   * A page with nothing new still answers one to pass again
   */
  next: string;
}

/** What a page of records asks besides its kind */
export interface PageOptions {
  /** The next cursor of the page before and empty for the first page */
  since?: string;
  /** 100 by default and at most 1000 */
  limit?: number;
  /** Keep the free text of the kind such as outputs, reasons and lesson content */
  content?: boolean;
}

/** What an adapter takes besides the client and the producer */
export interface AdapterOptions<L = Labels> {
  labels?: L;
  /** Passed to knowledge so a share of runs gets no item and report effect compares them */
  holdoutKey?: string;
}

/**
 * The options of an adapter from its third argument
 * A labels mapping alone stays accepted since every label value is a list and no option value is
 */
export function adapterOptions<L>(arg: L | AdapterOptions<L> | undefined, empty: L): { labels: L; holdoutKey: string } {
  if (arg === undefined) return { labels: empty, holdoutKey: "" };
  if (typeof arg === "function" || Object.values(arg as object).some(Array.isArray)) return { labels: arg as L, holdoutKey: "" };
  const options = arg as AdapterOptions<L>;
  return { labels: options.labels ?? empty, holdoutKey: options.holdoutKey ?? "" };
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

  /**
   * The approved items a run of the producer with the labels receives and the text to put in its prompt
   * holdoutKey is a string kept for one run such as a service and an hour block and the holdout share of config.json draws on it
   * A drawn run gets no item and withheld instead
   * Without a key nothing is withheld
   * the correlation label names an external conversation independently of the returned run id and holdoutKey
   */
  async knowledge(producer: string, labels: Labels = {}, holdoutKey = ""): Promise<Knowledge> {
    const args: Record<string, unknown> = { producer, labels };
    if (holdoutKey) args.holdout_key = holdoutKey;
    const answer = await this.call("knowledge_for", args);
    return { items: answer.items ?? [], text: answer.context ?? "", withheld: answer.withheld ?? [] };
  }

  /** Records one output and returns the run id a verdict cites */
  async record(run: Run): Promise<string> {
    if (run.durationMs !== undefined && (!Number.isFinite(run.durationMs) || run.durationMs < 0 || Math.round(run.durationMs) > 9223372036854)) {
      throw new RangeError("durationMs must be finite and between 0 and 9223372036854 milliseconds");
    }
    const args: Record<string, unknown> = { producer: run.producer, output: run.output, labels: run.labels ?? {} };
    if (run.applied?.length) args.applied = run.applied.map((i) => ({ id: i.id, version: i.version }));
    if (run.withheld?.length) args.withheld = run.withheld.map(({ id, version }) => ({ id, version }));
    if (run.subject) args.subject = run.subject;
    if (run.holdoutKey) args.holdout_key = run.holdoutKey;
    if (run.model) args.model = run.model;
    if (run.durationMs) args.duration_ms = Math.round(run.durationMs);
    return (await this.call("run", args)).trace_id;
  }

  /**
   * Records a verdict on a run: approve, edit with the corrected output, reject with what was wrong, or withdraw an earlier one
   * reviewer names who judged so a verdict without a name never reads as a person's
   * nodloop is refused since only the capture of a session records verdicts under it
   * reviewer stays in the options of an earlier release and is required there so no positional argument can take its place
   */
  async judge(
    run: string,
    verdict: "approve" | "edit" | "reject" | "withdraw",
    options: { reviewer: string; reason?: string; reasonCode?: string; edited?: unknown },
  ): Promise<void> {
    const args: Record<string, unknown> = { trace_id: run, verdict };
    if (options.reviewer) args.reviewer = options.reviewer;
    if (options.reason) args.reason = options.reason;
    if (options.reasonCode) args.reason_code = options.reasonCode;
    if (options.edited !== undefined) args.edited_output = options.edited;
    await this.call("feedback", args);
  }

  /**
   * Records what a real check found for a run
   * A different fact from the verdict on the output and confirmedCause only goes with confirmed
   * reviewer names who checked and is required over stdio while a server key names itself
   */
  async outcome(
    run: string,
    result: "confirmed" | "refuted" | "inconclusive",
    options: { confirmedCause?: string; note?: string; reviewer?: string } = {},
  ): Promise<void> {
    const args: Record<string, unknown> = { trace_id: run, result };
    if (options.confirmedCause) args.confirmed_cause = options.confirmedCause;
    if (options.note) args.note = options.note;
    if (options.reviewer) args.reviewer = options.reviewer;
    await this.call("outcome", args);
  }

  /**
   * One report of the records as JSON with the start of the window it read
   * @deprecated Page the raw records with records instead since they carry the published contract
   * report is removed in the next release
   */
  async report(name: ReportName): Promise<Report> {
    const { report, since } = await this.call("report", { name });
    return since ? { report, since } : { report };
  }

  /**
   * One page of the raw records of a kind in its published schema, in the order they were appended after the cursor since
   * Pass next back as since for the following page, so a record appended late with an earlier time still comes once
   * The free text of the kind such as outputs, reasons and lesson content is left out unless content is true
   */
  async records<K extends RecordKind>(kind: K, options: PageOptions = {}): Promise<Page<Records[K]>> {
    const args: Record<string, unknown> = { kind };
    if (options.since) args.since = options.since;
    if (options.limit) args.limit = options.limit;
    if (options.content) args.content = true;
    const answer = await this.call("records", args);
    return { records: answer.records ?? [], next: answer.next ?? "" };
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

  /**
   * Approves a candidate under a key of nodloop-server that a person holds
   * Any other call such as one over stdio records an agent proposal the person is asked about: the status stays and proposal names it
   */
  async approve(id: string, version: number, approver: string, options: { labels?: Labels } = {}): Promise<Approval> {
    const args: Record<string, unknown> = { id, version, approver };
    if (options.labels !== undefined) args.scope = { labels: options.labels };
    const answer = await this.call("approve", args);
    return {
      id: answer.id,
      version: answer.version,
      status: answer.status,
      approver: answer.approver,
      veto: Boolean(answer.veto),
      exportError: answer.export_error ?? "",
      folderError: answer.folder_error ?? "",
      proposal: answer.proposal ?? "",
    };
  }

  /**
   * Submits one intervention of the write side: judge, answer, dismiss, pause, resume, knob, reanchor, digest or reverse
   * Under a key of nodloop-server that a person holds it is the person's decision at once
   * Any other call such as one over stdio records an agent proposal the person is asked about and changes nothing
   * A digest needs no proof and answers since, the cursor after the person's last decision
   */
  async intervene(request: InterventionRequest): Promise<Intervention> {
    const args: Record<string, unknown> = { action: request.action };
    for (const key of ["target", "choice", "labels", "value", "session"] as const) {
      if (request[key] !== undefined) args[key] = request[key];
    }
    const { export_error: exportError, ...answer } = await this.call("intervene", args);
    return exportError ? { ...answer, records: answer.records ?? [], exportError } : { ...answer, records: answer.records ?? [] };
  }

  /**
   * Whether a veto approved for the producer blocks or asks about a tool call before the agent runs it
   * An empty producer checks against the vetoes of every producer
   */
  async checkCall(tool: string, input: Record<string, unknown>, producer = ""): Promise<Decision> {
    return this.call("check_call", { tool, input, producer });
  }
}
