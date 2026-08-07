import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { once } from "node:events";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
	createAssistantMessageEventStream,
	type Api,
	type AssistantMessage,
	type AssistantMessageEventStream,
	type Context,
	type Model,
	type SimpleStreamOptions,
} from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const PROVIDER_ID = "go-codex";
const API_ID = "go-codex-bridge";
const DEFAULT_MODEL = "gpt-5.5";
const STANDARD_THINKING = { off: "none", minimal: "minimal", low: "low", medium: "medium", high: "high" } as const;
const LEGACY_THINKING = { ...STANDARD_THINKING, minimal: "low" } as const;
const GPT_56_THINKING = { ...STANDARD_THINKING, xhigh: "xhigh", max: "max" } as const;
const extensionDir = dirname(fileURLToPath(import.meta.url));
const defaultBinary = resolve(extensionDir, "..", "pi-go-codex");
const defaultCredentialCommand =
	"pi auth print-bearer-token --provider openai-codex --model gpt-5.5 --min-expiry 5m";

type BridgeRequest = {
	model: string;
	systemPrompt: string;
	messages: Array<{
		role: string;
		content: unknown;
		toolCallId?: string;
		toolName?: string;
		isError?: boolean;
	}>;
	tools: Array<{ name: string; description: string; parameters: unknown }>;
	reasoning?: string;
	sessionId?: string;
	transport?: SimpleStreamOptions["transport"];
	headers?: Record<string, string | null>;
};

type BridgeEvent = {
	type: string;
	payload?: unknown;
	contentIndex?: number;
	delta?: string;
	id?: string;
	name?: string;
	arguments?: Record<string, unknown>;
	reason?: "stop" | "length" | "toolUse";
	responseId?: string;
	usage?: {
		input_tokens?: number;
		output_tokens?: number;
		total_tokens?: number;
		input_tokens_details?: { cached_tokens?: number };
	};
	message?: string;
	status?: number;
	headers?: Record<string, string[] | string>;
};

type PartialToolCall = { id: string; name: string; json: string };

function createOutput(model: Model<Api>): AssistantMessage {
	return {
		role: "assistant",
		content: [],
		api: model.api,
		provider: model.provider,
		model: model.id,
		usage: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
		},
		stopReason: "pending",
		timestamp: Date.now(),
	};
}

function bridgeRequest(model: Model<Api>, context: Context, options?: SimpleStreamOptions): BridgeRequest {
	return {
		model: model.id,
		systemPrompt: context.systemPrompt ?? "",
		messages: context.messages.map((message) => {
			if (message.role === "toolResult") {
				return {
					role: message.role,
					content: message.content,
					toolCallId: message.toolCallId,
					toolName: message.toolName,
					isError: message.isError,
				};
			}
			return { role: message.role, content: message.content };
		}),
		tools: (context.tools ?? []).map((tool) => ({
			name: tool.name,
			description: tool.description,
			parameters: tool.parameters,
		})),
		reasoning: options?.reasoning,
		sessionId: options?.sessionId,
		transport: options?.transport,
		headers: options?.headers,
	};
}

async function writeJson(child: ChildProcessWithoutNullStreams, value: unknown, end = false): Promise<void> {
	const data = `${JSON.stringify(value)}\n`;
	if (!child.stdin.write(data)) {
		await once(child.stdin, "drain");
	}
	if (end) child.stdin.end();
}

function parsePartialArguments(value: string): Record<string, unknown> {
	try {
		const parsed: unknown = JSON.parse(value);
		return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
			? (parsed as Record<string, unknown>)
			: {};
	} catch {
		return {};
	}
}

function headersToRecord(headers: Record<string, string[] | string> | undefined): Record<string, string> {
	return Object.fromEntries(
		Object.entries(headers ?? {}).map(([name, value]) => [name, Array.isArray(value) ? value.join(", ") : value]),
	);
}

function applyUsage(output: AssistantMessage, usage: BridgeEvent["usage"]): void {
	if (!usage) return;
	output.usage.input = usage.input_tokens ?? 0;
	output.usage.output = usage.output_tokens ?? 0;
	output.usage.cacheRead = usage.input_tokens_details?.cached_tokens ?? 0;
	output.usage.cacheWrite = 0;
	output.usage.totalTokens =
		usage.total_tokens ?? output.usage.input + output.usage.output + output.usage.cacheRead + output.usage.cacheWrite;
}

function streamGoCodex(
	model: Model<Api>,
	context: Context,
	options?: SimpleStreamOptions,
): AssistantMessageEventStream {
	const stream = createAssistantMessageEventStream();
	const output = createOutput(model);

	void (async () => {
		let terminal = false;
		try {
			if (!options?.apiKey) throw new Error("No Codex credential available. Run /login openai-codex first.");
			const binary = process.env.PI_GO_CODEX_BIN || defaultBinary;
			const args = ["--provider"];
			if (options?.timeoutMs !== undefined) args.push("--timeout", `${options.timeoutMs}ms`);
			const child = spawn(binary, args, {
				env: { ...process.env, PI_GO_CODEX_TOKEN: options.apiKey },
				stdio: ["pipe", "pipe", "pipe"],
			});
			const partialTools = new Map<number, PartialToolCall>();
			const abort = () => child.kill("SIGTERM");
			options?.signal?.addEventListener("abort", abort, { once: true });

			const handleEvent = async (event: BridgeEvent): Promise<void> => {
				switch (event.type) {
					case "payload": {
						const replacement = await options?.onPayload?.(event.payload, model);
						await writeJson(child, { type: "payload", payload: replacement ?? event.payload }, true);
						return;
					}
					case "response":
						await options?.onResponse?.({ status: event.status ?? 0, headers: headersToRecord(event.headers) }, model);
						return;
					case "start":
						stream.push({ type: "start", partial: output });
						return;
					case "text_start": {
						const contentIndex = event.contentIndex ?? 0;
						output.content[contentIndex] = { type: "text", text: "" };
						stream.push({ type: "text_start", contentIndex, partial: output });
						return;
					}
					case "text_delta": {
						const contentIndex = event.contentIndex ?? 0;
						const block = output.content[contentIndex];
						if (!block || block.type !== "text") throw new Error("Go Codex sent a text delta without a text block");
						block.text += event.delta ?? "";
						stream.push({ type: "text_delta", contentIndex, delta: event.delta ?? "", partial: output });
						return;
					}
					case "text_end": {
						const contentIndex = event.contentIndex ?? 0;
						const block = output.content[contentIndex];
						if (!block || block.type !== "text") throw new Error("Go Codex ended an unknown text block");
						stream.push({ type: "text_end", contentIndex, content: block.text, partial: output });
						return;
					}
					case "toolcall_start": {
						const contentIndex = event.contentIndex ?? 0;
						const tool = { id: event.id ?? "", name: event.name ?? "", json: "" };
						partialTools.set(contentIndex, tool);
						output.content[contentIndex] = { type: "toolCall", id: tool.id, name: tool.name, arguments: {} };
						stream.push({ type: "toolcall_start", contentIndex, partial: output });
						return;
					}
					case "toolcall_delta": {
						const contentIndex = event.contentIndex ?? 0;
						const tool = partialTools.get(contentIndex);
						const block = output.content[contentIndex];
						if (!tool || !block || block.type !== "toolCall") {
							throw new Error("Go Codex sent a tool-call delta without a tool-call block");
						}
						tool.json += event.delta ?? "";
						block.arguments = parsePartialArguments(tool.json);
						stream.push({ type: "toolcall_delta", contentIndex, delta: event.delta ?? "", partial: output });
						return;
					}
					case "toolcall_end": {
						const contentIndex = event.contentIndex ?? 0;
						const block = output.content[contentIndex];
						if (!block || block.type !== "toolCall") throw new Error("Go Codex ended an unknown tool-call block");
						block.arguments = event.arguments ?? {};
						stream.push({ type: "toolcall_end", contentIndex, toolCall: block, partial: output });
						return;
					}
					case "done": {
						applyUsage(output, event.usage);
						output.responseId = event.responseId;
						const reason = event.reason ?? "stop";
						output.stopReason = reason;
						stream.push({ type: "done", reason, message: output });
						stream.end();
						terminal = true;
						return;
					}
					case "error":
						throw new Error(event.message || "Go Codex provider failed");
					default:
						throw new Error(`Unknown Go Codex bridge event: ${event.type}`);
				}
			};

			let stderr = "";
			let pending = "";
			let processing = Promise.resolve();
			child.stderr.on("data", (chunk: Buffer) => {
				stderr += chunk.toString("utf8");
			});
			child.stdout.on("data", (chunk: Buffer) => {
				pending += chunk.toString("utf8");
				while (true) {
					const newline = pending.indexOf("\n");
					if (newline === -1) break;
					const line = pending.slice(0, newline);
					pending = pending.slice(newline + 1);
					if (!line.trim()) continue;
					processing = processing.then(async () => handleEvent(JSON.parse(line) as BridgeEvent));
				}
			});

			await writeJson(child, bridgeRequest(model, context, options));
			await new Promise<void>((resolvePromise, reject) => {
				child.once("error", reject);
				child.once("close", () => resolvePromise());
			});
			if (pending.trim()) await handleEvent(JSON.parse(pending) as BridgeEvent);
			await processing;
			if (!terminal) {
				throw new Error(stderr.trim() || "Go Codex provider exited without a terminal event");
			}
			options?.signal?.removeEventListener("abort", abort);
		} catch (error) {
			if (!terminal) {
				output.stopReason = options?.signal?.aborted ? "aborted" : "error";
				output.errorMessage = error instanceof Error ? error.message : String(error);
				stream.push({ type: "error", reason: output.stopReason, error: output });
				stream.end();
			}
		}
	})();

	return stream;
}

export default function (pi: ExtensionAPI): void {
	const credentialCommand = process.env.PI_GO_CODEX_CREDENTIAL_COMMAND || defaultCredentialCommand;
	pi.registerProvider(PROVIDER_ID, {
		name: "Go Codex Bridge",
		baseUrl: "https://chatgpt.com/backend-api",
		apiKey: `!${credentialCommand}`,
		api: API_ID,
		models: [
			{
				id: "gpt-5.3-codex-spark",
				name: "GPT-5.3 Codex Spark (Go Codex Bridge)",
				reasoning: true,
				thinkingLevelMap: LEGACY_THINKING,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 128000,
				maxTokens: 128000,
			},
			{
				id: DEFAULT_MODEL,
				name: "GPT-5.5 (Go Codex Bridge)",
				reasoning: true,
				thinkingLevelMap: LEGACY_THINKING,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 272000,
				maxTokens: 128000,
			},
			{
				id: "gpt-5.6-luna",
				name: "GPT-5.6 Luna (Go Codex Bridge)",
				reasoning: true,
				thinkingLevelMap: GPT_56_THINKING,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 272000,
				maxTokens: 128000,
			},
			{
				id: "gpt-5.6-sol",
				name: "GPT-5.6 Sol (Go Codex Bridge)",
				reasoning: true,
				thinkingLevelMap: GPT_56_THINKING,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 272000,
				maxTokens: 128000,
			},
			{
				id: "gpt-5.6-terra",
				name: "GPT-5.6 Terra (Go Codex Bridge)",
				reasoning: true,
				thinkingLevelMap: GPT_56_THINKING,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 272000,
				maxTokens: 128000,
			},
		],
		streamSimple: streamGoCodex,
	});
}
