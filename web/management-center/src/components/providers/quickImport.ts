import type { ProviderKeyConfig } from '@/types';

export type QuickImportProvider = 'codex' | 'claude';

export type QuickImportFields = {
  baseUrl: string;
  apiKey: string;
};

export type QuickImportConfigInput = {
  provider: QuickImportProvider;
  apiKey: string;
  baseUrl: string;
  priority: number;
  prefix: string;
};

export type QuickImportShortcutEvent = {
  key: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
};

const BASE_URL_FIELD_PATTERN =
  /(?:^|[\s,{])["']?(?:base_url|base-url|baseURL|url|endpoint|api_base)["']?\s*[:=]\s*["']?(https?:\/\/[^\s"',;)]+)["']?/i;
const API_KEY_FIELD_PATTERN =
  /(?:^|[\s,{])["']?(?:api_key|api-key|key|token)["']?\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{12,})["']?/i;
const AUTHORIZATION_PATTERN = /authorization\s*:\s*bearer\s+([A-Za-z0-9._~+/=-]{12,})/i;
const BEARER_PATTERN = /\bbearer\s+([A-Za-z0-9._~+/=-]{12,})/i;
const URL_PATTERN = /https?:\/\/[^\s"',;)]+/i;
const KEY_LIKE_PATTERN =
  /\b(?:sk-(?:ant-|proj-)?[A-Za-z0-9._~+/=-]{12,}|AIza[A-Za-z0-9._~+/=-]{12,}|[A-Za-z0-9._~+/=-]{24,})\b/;

const cleanUrl = (value: string) =>
  String(value ?? '')
    .trim()
    .replace(/^["'([{]+/g, '')
    .replace(/["',;)\]}]+$/g, '');

const cleanKey = (value: string) =>
  String(value ?? '')
    .trim()
    .replace(/^["'([{]+/g, '')
    .replace(/["',;)\]}]+$/g, '');

const isLikelyApiKey = (value: string) => {
  const key = cleanKey(value);
  if (key.length < 12) return false;
  if (/^https?:\/\//i.test(key)) return false;
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(key)) return false;
  if (/\s/.test(key)) return false;
  return KEY_LIKE_PATTERN.test(key);
};

export function extractQuickImportFields(text: string): QuickImportFields {
  const source = String(text ?? '');
  const namedUrl = source.match(BASE_URL_FIELD_PATTERN)?.[1] ?? '';
  const fallbackUrl = source.match(URL_PATTERN)?.[0] ?? '';
  const namedKey =
    source.match(AUTHORIZATION_PATTERN)?.[1] ??
    source.match(BEARER_PATTERN)?.[1] ??
    source.match(API_KEY_FIELD_PATTERN)?.[1] ??
    '';
  const fallbackKey = source.match(KEY_LIKE_PATTERN)?.[0] ?? '';
  const apiKey = cleanKey(namedKey || fallbackKey);

  return {
    baseUrl: cleanUrl(namedUrl || fallbackUrl),
    apiKey: isLikelyApiKey(apiKey) ? apiKey : '',
  };
}

export function normalizeQuickImportPriority(input: string): {
  value: number | null;
  error: string;
} {
  const trimmed = String(input ?? '').trim();
  if (!trimmed) return { value: 10, error: '' };
  if (!/^-?\d+$/.test(trimmed)) {
    return { value: null, error: 'Priority must be an integer.' };
  }
  return { value: Number.parseInt(trimmed, 10), error: '' };
}

export function buildQuickImportProviderConfig(input: QuickImportConfigInput): ProviderKeyConfig {
  const prefix = input.prefix.trim();
  return {
    apiKey: input.apiKey.trim(),
    baseUrl: input.baseUrl.trim(),
    priority: input.priority,
    ...(prefix ? { prefix } : {}),
    ...(input.provider === 'codex' ? { useV1: true } : {}),
  };
}

export function isQuickImportShortcut(event: QuickImportShortcutEvent): boolean {
  return (
    event.ctrlKey &&
    !event.altKey &&
    event.shiftKey &&
    !event.metaKey &&
    event.key.toLowerCase() === 'y'
  );
}
