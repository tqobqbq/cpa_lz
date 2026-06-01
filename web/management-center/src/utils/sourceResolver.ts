import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { CredentialInfo, SourceInfo } from '@/types/sourceInfo';

export interface SourceInfoMapInput {
  geminiApiKeys?: GeminiKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
}

type SourceInfoEntry = SourceInfo &
  Required<Pick<SourceInfo, 'displayName' | 'type' | 'identityKey'>>;

export interface SourceInfoMap {
  byAuthIndex: Map<string, SourceInfoEntry | null>;
  bySource: Map<string, SourceInfoEntry | null>;
}

const USAGE_SOURCE_PREFIX_KEY = 'k:';
const USAGE_SOURCE_PREFIX_MASKED = 'm:';
const USAGE_SOURCE_PREFIX_TEXT = 't:';
const DISABLE_ALL_MODELS_RULE = '*';

const KEY_LIKE_TOKEN_REGEX =
  /(sk-[A-Za-z0-9-_]{6,}|sk-ant-[A-Za-z0-9-_]{6,}|AIza[0-9A-Za-z-_]{8,}|AI[a-zA-Z0-9_-]{6,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})/;
const MASKED_TOKEN_HINT_REGEX = /^[^\s]{1,24}(\*{2,}|\.{3}|…)[^\s]{1,24}$/;
const keyFingerprintCache = new Map<string, string>();

const buildProviderIdentityKey = (type: string, index: number) => `${type}:${index}`;

const normalizeAuthIndex = (value: unknown) => {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value.toString();
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  return null;
};

const maskApiKey = (key: string): string => {
  const trimmed = String(key || '').trim();
  if (!trimmed) {
    return '';
  }

  const maskedLength = Math.max(10 - (trimmed.length < 4 ? 2 : 4), 1);
  const visibleChars = trimmed.length < 4 ? 1 : 2;
  return `${trimmed.slice(0, visibleChars)}${'*'.repeat(maskedLength)}${trimmed.slice(
    -visibleChars
  )}`;
};

const fnv1a64Hex = (value: string): string => {
  const cached = keyFingerprintCache.get(value);
  if (cached) return cached;

  const FNV_OFFSET_BASIS = 0xcbf29ce484222325n;
  const FNV_PRIME = 0x100000001b3n;

  let hash = FNV_OFFSET_BASIS;
  for (let i = 0; i < value.length; i++) {
    hash ^= BigInt(value.charCodeAt(i));
    hash = (hash * FNV_PRIME) & 0xffffffffffffffffn;
  }

  const hex = hash.toString(16).padStart(16, '0');
  keyFingerprintCache.set(value, hex);
  return hex;
};

const looksLikeRawSecret = (text: string): boolean => {
  if (!text || /\s/.test(text)) return false;

  const lower = text.toLowerCase();
  if (lower.endsWith('.json')) return false;
  if (lower.startsWith('http://') || lower.startsWith('https://')) return false;
  if (/[\\/]/.test(text)) return false;

  if (KEY_LIKE_TOKEN_REGEX.test(text)) return true;

  if (text.length >= 32 && text.length <= 512) {
    return true;
  }

  if (text.length >= 16 && text.length < 32 && /^[A-Za-z0-9._=-]+$/.test(text)) {
    return /[A-Za-z]/.test(text) && /\d/.test(text);
  }

  return false;
};

const extractRawSecretFromText = (text: string): string | null => {
  if (!text) return null;
  if (looksLikeRawSecret(text)) return text;

  const keyLikeMatch = text.match(KEY_LIKE_TOKEN_REGEX);
  if (keyLikeMatch?.[0]) return keyLikeMatch[0];

  const queryMatch = text.match(
    /(?:[?&])(api[-_]?key|key|token|access_token|authorization)=([^&#\s]+)/i
  );
  const queryValue = queryMatch?.[2];
  if (queryValue && looksLikeRawSecret(queryValue)) {
    return queryValue;
  }

  const headerMatch = text.match(
    /(api[-_]?key|key|token|access[-_]?token|authorization)\s*[:=]\s*([A-Za-z0-9._=-]+)/i
  );
  const headerValue = headerMatch?.[2];
  if (headerValue && looksLikeRawSecret(headerValue)) {
    return headerValue;
  }

  const bearerMatch = text.match(/\bBearer\s+([A-Za-z0-9._=-]{6,})/i);
  const bearerValue = bearerMatch?.[1];
  if (bearerValue && looksLikeRawSecret(bearerValue)) {
    return bearerValue;
  }

  return null;
};

const normalizeUsageSourceId = (value: unknown): string => {
  const raw =
    typeof value === 'string' ? value : value === null || value === undefined ? '' : String(value);
  const trimmed = raw.trim();
  if (!trimmed) return '';

  const extracted = extractRawSecretFromText(trimmed);
  if (extracted) {
    return `${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(extracted)}`;
  }

  if (MASKED_TOKEN_HINT_REGEX.test(trimmed)) {
    return `${USAGE_SOURCE_PREFIX_MASKED}${maskApiKey(trimmed)}`;
  }

  return `${USAGE_SOURCE_PREFIX_TEXT}${trimmed}`;
};

const buildCandidateUsageSourceIds = (input: {
  apiKey?: string;
  prefix?: string;
  displaySource?: string;
}): string[] => {
  const result: string[] = [];

  const displaySource = input.displaySource?.trim();
  if (displaySource) {
    result.push(`${USAGE_SOURCE_PREFIX_TEXT}${displaySource}`);
  }

  const prefix = input.prefix?.trim();
  if (prefix) {
    result.push(`${USAGE_SOURCE_PREFIX_TEXT}${prefix}`);
  }

  const apiKey = input.apiKey?.trim();
  if (apiKey) {
    result.push(normalizeUsageSourceId(apiKey));
    result.push(`${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(apiKey)}`);
    result.push(`${USAGE_SOURCE_PREFIX_MASKED}${maskApiKey(apiKey)}`);
  }

  return Array.from(new Set(result.filter(Boolean)));
};

const buildSourceLookupCandidates = (source: string): string[] =>
  Array.from(
    new Set([
      source,
      normalizeUsageSourceId(source),
      ...buildCandidateUsageSourceIds({ apiKey: source, prefix: source }),
    ])
  ).filter(Boolean);

const hasDisableAllModelsRule = (models?: string[]) =>
  Array.isArray(models) &&
  models.some((model) => String(model ?? '').trim() === DISABLE_ALL_MODELS_RULE);

const countHeaders = (headers?: Record<string, string>) =>
  headers && typeof headers === 'object' ? Object.keys(headers).length : 0;

const registerIdentity = (
  map: Map<string, SourceInfoEntry | null>,
  key: string | null | undefined,
  entry: SourceInfoEntry
) => {
  if (!key) return;

  const existing = map.get(key);
  if (existing === undefined) {
    map.set(key, entry);
    return;
  }

  if (existing === null) {
    return;
  }

  if (existing.identityKey === entry.identityKey) {
    map.set(key, { ...existing, ...entry });
    return;
  }

  map.set(key, null);
};

const formatRawSourceDisplayName = (source: string) => {
  if (!source) return '-';
  return source.startsWith('t:') ? source.slice(2) : source;
};

export function buildSourceInfoMap(input: SourceInfoMapInput): SourceInfoMap {
  const byAuthIndex = new Map<string, SourceInfoEntry | null>();
  const bySource = new Map<string, SourceInfoEntry | null>();

  const registerProvider = (
    entry: SourceInfoEntry,
    authIndices: Array<unknown>,
    candidates: Iterable<string>
  ) => {
    authIndices.forEach((authIndex) => {
      registerIdentity(byAuthIndex, normalizeAuthIndex(authIndex), entry);
    });

    Array.from(candidates).forEach((candidate) => {
      registerIdentity(bySource, candidate, entry);
    });
  };

  const providers: Array<{
    items: Array<{
      apiKey?: string;
      prefix?: string;
      authIndex?: string;
      baseUrl?: string;
      priority?: number;
      proxyUrl?: string;
      models?: unknown[];
      headers?: Record<string, string>;
      excludedModels?: string[];
    }>;
    type: string;
    label: string;
  }> = [
    { items: input.geminiApiKeys || [], type: 'gemini', label: 'Gemini' },
    { items: input.claudeApiKeys || [], type: 'claude', label: 'Claude' },
    { items: input.codexApiKeys || [], type: 'codex', label: 'Codex' },
    { items: input.vertexApiKeys || [], type: 'vertex', label: 'Vertex' },
  ];

  providers.forEach(({ items, type, label }) => {
    items.forEach((item, index) => {
      const identityKey = buildProviderIdentityKey(type, index);
      registerProvider(
        {
          displayName: item.prefix?.trim() || `${label} #${index}`,
          type,
          identityKey,
          editPath: `/ai-providers/${type}/${index}`,
          baseUrl: item.baseUrl,
          priority: item.priority,
          enabled: !hasDisableAllModelsRule(item.excludedModels),
          proxyUrl: item.proxyUrl,
          apiKeyCount: item.apiKey ? 1 : 0,
          modelCount: item.models?.length ?? 0,
          headerCount: countHeaders(item.headers),
        },
        [item.authIndex],
        [
          identityKey,
          `${label} #${index}`,
          `${type} #${index}`,
          ...buildCandidateUsageSourceIds({
            apiKey: item.apiKey,
            prefix: item.prefix,
            displaySource: `${type}#${index}`,
          }),
        ]
      );
    });
  });

  (input.openaiCompatibility || []).forEach((provider, providerIndex) => {
    const apiKeyEntries = provider.apiKeyEntries || [];
    const identityKey = buildProviderIdentityKey('openai', providerIndex);
    const providerName = String(provider.name ?? '').trim().toLowerCase() || 'openai-compatibility';
    const providerEntry: SourceInfoEntry = {
      displayName: provider.prefix?.trim() || provider.name?.trim() || `OpenAI #${providerIndex}`,
      type: 'openai',
      identityKey,
      editPath: `/ai-providers/openai/${providerIndex}`,
      baseUrl: provider.baseUrl,
      priority: provider.priority,
      enabled: true,
      apiKeyCount: apiKeyEntries.length,
      modelCount: provider.models?.length ?? 0,
      headerCount: countHeaders(provider.headers),
    };

    registerProvider(
      providerEntry,
      [provider.authIndex],
      [
        identityKey,
        `OpenAI #${providerIndex}`,
        `openai #${providerIndex}`,
        provider.name ?? '',
        ...buildCandidateUsageSourceIds({
          prefix: provider.prefix,
          displaySource: `${providerName}#${providerIndex}`,
        }),
      ]
    );

    apiKeyEntries.forEach((entry) => {
      registerProvider(
        {
          ...providerEntry,
          proxyUrl: entry.proxyUrl,
          headerCount: countHeaders(provider.headers) + countHeaders(entry.headers),
        },
        [entry.authIndex],
        buildCandidateUsageSourceIds({ apiKey: entry.apiKey })
      );
    });
  });

  return { byAuthIndex, bySource };
}

export function resolveSourceDisplay(
  sourceRaw: string,
  authIndex: unknown,
  sourceInfoMap: SourceInfoMap,
  authFileMap: Map<string, CredentialInfo>
): SourceInfo {
  const source = sourceRaw.trim();
  const authIndexKey = normalizeAuthIndex(authIndex);

  if (source) {
    for (const candidate of buildSourceLookupCandidates(source)) {
      const matchedBySource = sourceInfoMap.bySource.get(candidate);
      if (matchedBySource) {
        return matchedBySource;
      }
    }
  }

  if (authIndexKey) {
    const matchedByAuthIndex = sourceInfoMap.byAuthIndex.get(authIndexKey);
    if (matchedByAuthIndex) {
      return matchedByAuthIndex;
    }

    const authInfo = authFileMap.get(authIndexKey);
    if (authInfo && !source) {
      return {
        displayName: authInfo.name || authIndexKey,
        type: authInfo.type,
        identityKey: `auth:${authIndexKey}`,
      };
    }
  }

  if (source) {
    return {
      displayName: formatRawSourceDisplayName(source),
      type: '',
      identityKey: `source:${source}`,
    };
  }

  if (authIndexKey) {
    return {
      displayName: authIndexKey,
      type: '',
      identityKey: `auth:${authIndexKey}`,
    };
  }

  return {
    displayName: '-',
    type: '',
    identityKey: 'source:-',
  };
}
