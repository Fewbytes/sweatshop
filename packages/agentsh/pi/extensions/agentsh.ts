import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { createConnection, type Socket } from "node:net";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { existsSync } from "node:fs";
import { join } from "node:path";

const exec = promisify(execFile);

type RPCResponse = { result?: unknown; error?: { message: string } };

function socketPath(cwd: string): string {
  return join(cwd, ".agentsh", "agentshd.sock");
}

async function ensureDaemon(cwd: string): Promise<void> {
  const binary = process.env.AGENTSH_PATH ?? "agentsh";
  try { await exec(binary, ["--workspace", cwd, "health"], { timeout: 7000 }); } catch (error) {
    throw new Error(`agentsh daemon unavailable: ${error instanceof Error ? error.message : String(error)}`);
  }
}

// Control operations answer from daemon state; a command runs for as long as
// the caller asked. A single fixed socket timeout caps every call at the
// shortest case and orphans the invocation, which the daemon still completes.
const CONTROL_TIMEOUT_MS = 30_000;
const CALL_GRACE_MS = 30_000;
const DEFAULT_COMMAND_TIMEOUT_MS = 120_000;

function callTimeout(op: string, params: unknown): number {
  if (op !== "bash" && op !== "bash_replay") return CONTROL_TIMEOUT_MS;
  const requested = (params as { timeout_ms?: number } | undefined)?.timeout_ms;
  return (requested && requested > 0 ? requested : DEFAULT_COMMAND_TIMEOUT_MS) + CALL_GRACE_MS;
}

async function call(cwd: string, op: string, params: unknown): Promise<unknown> {
  await ensureDaemon(cwd);
  const socket = socketPath(cwd);
  return new Promise((resolve, reject) => {
    let data = "";
    const connection: Socket = createConnection(socket);
    connection.setTimeout(callTimeout(op, params));
    connection.on("connect", () => connection.write(JSON.stringify({ version: 1, id: `pi-${Date.now()}`, op, params }) + "\n"));
    connection.on("data", chunk => {
      data += chunk.toString();
      const newline = data.indexOf("\n");
      if (newline < 0) return;
      try {
        const response = JSON.parse(data.slice(0, newline)) as RPCResponse;
        connection.end();
        if (response.error) reject(new Error(response.error.message));
        else resolve(response.result);
      } catch (error) { reject(error); }
    });
    connection.on("timeout", () => { connection.destroy(); reject(new Error("agentsh RPC timeout")); });
    connection.on("error", reject);
  });
}

const bashSchema = Type.Object({
  command: Type.String({ description: "Bash command line to execute" }),
  session: Type.Optional(Type.String()),
  timeout: Type.Optional(Type.Number()),
  background: Type.Optional(Type.Boolean()),
  stdin: Type.Optional(Type.String()),
});

export default function (pi: ExtensionAPI) {
  // Prevent pi's competing built-in shell from winning under context pressure.
  pi.on("tool_call", async event => {
    if (event.toolName === "bash") return { block: true, reason: "Use the agentsh Bash tool instead." };
  });

  pi.registerTool({
    name: "Bash",
    label: "Bash (agentsh)",
    description: "Run a Bash command through the supervised agentsh runtime. Use BashOutput recovery calls from truncated or non-clean results.",
    parameters: bashSchema,
    async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
      const result = await call(ctx.cwd, "bash", {
        command: params.command,
        session: params.session,
        timeout_ms: params.timeout ? Math.round(params.timeout * 1000) : undefined,
        background: params.background,
        stdin: params.stdin,
      });
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: result };
    },
  });

  pi.registerTool({
    name: "BashOutput",
    label: "BashOutput (agentsh)",
    description: "Retrieve stored agentsh output by stream, line range, or grep.",
    parameters: Type.Object({ id: Type.String(), stream: Type.Optional(Type.String()), lines: Type.Optional(Type.String()), grep: Type.Optional(Type.String()), context: Type.Optional(Type.Number()) }),
    async execute(_id, params, _signal, _update, ctx) {
      const result = await call(ctx.cwd, "bash_output", params);
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: result };
    },
  });

  pi.registerTool({
    name: "BashInput",
    label: "BashInput (agentsh)",
    description: "Write input to an agentsh invocation reporting waiting_on_input.",
    parameters: Type.Object({ id: Type.String(), data: Type.String() }),
    async execute(_id, params, _signal, _update, ctx) {
      const result = await call(ctx.cwd, "bash_input", params);
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: result };
    },
  });
}
