export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };

export interface Limits {
  maxSteps?: number;
  maxProviderCalls?: number;
  maxToolCalls?: number;
  maxOutputTokens?: number;
  deadlineMs?: number;
}

export interface TextPart {
  type: "text";
  text: string;
}

export interface ImagePart {
  type: "image";
  mediaType: string;
  data?: string;
  url?: string;
}

export interface ToolCallPart {
  type: "toolCall";
  toolCallId: string;
  name: string;
  arguments: JsonValue;
}

export interface ToolResultPart {
  type: "toolResult";
  toolCallId: string;
  output: JsonValue;
  isError?: boolean;
}

export type ContentPart = TextPart | ImagePart | ToolCallPart | ToolResultPart;

export interface ProviderMessage {
	role: "system" | "user" | "assistant" | "tool";
	text?: string;
	images?: Array<{ mediaType: string; data: string }>;
	toolCalls?: Array<{ id: string; name: string; arguments: JsonValue }>;
	toolResult?: { callId: string; output: string; isError?: boolean };
	reasoning?: JsonValue[];
}

export interface ProviderRequest {
	model: string;
	system?: string;
	messages: ProviderMessage[];
	tools?: ToolDefinition[];
	maxOutputTokens?: number;
	effort?: "low" | "medium" | "high";
	priority?: boolean;
}

export interface ToolDefinition {
  name: string;
  description?: string;
  inputSchema: JsonValue;
}

interface MessageBase {
  version: 1;
  turnId: string;
}

export interface TurnStart extends MessageBase {
  type: "turn.start";
	agent: string;
	provider: string;
	request: ProviderRequest;
	capabilities: string[];
}

export interface ProviderCall extends MessageBase {
  type: "provider.call";
  callId: string;
	request: ProviderRequest;
}

interface ProviderEventBase extends MessageBase {
  type: "provider.event";
  callId: string;
  done?: boolean;
}

export type ProviderEvent = ProviderEventBase & (
	| { kind: "text"; text: string }
	| { kind: "reasoning"; reasoning: JsonValue }
	| { kind: "tool_call"; toolCall: { id: string; name: string; arguments: JsonValue } }
	| { kind: "usage"; usage: Record<string, JsonValue> }
	| { kind: "completion"; stopReason?: string }
	| { kind: "error"; error: string }
);

export interface ProgressEmit extends MessageBase {
  type: "progress.emit";
	payload: JsonValue;
}

export interface ToolExecute extends MessageBase {
  type: "tool.execute";
  toolCallId: string;
  name: string;
  arguments: JsonValue;
}

export interface ToolResult extends MessageBase {
  type: "tool.result";
  toolCallId: string;
	output?: string;
	error?: string;
}

export interface TurnComplete extends MessageBase {
  type: "turn.complete";
	text?: string;
	reasoning?: JsonValue[];
	toolCalls?: Array<{ id: string; name: string; arguments: JsonValue }>;
	stopReason?: string;
}

export interface TurnError extends MessageBase {
  type: "turn.error";
  code: string;
  message: string;
  retryable?: boolean;
}

export interface TurnCancel extends MessageBase {
  type: "turn.cancel";
  reason?: string;
}

export type HarnessMessage =
  | TurnStart
  | ProviderCall
  | ProviderEvent
  | ProgressEmit
  | ToolExecute
  | ToolResult
  | TurnComplete
  | TurnError
  | TurnCancel;
