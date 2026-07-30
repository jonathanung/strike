export interface ProviderRequest {
  model: string;
  system?: string;
  messages: unknown[];
  tools?: unknown[];
  maxOutputTokens?: number;
  effort?: string;
  priority?: boolean;
}

export interface ModelResponse {
  text?: string;
  reasoning?: unknown[];
  toolCalls?: unknown[];
  stopReason?: string;
  usage?: unknown;
}

export interface HarnessResult extends ModelResponse {}

export interface HarnessInput {
  request: ProviderRequest;
  signal: AbortSignal;
}

export interface Provider {
  call(request: ProviderRequest): Promise<ModelResponse>;
}

export type Emit = (payload: unknown) => void;
export type Harness = (
  input: HarnessInput,
  provider: Provider,
  emit: Emit,
) => Promise<HarnessResult>;

export function runHarness(harness: Harness): void;
