export type ProviderTestRequest = {
  method: string;
  url: string;
  header?: Record<string, string>;
  data?: string;
  proxyUrl?: string;
};

export type ProviderTestRequestOptions = {
  apiKey?: string;
  authMode?: 'auto' | 'api-key' | 'bearer';
  baseUrl?: string;
  headers?: Record<string, string>;
  model: string;
  proxyUrl?: string;
  useV1?: boolean;
};

const DEFAULT_CODEX_USER_AGENT =
  'codex-tui/0.118.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9 (codex-tui; 0.118.0)';
const DEFAULT_CLAUDE_USER_AGENT = 'claude-cli/2.1.77 (external, cli)';
const DEFAULT_ANTHROPIC_VERSION = '2023-06-01';

const hasHeader = (headers: Record<string, string>, name: string) => {
  const target = name.toLowerCase();
  return Object.keys(headers).some((key) => key.toLowerCase() === target);
};

const resolveBearerTokenFromAuthorization = (headers: Record<string, string>): string => {
  const entry = Object.entries(headers).find(([key]) => key.toLowerCase() === 'authorization');
  if (!entry) return '';
  const value = String(entry[1] ?? '').trim();
  if (!value) return '';
  const match = value.match(/^Bearer\s+(.+)$/i);
  return match?.[1]?.trim() || '';
};

const normalizeBaseUrl = (baseUrl: string, fallback: string) => {
  let trimmed = String(baseUrl ?? '').trim();
  if (!trimmed) {
    return fallback;
  }
  trimmed = trimmed.replace(/\/?v0\/management\/?$/i, '');
  trimmed = trimmed.replace(/\/+$/g, '');
  if (!/^https?:\/\//i.test(trimmed)) {
    trimmed = `http://${trimmed}`;
  }
  return trimmed;
};

const buildOpenAIResponsesEndpoint = (baseUrl: string, useV1 = true) => {
  const trimmed = normalizeBaseUrl(baseUrl, 'https://api.openai.com');
  if (trimmed.endsWith('/responses')) return trimmed;
  if (trimmed.endsWith('/v1')) return `${trimmed}/responses`;
  if (!useV1) return `${trimmed}/responses`;
  return `${trimmed}/v1/responses`;
};

const buildClaudeMessagesEndpoint = (baseUrl: string) => {
  const trimmed = normalizeBaseUrl(baseUrl, 'https://api.anthropic.com');
  if (trimmed.endsWith('/v1/messages')) return trimmed;
  if (trimmed.endsWith('/v1')) return `${trimmed}/messages`;
  return `${trimmed}/v1/messages`;
};

const normalizeProxyUrl = (proxyUrl: string | undefined) => {
  const trimmed = String(proxyUrl ?? '').trim();
  return trimmed || undefined;
};

export function buildCodexProviderTestRequest(
  options: ProviderTestRequestOptions
): ProviderTestRequest {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers ?? {}),
  };
  const apiKey = String(options.apiKey ?? '').trim();

  if (!hasHeader(headers, 'authorization') && apiKey) {
    headers.Authorization = `Bearer ${apiKey}`;
  }
  if (!hasHeader(headers, 'user-agent')) {
    headers['User-Agent'] = DEFAULT_CODEX_USER_AGENT;
  }

  return {
    method: 'POST',
    url: buildOpenAIResponsesEndpoint(String(options.baseUrl ?? ''), options.useV1 !== false),
    header: headers,
    data: JSON.stringify({
      model: options.model,
      input: 'Hi',
      max_output_tokens: 128,
    }),
    proxyUrl: normalizeProxyUrl(options.proxyUrl),
  };
}

export function buildClaudeProviderTestRequest(
  options: ProviderTestRequestOptions
): ProviderTestRequest {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers ?? {}),
  };
  let resolvedApiKey = String(options.apiKey ?? '').trim();
  if (!resolvedApiKey && !hasHeader(headers, 'x-api-key')) {
    resolvedApiKey = resolveBearerTokenFromAuthorization(headers);
  }

  if (!hasHeader(headers, 'anthropic-version')) {
    headers['anthropic-version'] = DEFAULT_ANTHROPIC_VERSION;
  }
  if (!Object.prototype.hasOwnProperty.call(headers, 'Anthropic-Version')) {
    headers['Anthropic-Version'] = headers['anthropic-version'] ?? DEFAULT_ANTHROPIC_VERSION;
  }
  const authMode = options.authMode ?? 'auto';
  if (authMode === 'bearer') {
    if (!hasHeader(headers, 'authorization') && resolvedApiKey) {
      headers.Authorization = `Bearer ${resolvedApiKey}`;
    }
  } else if (!hasHeader(headers, 'x-api-key') && resolvedApiKey) {
    headers['x-api-key'] = resolvedApiKey;
  }
  if (
    authMode !== 'bearer' &&
    !Object.prototype.hasOwnProperty.call(headers, 'X-Api-Key') &&
    resolvedApiKey
  ) {
    headers['X-Api-Key'] = headers['x-api-key'] ?? resolvedApiKey;
  }
  if (!hasHeader(headers, 'user-agent')) {
    headers['User-Agent'] = DEFAULT_CLAUDE_USER_AGENT;
  }

  return {
    method: 'POST',
    url: buildClaudeMessagesEndpoint(String(options.baseUrl ?? '')),
    header: headers,
    data: JSON.stringify({
      model: options.model,
      max_tokens: 8,
      messages: [{ role: 'user', content: 'Hi' }],
    }),
    proxyUrl: normalizeProxyUrl(options.proxyUrl),
  };
}
