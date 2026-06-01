/**
 * 使用统计相关工具
 * 迁移自基线 modules/usage.js 的纯逻辑部分
 */

import type { ScriptableContext } from 'chart.js';
import type { LatencyAccumulator, LatencyStats } from './usage/latency';
import {
  addLatencySample,
  calculateLatencyStatsFromDetails,
  createLatencyAccumulator,
  extractLatencyMs,
  finalizeLatencyStats,
} from './usage/latency';
import { resolveModelPrice } from './usage/cost';
import { maskApiKey } from './format';
import { parseTimestampMs } from './timestamp';

const USAGE_BUCKET_DURATION_MS = 20 * 60 * 1000;

export type { DurationFormatOptions, LatencyStats } from './usage/latency';
export { resolveModelPrice } from './usage/cost';
export {
  LATENCY_SOURCE_FIELD,
  LATENCY_SOURCE_UNIT,
  calculateLatencyStatsFromDetails,
  extractLatencyMs,
  formatDurationMs,
} from './usage/latency';

export interface KeyStatBucket {
  success: number;
  failure: number;
}

export interface KeyStats {
  bySource: Record<string, KeyStatBucket>;
  byAuthIndex: Record<string, KeyStatBucket>;
}

export interface TokenBreakdown {
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  reasoningTokens: number;
}

export interface RateStats {
  rpm: number;
  tpm: number;
  windowMinutes: number;
  requestCount: number;
  tokenCount: number;
}

export interface Usage20mBucketStats {
  total_requests?: number;
  success_count?: number;
  failure_count?: number;
  input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
  cached_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_creation_5m_input_tokens?: number;
  cache_creation_1h_input_tokens?: number;
  cache_read_input_tokens?: number;
  total_tokens?: number;
  latency_total_ms?: number;
  latency_samples?: number;
}

export interface ModelPrice {
  input: number;
  cached_input: number;
  output: number;
  reasoning_output: number;
  cache_creation_input: number;
  cache_write_5m: number;
  cache_write_1h: number;
  cache_read: number;
}

export type ModelPriceFieldKey = keyof ModelPrice;

export const MODEL_PRICE_FIELDS: ReadonlyArray<{
  key: ModelPriceFieldKey;
  labelKey: string;
}> = [
  { key: 'input', labelKey: 'usage_stats.model_price_input' },
  { key: 'cached_input', labelKey: 'usage_stats.model_price_cached_input' },
  { key: 'output', labelKey: 'usage_stats.model_price_output' },
  { key: 'reasoning_output', labelKey: 'usage_stats.model_price_reasoning_output' },
  { key: 'cache_creation_input', labelKey: 'usage_stats.model_price_cache_creation_input' },
  { key: 'cache_write_5m', labelKey: 'usage_stats.model_price_cache_write_5m' },
  { key: 'cache_write_1h', labelKey: 'usage_stats.model_price_cache_write_1h' },
  { key: 'cache_read', labelKey: 'usage_stats.model_price_cache_read' },
];

export interface UsageDetail {
  timestamp: string;
  source: string;
  auth_index: string | number | null;
  remote_ip?: string;
  user_agent?: string;
  input_chars?: number;
  latency_ms?: number;
  status_code?: number;
  error_reason?: string;
  error_message?: string;
  tokens: {
    input_tokens: number;
    output_tokens: number;
    reasoning_tokens: number;
    cached_tokens: number;
    cache_tokens?: number;
    cache_creation_input_tokens?: number;
    cache_creation_5m_input_tokens?: number;
    cache_creation_1h_input_tokens?: number;
    cache_read_input_tokens?: number;
    total_tokens: number;
  };
  failed: boolean;
  __modelName?: string;
  __timestampMs?: number;
}

export interface UsageDetailWithEndpoint extends UsageDetail {
  __endpoint: string;
  __endpointMethod?: string;
  __endpointPath?: string;
  __timestampMs: number;
}

export interface UsageTokenBuckets {
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedInputTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheWrite5mTokens: number;
  cacheWrite1hTokens: number;
  cacheReadTokens: number;
  totalTokens: number;
}

export interface ApiStats {
  endpoint: string;
  totalRequests: number;
  successCount: number;
  failureCount: number;
  totalTokens: number;
  totalCost: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  averageLatencyMs: number | null;
  totalLatencyMs: number | null;
  latencySampleCount: number;
  models: Record<
    string,
    {
      requests: number;
      successCount: number;
      failureCount: number;
      tokens: number;
      inputTokens: number;
      outputTokens: number;
      reasoningTokens: number;
      cachedTokens: number;
      cacheCreationTokens: number;
      cacheReadTokens: number;
      averageLatencyMs: number | null;
      totalLatencyMs: number | null;
      latencySampleCount: number;
    }
  >;
}

export interface ModelStatsSummary {
  model: string;
  requests: number;
  successCount: number;
  failureCount: number;
  tokens: number;
  cost: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  averageLatencyMs: number | null;
  totalLatencyMs: number | null;
  latencySampleCount: number;
}

export type UsageTimeRange = '7h' | '24h' | '7d' | 'all';

const TOKENS_PER_PRICE_UNIT = 1_000_000;
const MODEL_PRICE_STORAGE_KEY = 'cli-proxy-model-prices-v2';
const createModelPrice = (
  input: number,
  cachedInput: number,
  output: number,
  reasoningOutput: number,
  cacheCreationInput: number,
  cacheWrite5m: number,
  cacheWrite1h: number,
  cacheRead: number
): ModelPrice => ({
  input,
  cached_input: cachedInput,
  output,
  reasoning_output: reasoningOutput,
  cache_creation_input: cacheCreationInput,
  cache_write_5m: cacheWrite5m,
  cache_write_1h: cacheWrite1h,
  cache_read: cacheRead,
});

export const DEFAULT_MODEL_PRICES: Record<string, ModelPrice> = {
  'gpt-5.3-codex': createModelPrice(1.75, 0.175, 14, 14, 0, 0, 0, 0),
  'gpt-5.4': createModelPrice(2.5, 0.25, 15, 15, 0, 0, 0, 0),
  'gpt-5.5': createModelPrice(5, 0.5, 30, 30, 0, 0, 0, 0),
  'gpt-5.4-pro': createModelPrice(30, 0, 180, 180, 0, 0, 0, 0),
  'gpt-5.4pro': createModelPrice(30, 0, 180, 180, 0, 0, 0, 0),
  'gpt-5.5-pro': createModelPrice(30, 0, 180, 180, 0, 0, 0, 0),
  'gpt-5.5pro': createModelPrice(30, 0, 180, 180, 0, 0, 0, 0),
  'claude-opus-4-7': createModelPrice(5, 0, 25, 25, 6.25, 6.25, 10, 0.5),
  'claude-opus-4-6': createModelPrice(5, 0, 25, 25, 6.25, 6.25, 10, 0.5),
  'claude-sonnet-4-6': createModelPrice(3, 0, 15, 15, 3.75, 3.75, 6, 0.3),
};
const USAGE_ENDPOINT_METHOD_REGEX = /^(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\s+(\S+)/i;
const USAGE_TIME_RANGE_MS: Record<Exclude<UsageTimeRange, 'all'>, number> = {
  '7h': 7 * 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const nonNegativeNumber = (value: unknown): number | undefined => {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
};

const tokenCount = (value: unknown): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? Math.max(parsed, 0) : 0;
};

export function normalizeModelPrice(value: unknown): ModelPrice | null {
  const record = isRecord(value) ? value : null;
  if (!record) {
    return null;
  }

  const inputRaw = nonNegativeNumber(record.input);
  const cachedInputRaw = nonNegativeNumber(record.cached_input);
  const outputRaw = nonNegativeNumber(record.output);
  const reasoningOutputRaw = nonNegativeNumber(record.reasoning_output);
  const cacheCreationInputRaw = nonNegativeNumber(record.cache_creation_input);
  const cacheWrite5mRaw = nonNegativeNumber(record.cache_write_5m);
  const cacheWrite1hRaw = nonNegativeNumber(record.cache_write_1h);
  const cacheReadRaw = nonNegativeNumber(record.cache_read);

  const legacyPromptRaw = nonNegativeNumber(record.prompt);
  const legacyCompletionRaw = nonNegativeNumber(record.completion);
  const legacyCacheRaw = nonNegativeNumber(record.cache);

  const hasAnyValue =
    inputRaw !== undefined ||
    cachedInputRaw !== undefined ||
    outputRaw !== undefined ||
    reasoningOutputRaw !== undefined ||
    cacheCreationInputRaw !== undefined ||
    cacheWrite5mRaw !== undefined ||
    cacheWrite1hRaw !== undefined ||
    cacheReadRaw !== undefined ||
    legacyPromptRaw !== undefined ||
    legacyCompletionRaw !== undefined ||
    legacyCacheRaw !== undefined;

  if (!hasAnyValue) {
    return null;
  }

  const input = inputRaw ?? legacyPromptRaw ?? 0;
  const cachedInput = cachedInputRaw ?? legacyCacheRaw ?? 0;
  const output = outputRaw ?? legacyCompletionRaw ?? 0;
  const reasoningOutput = reasoningOutputRaw ?? output;
  const cacheCreationInput = cacheCreationInputRaw ?? legacyCacheRaw ?? 0;
  const cacheWrite5m = cacheWrite5mRaw ?? cacheCreationInput;
  const cacheWrite1h = cacheWrite1hRaw ?? cacheCreationInput;
  const cacheRead = cacheReadRaw ?? legacyCacheRaw ?? cachedInput;

  return createModelPrice(
    input,
    cachedInput,
    output,
    reasoningOutput,
    cacheCreationInput,
    cacheWrite5m,
    cacheWrite1h,
    cacheRead
  );
}

const getApisRecord = (usageData: unknown): Record<string, unknown> | null => {
  const usageRecord = isRecord(usageData) ? usageData : null;
  const apisRaw = usageRecord ? usageRecord.apis : null;
  return isRecord(apisRaw) ? apisRaw : null;
};

export interface Usage20mSummaryEntry {
  provider: string;
  bucket: string;
  bucketStartMs: number;
  identity: string;
  model: string;
  stats: Usage20mBucketStats;
}

interface UsageSummary {
  totalRequests: number;
  successCount: number;
  failureCount: number;
  totalTokens: number;
}

const createUsageSummary = (): UsageSummary => ({
  totalRequests: 0,
  successCount: 0,
  failureCount: 0,
  totalTokens: 0,
});

const toUsageSummaryFields = (summary: UsageSummary) => ({
  total_requests: summary.totalRequests,
  success_count: summary.successCount,
  failure_count: summary.failureCount,
  total_tokens: summary.totalTokens,
});

const addUsageBucketToSummary = (summary: UsageSummary, stats: Usage20mBucketStats) => {
  summary.totalRequests += tokenCount(stats.total_requests);
  summary.successCount += tokenCount(stats.success_count);
  summary.failureCount += tokenCount(stats.failure_count);
  summary.totalTokens += tokenCount(stats.total_tokens);
};

export function collectUsage20mSummaryEntries(
  usageData: unknown,
  options: { windowStartMs?: number; windowEndMs?: number } = {}
): Usage20mSummaryEntry[] {
  const usageRecord = isRecord(usageData) ? usageData : null;
  const root = usageRecord && isRecord(usageRecord.usage_20m) ? usageRecord.usage_20m : null;
  if (!root) return [];

  const entries: Usage20mSummaryEntry[] = [];
  Object.entries(root).forEach(([provider, providerBuckets]) => {
    if (!isRecord(providerBuckets)) return;
    Object.entries(providerBuckets).forEach(([bucket, identityBuckets]) => {
      if (!isRecord(identityBuckets)) return;
      const bucketStartMs = parseTimestampMs(bucket);
      if (!Number.isFinite(bucketStartMs)) return;
      if (options.windowStartMs !== undefined && bucketStartMs < options.windowStartMs) return;
      if (options.windowEndMs !== undefined && bucketStartMs > options.windowEndMs) return;

      Object.entries(identityBuckets).forEach(([identity, models]) => {
        if (!isRecord(models)) return;
        Object.entries(models).forEach(([model, rawStats]) => {
          if (!isRecord(rawStats)) return;
          entries.push({
            provider,
            bucket,
            bucketStartMs,
            identity,
            model,
            stats: {
              total_requests: tokenCount(rawStats.total_requests),
              success_count: tokenCount(rawStats.success_count),
              failure_count: tokenCount(rawStats.failure_count),
              input_tokens: tokenCount(rawStats.input_tokens),
              output_tokens: tokenCount(rawStats.output_tokens),
              reasoning_tokens: tokenCount(rawStats.reasoning_tokens),
              cached_tokens: tokenCount(rawStats.cached_tokens),
              cache_creation_input_tokens: tokenCount(rawStats.cache_creation_input_tokens),
              cache_creation_5m_input_tokens: tokenCount(rawStats.cache_creation_5m_input_tokens),
              cache_creation_1h_input_tokens: tokenCount(rawStats.cache_creation_1h_input_tokens),
              cache_read_input_tokens: tokenCount(rawStats.cache_read_input_tokens),
              total_tokens: tokenCount(rawStats.total_tokens),
              latency_total_ms: tokenCount(rawStats.latency_total_ms),
              latency_samples: tokenCount(rawStats.latency_samples),
            },
          });
        });
      });
    });
  });

  return entries;
}

export function usageBucketTokenBuckets(stats: Usage20mBucketStats): UsageTokenBuckets {
  const inputTokens = tokenCount(stats.input_tokens);
  const outputTokens = tokenCount(stats.output_tokens);
  const reasoningTokens = tokenCount(stats.reasoning_tokens);
  const cacheWrite5mTokens = tokenCount(stats.cache_creation_5m_input_tokens);
  const cacheWrite1hTokens = tokenCount(stats.cache_creation_1h_input_tokens);
  const cacheCreationTokens = Math.max(
    tokenCount(stats.cache_creation_input_tokens),
    cacheWrite5mTokens + cacheWrite1hTokens
  );
  const cacheReadTokens = tokenCount(stats.cache_read_input_tokens);
  const cachedInputTokens = cacheReadTokens > 0 ? 0 : tokenCount(stats.cached_tokens);
  const cachedTokens = Math.max(cachedInputTokens, cacheReadTokens);
  const totalTokens =
    tokenCount(stats.total_tokens) ||
    inputTokens + outputTokens + cacheCreationTokens + cacheReadTokens;

  return {
    inputTokens,
    outputTokens,
    reasoningTokens,
    cachedInputTokens,
    cachedTokens,
    cacheCreationTokens,
    cacheWrite5mTokens,
    cacheWrite1hTokens,
    cacheReadTokens,
    totalTokens,
  };
}

function usageBucketLatencyStats(stats: Usage20mBucketStats): LatencyStats {
  const totalMs = tokenCount(stats.latency_total_ms);
  const sampleCount = tokenCount(stats.latency_samples);
  return {
    totalMs: sampleCount > 0 ? totalMs : null,
    averageMs: sampleCount > 0 ? totalMs / sampleCount : null,
    sampleCount,
  };
}

export function calculateUsageBucketCost(
  modelName: string,
  stats: Usage20mBucketStats,
  modelPrices: Record<string, ModelPrice>
): number {
  return calculateCostFromTokenBuckets(modelName, usageBucketTokenBuckets(stats), modelPrices);
}

function calculateCostFromTokenBuckets(
  modelName: string,
  buckets: UsageTokenBuckets,
  modelPrices: Record<string, ModelPrice>
): number {
  const price = resolveModelPrice(modelName, modelPrices);
  if (!price) return 0;

  const outputTokens = buckets.outputTokens;
  const reasoningTokens = Math.min(buckets.reasoningTokens, outputTokens);
  const visibleOutputTokens = Math.max(outputTokens - reasoningTokens, 0);
  const inputTokens = Math.max(buckets.inputTokens - buckets.cachedInputTokens, 0);
  const cacheCreationUnclassifiedTokens = Math.max(
    buckets.cacheCreationTokens - buckets.cacheWrite5mTokens - buckets.cacheWrite1hTokens,
    0
  );

  const total =
    (inputTokens / TOKENS_PER_PRICE_UNIT) * price.input +
    (buckets.cachedInputTokens / TOKENS_PER_PRICE_UNIT) * price.cached_input +
    (visibleOutputTokens / TOKENS_PER_PRICE_UNIT) * price.output +
    (reasoningTokens / TOKENS_PER_PRICE_UNIT) * price.reasoning_output +
    (cacheCreationUnclassifiedTokens / TOKENS_PER_PRICE_UNIT) * price.cache_creation_input +
    (buckets.cacheWrite5mTokens / TOKENS_PER_PRICE_UNIT) * price.cache_write_5m +
    (buckets.cacheWrite1hTokens / TOKENS_PER_PRICE_UNIT) * price.cache_write_1h +
    (buckets.cacheReadTokens / TOKENS_PER_PRICE_UNIT) * price.cache_read;

  return Number.isFinite(total) && total > 0 ? total : 0;
}

function buildUsageSnapshotFrom20mEntries<T>(
  usageData: T,
  entries: Usage20mSummaryEntry[]
): T {
  const usageRecord = isRecord(usageData) ? usageData : {};
  const totalSummary = createUsageSummary();
  const apis: Record<string, Record<string, unknown>> = {};
  const usage20m: Record<string, Record<string, Record<string, Record<string, Usage20mBucketStats>>>> = {};

  entries.forEach((entry) => {
    addUsageBucketToSummary(totalSummary, entry.stats);

    if (!usage20m[entry.provider]) usage20m[entry.provider] = {};
    if (!usage20m[entry.provider][entry.bucket]) usage20m[entry.provider][entry.bucket] = {};
    if (!usage20m[entry.provider][entry.bucket][entry.identity]) {
      usage20m[entry.provider][entry.bucket][entry.identity] = {};
    }
    usage20m[entry.provider][entry.bucket][entry.identity][entry.model] = entry.stats;

    const endpoint = `${entry.provider}/${entry.identity}`;
    const apiEntry = (apis[endpoint] ?? {
      total_requests: 0,
      success_count: 0,
      failure_count: 0,
      total_tokens: 0,
      models: {},
    }) as Record<string, unknown>;
    const models = isRecord(apiEntry.models) ? apiEntry.models : {};
    const modelEntry = (models[entry.model] ?? {
      total_requests: 0,
      success_count: 0,
      failure_count: 0,
      total_tokens: 0,
    }) as Record<string, unknown>;

    modelEntry.total_requests = tokenCount(modelEntry.total_requests) + tokenCount(entry.stats.total_requests);
    modelEntry.success_count = tokenCount(modelEntry.success_count) + tokenCount(entry.stats.success_count);
    modelEntry.failure_count = tokenCount(modelEntry.failure_count) + tokenCount(entry.stats.failure_count);
    modelEntry.total_tokens = tokenCount(modelEntry.total_tokens) + tokenCount(entry.stats.total_tokens);
    models[entry.model] = modelEntry;

    apiEntry.total_requests = tokenCount(apiEntry.total_requests) + tokenCount(entry.stats.total_requests);
    apiEntry.success_count = tokenCount(apiEntry.success_count) + tokenCount(entry.stats.success_count);
    apiEntry.failure_count = tokenCount(apiEntry.failure_count) + tokenCount(entry.stats.failure_count);
    apiEntry.total_tokens = tokenCount(apiEntry.total_tokens) + tokenCount(entry.stats.total_tokens);
    apiEntry.models = models;
    apis[endpoint] = apiEntry;
  });

  return {
    ...usageRecord,
    __usage20mDerivedApis: true,
    ...toUsageSummaryFields(totalSummary),
    apis,
    usage_20m: usage20m,
  } as T;
}

export function filterUsageByTimeRange<T>(
  usageData: T,
  range: UsageTimeRange,
  nowMs: number = Date.now()
): T {
  if (range === 'all') {
    return usageData;
  }

  const usageRecord = isRecord(usageData) ? usageData : null;
  if (!usageRecord) {
    return usageData;
  }

  const rangeMs = USAGE_TIME_RANGE_MS[range];
  if (!Number.isFinite(rangeMs) || rangeMs <= 0) {
    return usageData;
  }

  const windowStart = nowMs - rangeMs;
  const usage20mEntries = collectUsage20mSummaryEntries(usageData, {
    windowStartMs: windowStart,
    windowEndMs: nowMs,
  });
  if (usage20mEntries.length > 0) {
    return buildUsageSnapshotFrom20mEntries(usageData, usage20mEntries);
  }

  return filterUsageDetailsByTimeRange(usageData, range, nowMs);
}

export function filterUsageDetailsByTimeRange<T>(
  usageData: T,
  range: UsageTimeRange,
  nowMs: number = Date.now()
): T {
  if (range === 'all') {
    return usageData;
  }

  const usageRecord = isRecord(usageData) ? usageData : null;
  const apis = getApisRecord(usageData);
  if (!usageRecord || !apis) {
    return usageData;
  }

  const rangeMs = USAGE_TIME_RANGE_MS[range];
  if (!Number.isFinite(rangeMs) || rangeMs <= 0) {
    return usageData;
  }

  const windowStart = nowMs - rangeMs;
  const filteredApis: Record<string, unknown> = {};
  const totalSummary = createUsageSummary();

  Object.entries(apis).forEach(([apiName, apiEntry]) => {
    if (!isRecord(apiEntry)) {
      return;
    }

    const models = isRecord(apiEntry.models) ? apiEntry.models : null;
    if (!models) {
      return;
    }

    const filteredModels: Record<string, unknown> = {};
    const apiSummary = createUsageSummary();
    let hasModelData = false;

    Object.entries(models).forEach(([modelName, modelEntry]) => {
      if (!isRecord(modelEntry)) {
        return;
      }

      const detailsRaw = Array.isArray(modelEntry.details) ? modelEntry.details : [];
      const modelSummary = createUsageSummary();
      const filteredDetails: unknown[] = [];

      detailsRaw.forEach((detail) => {
        const detailRecord = isRecord(detail) ? detail : null;
        if (!detailRecord || typeof detailRecord.timestamp !== 'string') {
          return;
        }
        const timestamp = parseTimestampMs(detailRecord.timestamp);
        if (Number.isNaN(timestamp) || timestamp < windowStart || timestamp > nowMs) {
          return;
        }

        filteredDetails.push(detail);
        modelSummary.totalRequests += 1;
        if (detailRecord.failed === true) {
          modelSummary.failureCount += 1;
        } else {
          modelSummary.successCount += 1;
        }
        modelSummary.totalTokens += extractTotalTokens(detailRecord);
      });

      if (!filteredDetails.length) {
        return;
      }

      filteredModels[modelName] = {
        ...modelEntry,
        ...toUsageSummaryFields(modelSummary),
        details: filteredDetails,
      };
      hasModelData = true;

      apiSummary.totalRequests += modelSummary.totalRequests;
      apiSummary.successCount += modelSummary.successCount;
      apiSummary.failureCount += modelSummary.failureCount;
      apiSummary.totalTokens += modelSummary.totalTokens;
    });

    if (!hasModelData) {
      return;
    }

    filteredApis[apiName] = {
      ...apiEntry,
      ...toUsageSummaryFields(apiSummary),
      models: filteredModels,
    };

    totalSummary.totalRequests += apiSummary.totalRequests;
    totalSummary.successCount += apiSummary.successCount;
    totalSummary.failureCount += apiSummary.failureCount;
    totalSummary.totalTokens += apiSummary.totalTokens;
  });

  return {
    ...usageRecord,
    ...toUsageSummaryFields(totalSummary),
    apis: filteredApis,
  } as T;
}

export const normalizeAuthIndex = (value: unknown) => {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value.toString();
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  return null;
};

const USAGE_SOURCE_PREFIX_KEY = 'k:';
const USAGE_SOURCE_PREFIX_MASKED = 'm:';
const USAGE_SOURCE_PREFIX_TEXT = 't:';

const KEY_LIKE_TOKEN_REGEX =
  /(sk-[A-Za-z0-9-_]{6,}|sk-ant-[A-Za-z0-9-_]{6,}|AIza[0-9A-Za-z-_]{8,}|AI[a-zA-Z0-9_-]{6,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})/;
const MASKED_TOKEN_HINT_REGEX = /^[^\s]{1,24}(\*{2,}|\.{3}|…)[^\s]{1,24}$/;

const keyFingerprintCache = new Map<string, string>();

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

export function normalizeUsageSourceId(
  value: unknown,
  masker: (val: string) => string = maskApiKey
): string {
  const raw =
    typeof value === 'string' ? value : value === null || value === undefined ? '' : String(value);
  const trimmed = raw.trim();
  if (!trimmed) return '';

  const extracted = extractRawSecretFromText(trimmed);
  if (extracted) {
    return `${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(extracted)}`;
  }

  if (MASKED_TOKEN_HINT_REGEX.test(trimmed)) {
    return `${USAGE_SOURCE_PREFIX_MASKED}${masker(trimmed)}`;
  }

  return `${USAGE_SOURCE_PREFIX_TEXT}${trimmed}`;
}

export function buildCandidateUsageSourceIds(input: {
  apiKey?: string;
  prefix?: string;
}): string[] {
  const result: string[] = [];

  const prefix = input.prefix?.trim();
  if (prefix) {
    result.push(`${USAGE_SOURCE_PREFIX_TEXT}${prefix}`);
  }

  const apiKey = input.apiKey?.trim();
  if (apiKey) {
    // Include the normalised form first so that "non-standard" keys (e.g. short tokens,
    // keys containing '/' etc.) that are classified as text by normalizeUsageSourceId()
    // can still match usage details.
    result.push(normalizeUsageSourceId(apiKey));
    result.push(`${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(apiKey)}`);
    result.push(`${USAGE_SOURCE_PREFIX_MASKED}${maskApiKey(apiKey)}`);
  }

  return Array.from(new Set(result));
}

export function buildUsage20mIdentityCandidates(input: {
  authIndex?: unknown;
  apiKey?: string;
  prefix?: string;
}): string[] {
  const result: string[] = [];
  const authIndex = normalizeAuthIndex(input.authIndex);
  if (authIndex) {
    result.push(`auth_index:${authIndex}`);
  }

  const sourceCandidates = buildCandidateUsageSourceIds({
    apiKey: input.apiKey,
    prefix: input.prefix,
  });
  const apiKey = input.apiKey?.trim();
  if (apiKey) {
    result.push(`api_key:${fnv1a64Hex(apiKey)}`);
  }
  sourceCandidates.forEach((candidate) => {
    result.push(`source:${candidate}`);
    const rawText = candidate.match(/^[kmt]:/) ? candidate.slice(2) : candidate;
    if (rawText) {
      result.push(`source:${rawText}`);
    }
  });

  return Array.from(new Set(result));
}

/**
 * 对使用数据中的敏感字段进行遮罩
 */
export function maskUsageSensitiveValue(
  value: unknown,
  masker: (val: string) => string = maskApiKey
): string {
  if (value === null || value === undefined) {
    return '';
  }
  const raw = typeof value === 'string' ? value : String(value);
  if (!raw) {
    return '';
  }

  let masked = raw;

  const queryRegex = /([?&])(api[-_]?key|key|token|access_token|authorization)=([^&#\s]+)/gi;
  masked = masked.replace(
    queryRegex,
    (_full, prefix, keyName, valuePart) => `${prefix}${keyName}=${masker(valuePart)}`
  );

  const headerRegex =
    /(api[-_]?key|key|token|access[-_]?token|authorization)\s*([:=])\s*([A-Za-z0-9._-]+)/gi;
  masked = masked.replace(
    headerRegex,
    (_full, keyName, separator, valuePart) => `${keyName}${separator}${masker(valuePart)}`
  );

  const keyLikeRegex =
    /(sk-[A-Za-z0-9]{6,}|AI[a-zA-Z0-9_-]{6,}|AIza[0-9A-Za-z-_]{8,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})/g;
  masked = masked.replace(keyLikeRegex, (match) => masker(match));

  if (masked === raw) {
    const trimmed = raw.trim();
    if (trimmed && !/\s/.test(trimmed)) {
      const looksLikeKey =
        /^sk-/i.test(trimmed) ||
        /^AI/i.test(trimmed) ||
        /^AIza/i.test(trimmed) ||
        /^hf_/i.test(trimmed) ||
        /^pk_/i.test(trimmed) ||
        /^rk_/i.test(trimmed) ||
        (!/[\\/]/.test(trimmed) && (/\d/.test(trimmed) || trimmed.length >= 10)) ||
        trimmed.length >= 24;
      if (looksLikeKey) {
        return masker(trimmed);
      }
    }
  }

  return masked;
}

/**
 * 格式化每分钟数值
 */
export function formatPerMinuteValue(value: number): string {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return '0.00';
  }
  const abs = Math.abs(num);
  if (abs >= 1000) {
    return Math.round(num).toLocaleString();
  }
  if (abs >= 100) {
    return num.toFixed(0);
  }
  if (abs >= 10) {
    return num.toFixed(1);
  }
  return num.toFixed(2);
}

/**
 * 格式化紧凑数字
 */
export function formatCompactNumber(value: number): string {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return '0';
  }
  const abs = Math.abs(num);
  if (abs >= 1_000_000) {
    return `${(num / 1_000_000).toFixed(1)}M`;
  }
  if (abs >= 1_000) {
    return `${(num / 1_000).toFixed(1)}K`;
  }
  return abs >= 1 ? num.toFixed(0) : num.toFixed(2);
}

/**
 * 格式化美元
 */
export function formatUsd(value: number): string {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return '$0.00';
  }
  const fixed = num.toFixed(2);
  const parts = Number(fixed).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
  return `$${parts}`;
}

const usageDetailsCache = new WeakMap<object, UsageDetail[]>();
const usageDetailsWithEndpointCache = new WeakMap<object, UsageDetailWithEndpoint[]>();

/**
 * 从使用数据中收集所有请求明细
 */
export function collectUsageDetails(usageData: unknown): UsageDetail[] {
  const cacheKey = isRecord(usageData) ? (usageData as object) : null;
  if (cacheKey) {
    const cached = usageDetailsCache.get(cacheKey);
    if (cached) return cached;
  }

  const apis = getApisRecord(usageData);
  if (!apis) return [];
  const details: UsageDetail[] = [];
  const sourceCache = new Map<string, string>();

  const normalizeSource = (value: unknown): string => {
    const raw =
      typeof value === 'string'
        ? value
        : value === null || value === undefined
          ? ''
          : String(value);
    const trimmed = raw.trim();
    if (!trimmed) return '';
    const cached = sourceCache.get(trimmed);
    if (cached !== undefined) return cached;
    const normalized = normalizeUsageSourceId(trimmed);
    sourceCache.set(trimmed, normalized);
    return normalized;
  };

  Object.values(apis).forEach((apiEntry) => {
    if (!isRecord(apiEntry)) return;
    const modelsRaw = apiEntry.models;
    const models = isRecord(modelsRaw) ? modelsRaw : null;
    if (!models) return;

    Object.entries(models).forEach(([modelName, modelEntry]) => {
      if (!isRecord(modelEntry)) return;
      const modelDetailsRaw = modelEntry.details;
      const modelDetails = Array.isArray(modelDetailsRaw) ? modelDetailsRaw : [];

      modelDetails.forEach((detailRaw) => {
        if (!isRecord(detailRaw) || typeof detailRaw.timestamp !== 'string') return;
        const timestamp = detailRaw.timestamp;
        const timestampMs = parseTimestampMs(timestamp);
        const tokensRaw = isRecord(detailRaw.tokens) ? detailRaw.tokens : {};
        const latencyMs = extractLatencyMs(detailRaw);
        details.push({
          timestamp,
          source: normalizeSource(detailRaw.source),
          auth_index:
            (detailRaw?.auth_index ??
              detailRaw?.authIndex ??
              detailRaw?.AuthIndex ??
              null) as UsageDetail['auth_index'],
          remote_ip:
            typeof detailRaw.remote_ip === 'string'
              ? detailRaw.remote_ip
              : typeof detailRaw.remoteIP === 'string'
                ? detailRaw.remoteIP
                : undefined,
          user_agent:
            typeof detailRaw.user_agent === 'string'
              ? detailRaw.user_agent
              : typeof detailRaw.userAgent === 'string'
                ? detailRaw.userAgent
                : undefined,
          input_chars:
            typeof detailRaw.input_chars === 'number'
              ? detailRaw.input_chars
              : typeof detailRaw.inputChars === 'number'
                ? detailRaw.inputChars
                : undefined,
          latency_ms: latencyMs ?? undefined,
          status_code:
            typeof detailRaw.status_code === 'number'
              ? detailRaw.status_code
              : typeof detailRaw.statusCode === 'number'
                ? detailRaw.statusCode
                : undefined,
          error_reason:
            typeof detailRaw.error_reason === 'string'
              ? detailRaw.error_reason
              : typeof detailRaw.errorReason === 'string'
                ? detailRaw.errorReason
                : undefined,
          error_message:
            typeof detailRaw.error_message === 'string'
              ? detailRaw.error_message
              : typeof detailRaw.errorMessage === 'string'
                ? detailRaw.errorMessage
                : undefined,
          tokens: tokensRaw as unknown as UsageDetail['tokens'],
          failed: detailRaw.failed === true,
          __modelName: modelName,
          __timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
        });
      });
    });
  });

  if (cacheKey) {
    usageDetailsCache.set(cacheKey, details);
  }
  return details;
}

/**
 * 从使用数据中收集包含 endpoint/method/path 的请求明细
 */
export function collectUsageDetailsWithEndpoint(usageData: unknown): UsageDetailWithEndpoint[] {
  const cacheKey = isRecord(usageData) ? (usageData as object) : null;
  if (cacheKey) {
    const cached = usageDetailsWithEndpointCache.get(cacheKey);
    if (cached) return cached;
  }

  const apis = getApisRecord(usageData);
  if (!apis) return [];

  const details: UsageDetailWithEndpoint[] = [];
  const sourceCache = new Map<string, string>();

  const normalizeSource = (value: unknown): string => {
    const raw =
      typeof value === 'string'
        ? value
        : value === null || value === undefined
          ? ''
          : String(value);
    const trimmed = raw.trim();
    if (!trimmed) return '';
    const cached = sourceCache.get(trimmed);
    if (cached !== undefined) return cached;
    const normalized = normalizeUsageSourceId(trimmed);
    sourceCache.set(trimmed, normalized);
    return normalized;
  };

  Object.entries(apis).forEach(([endpoint, apiEntry]) => {
    if (!isRecord(apiEntry)) return;
    const modelsRaw = apiEntry.models;
    const models = isRecord(modelsRaw) ? modelsRaw : null;
    if (!models) return;

    const endpointMatch = endpoint.match(USAGE_ENDPOINT_METHOD_REGEX);
    const endpointMethod = endpointMatch?.[1]?.toUpperCase();
    const endpointPath = endpointMatch?.[2];

    Object.entries(models).forEach(([modelName, modelEntry]) => {
      if (!isRecord(modelEntry)) return;
      const modelDetailsRaw = modelEntry.details;
      const modelDetails = Array.isArray(modelDetailsRaw) ? modelDetailsRaw : [];

      modelDetails.forEach((detailRaw) => {
        if (!isRecord(detailRaw) || typeof detailRaw.timestamp !== 'string') return;
        const timestamp = detailRaw.timestamp;
        const timestampMs = parseTimestampMs(timestamp);
        const tokensRaw = isRecord(detailRaw.tokens) ? detailRaw.tokens : {};
        const latencyMs = extractLatencyMs(detailRaw);
        details.push({
          timestamp,
          source: normalizeSource(detailRaw.source),
          auth_index:
            (detailRaw?.auth_index ??
              detailRaw?.authIndex ??
              detailRaw?.AuthIndex ??
              null) as UsageDetail['auth_index'],
          remote_ip:
            typeof detailRaw.remote_ip === 'string'
              ? detailRaw.remote_ip
              : typeof detailRaw.remoteIP === 'string'
                ? detailRaw.remoteIP
                : undefined,
          user_agent:
            typeof detailRaw.user_agent === 'string'
              ? detailRaw.user_agent
              : typeof detailRaw.userAgent === 'string'
                ? detailRaw.userAgent
                : undefined,
          input_chars:
            typeof detailRaw.input_chars === 'number'
              ? detailRaw.input_chars
              : typeof detailRaw.inputChars === 'number'
                ? detailRaw.inputChars
                : undefined,
          latency_ms: latencyMs ?? undefined,
          status_code:
            typeof detailRaw.status_code === 'number'
              ? detailRaw.status_code
              : typeof detailRaw.statusCode === 'number'
                ? detailRaw.statusCode
                : undefined,
          error_reason:
            typeof detailRaw.error_reason === 'string'
              ? detailRaw.error_reason
              : typeof detailRaw.errorReason === 'string'
                ? detailRaw.errorReason
                : undefined,
          error_message:
            typeof detailRaw.error_message === 'string'
              ? detailRaw.error_message
              : typeof detailRaw.errorMessage === 'string'
                ? detailRaw.errorMessage
                : undefined,
          tokens: tokensRaw as unknown as UsageDetail['tokens'],
          failed: detailRaw.failed === true,
          __modelName: modelName,
          __endpoint: endpoint,
          __endpointMethod: endpointMethod,
          __endpointPath: endpointPath,
          __timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
        });
      });
    });
  });

  if (cacheKey) {
    usageDetailsWithEndpointCache.set(cacheKey, details);
  }
  return details;
}

/**
 * 从单条明细提取总 tokens
 */
export function extractTotalTokens(detail: unknown): number {
  return getUsageTokenBuckets(detail).totalTokens;
}

export function getUsageTokenBuckets(detail: unknown): UsageTokenBuckets {
  const record = isRecord(detail) ? detail : null;
  const tokensRaw = record?.tokens;
  const tokens = isRecord(tokensRaw) ? tokensRaw : {};

  const inputTokens = tokenCount(tokens.input_tokens);
  const outputTokens = tokenCount(tokens.output_tokens);
  const reasoningTokens = tokenCount(tokens.reasoning_tokens);
  const cacheWrite5mTokens = tokenCount(tokens.cache_creation_5m_input_tokens);
  const cacheWrite1hTokens = tokenCount(tokens.cache_creation_1h_input_tokens);
  const cacheCreationTokens = Math.max(
    tokenCount(tokens.cache_creation_input_tokens),
    cacheWrite5mTokens + cacheWrite1hTokens
  );
  const cacheReadTokens = tokenCount(tokens.cache_read_input_tokens);
  const hasProviderCacheBuckets = cacheCreationTokens > 0 || cacheReadTokens > 0;
  const cachedInputTokens = hasProviderCacheBuckets
    ? 0
    : Math.max(tokenCount(tokens.cached_tokens), tokenCount(tokens.cache_tokens));
  const cachedTokens = Math.max(cachedInputTokens, cacheReadTokens);
  const explicitTotalTokens = tokenCount(tokens.total_tokens);
  const outputTotalTokens = outputTokens > 0 ? outputTokens : reasoningTokens;
  const inputTotalTokens =
    inputTokens > 0 || cacheCreationTokens > 0 || cacheReadTokens > 0
      ? inputTokens + cacheCreationTokens + cacheReadTokens
      : cachedInputTokens;
  const totalTokens =
    explicitTotalTokens > 0 ? explicitTotalTokens : inputTotalTokens + outputTotalTokens;

  return {
    inputTokens,
    outputTokens,
    reasoningTokens,
    cachedInputTokens,
    cachedTokens,
    cacheCreationTokens,
    cacheWrite5mTokens,
    cacheWrite1hTokens,
    cacheReadTokens,
    totalTokens,
  };
}

/**
 * 计算耗时统计
 */
export function calculateLatencyStats(usageData: unknown): LatencyStats {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    let totalMs = 0;
    let sampleCount = 0;
    summaryEntries.forEach((entry) => {
      totalMs += tokenCount(entry.stats.latency_total_ms);
      sampleCount += tokenCount(entry.stats.latency_samples);
    });
    return {
      totalMs: sampleCount > 0 ? totalMs : null,
      averageMs: sampleCount > 0 ? totalMs / sampleCount : null,
      sampleCount,
    };
  }
  return calculateLatencyStatsFromDetails(collectUsageDetails(usageData));
}

/**
 * 计算 token 分类统计
 */
export function calculateTokenBreakdown(usageData: unknown): TokenBreakdown {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    return summaryEntries.reduce<TokenBreakdown>(
      (acc, entry) => {
        const buckets = usageBucketTokenBuckets(entry.stats);
        acc.cachedTokens += buckets.cachedInputTokens;
        acc.cacheCreationTokens += buckets.cacheCreationTokens;
        acc.cacheReadTokens += buckets.cacheReadTokens;
        acc.reasoningTokens += buckets.reasoningTokens;
        return acc;
      },
      { cachedTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0, reasoningTokens: 0 }
    );
  }

  const details = collectUsageDetails(usageData);
  if (!details.length) {
    return { cachedTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0, reasoningTokens: 0 };
  }

  let cachedTokens = 0;
  let cacheCreationTokens = 0;
  let cacheReadTokens = 0;
  let reasoningTokens = 0;

  details.forEach((detail) => {
    const buckets = getUsageTokenBuckets(detail);
    cachedTokens += buckets.cachedInputTokens;
    cacheCreationTokens += buckets.cacheCreationTokens;
    cacheReadTokens += buckets.cacheReadTokens;
    reasoningTokens += buckets.reasoningTokens;
  });

  return { cachedTokens, cacheCreationTokens, cacheReadTokens, reasoningTokens };
}

/**
 * 计算最近 N 分钟的 RPM/TPM
 */
export function calculateRecentPerMinuteRates(
  windowMinutes: number = 30,
  usageData: unknown
): RateStats {
  const details = collectUsageDetails(usageData);
  const effectiveWindow = Number.isFinite(windowMinutes) && windowMinutes > 0 ? windowMinutes : 30;

  if (!details.length) {
    return { rpm: 0, tpm: 0, windowMinutes: effectiveWindow, requestCount: 0, tokenCount: 0 };
  }

  const now = Date.now();
  const windowStart = now - effectiveWindow * 60 * 1000;
  let requestCount = 0;
  let tokenCount = 0;

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp < windowStart || timestamp > now) {
      return;
    }
    requestCount += 1;
    tokenCount += extractTotalTokens(detail);
  });

  const denominator = effectiveWindow > 0 ? effectiveWindow : 1;
  return {
    rpm: requestCount / denominator,
    tpm: tokenCount / denominator,
    windowMinutes: effectiveWindow,
    requestCount,
    tokenCount,
  };
}

/**
 * 从使用数据获取模型名称列表
 */
export function getModelNamesFromUsage(usageData: unknown): string[] {
  const apis = getApisRecord(usageData);
  if (!apis) return [];
  const names = new Set<string>();
  Object.values(apis).forEach((apiEntry) => {
    if (!isRecord(apiEntry)) return;
    const modelsRaw = apiEntry.models;
    const models = isRecord(modelsRaw) ? modelsRaw : null;
    if (!models) return;
    Object.keys(models).forEach((modelName) => {
      if (modelName) {
        names.add(modelName);
      }
    });
  });
  return Array.from(names).sort((a, b) => a.localeCompare(b));
}

/**
 * 计算成本数据
 */
export function calculateCost(
  detail: UsageDetail,
  modelPrices: Record<string, ModelPrice>
): number {
  const modelName = detail.__modelName || '';
  return calculateCostFromTokenBuckets(modelName, getUsageTokenBuckets(detail), modelPrices);
}

/**
 * 计算总成本
 */
export function calculateTotalCost(
  usageData: unknown,
  modelPrices: Record<string, ModelPrice>
): number {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    return summaryEntries.reduce(
      (sum, entry) => sum + calculateUsageBucketCost(entry.model, entry.stats, modelPrices),
      0
    );
  }

  const details = collectUsageDetails(usageData);
  if (!details.length || !Object.keys(modelPrices).length) {
    return 0;
  }
  return details.reduce((sum, detail) => sum + calculateCost(detail, modelPrices), 0);
}

/**
 * 从 localStorage 加载模型价格
 */
export function loadModelPrices(): Record<string, ModelPrice> {
  const defaults = { ...DEFAULT_MODEL_PRICES };
  try {
    if (typeof localStorage === 'undefined') {
      return defaults;
    }
    const raw = localStorage.getItem(MODEL_PRICE_STORAGE_KEY);
    if (!raw) {
      return defaults;
    }
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) {
      return defaults;
    }
    const normalized: Record<string, ModelPrice> = {};
    Object.entries(parsed).forEach(([model, price]: [string, unknown]) => {
      if (!model) return;
      const normalizedPrice = normalizeModelPrice(price);
      if (normalizedPrice) normalized[model] = normalizedPrice;
    });
    return { ...defaults, ...normalized };
  } catch {
    return defaults;
  }
}

/**
 * 保存模型价格到 localStorage
 */
export function saveModelPrices(prices: Record<string, ModelPrice>): void {
  try {
    if (typeof localStorage === 'undefined') {
      return;
    }
    localStorage.setItem(MODEL_PRICE_STORAGE_KEY, JSON.stringify(prices));
  } catch {
    console.warn('保存模型价格失败');
  }
}

/**
 * 获取 API 统计数据
 */
export function getApiStats(
  usageData: unknown,
  modelPrices: Record<string, ModelPrice>
): ApiStats[] {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  const apis = getApisRecord(usageData);
  const usageRecord = isRecord(usageData) ? usageData : null;
  const shouldUseUsage20mForApiStats =
    summaryEntries.length > 0 &&
    (!apis || usageRecord?.__usage20mDerivedApis === true);

  if (shouldUseUsage20mForApiStats) {
    const apiMap = new Map<string, ApiStats>();
    summaryEntries.forEach((entry) => {
      const endpoint = `${entry.provider}/${entry.identity}`;
      const apiStats =
        apiMap.get(endpoint) ??
        ({
          endpoint: maskUsageSensitiveValue(endpoint) || endpoint,
          totalRequests: 0,
          successCount: 0,
          failureCount: 0,
          totalTokens: 0,
          totalCost: 0,
          inputTokens: 0,
          outputTokens: 0,
          reasoningTokens: 0,
          cachedTokens: 0,
          cacheCreationTokens: 0,
          cacheReadTokens: 0,
          averageLatencyMs: null,
          totalLatencyMs: null,
          latencySampleCount: 0,
          models: {},
        } satisfies ApiStats);
      const modelStats =
        apiStats.models[entry.model] ?? {
          requests: 0,
          successCount: 0,
          failureCount: 0,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          reasoningTokens: 0,
          cachedTokens: 0,
          cacheCreationTokens: 0,
          cacheReadTokens: 0,
          averageLatencyMs: null,
          totalLatencyMs: null,
          latencySampleCount: 0,
        };

      const requests = tokenCount(entry.stats.total_requests);
      const success = tokenCount(entry.stats.success_count);
      const failure = tokenCount(entry.stats.failure_count);
      const buckets = usageBucketTokenBuckets(entry.stats);
      const latency = usageBucketLatencyStats(entry.stats);
      const tokens = buckets.totalTokens;
      modelStats.requests += requests;
      modelStats.successCount += success;
      modelStats.failureCount += failure;
      modelStats.tokens += tokens;
      modelStats.inputTokens += buckets.inputTokens;
      modelStats.outputTokens += buckets.outputTokens;
      modelStats.reasoningTokens += buckets.reasoningTokens;
      modelStats.cachedTokens += buckets.cachedInputTokens;
      modelStats.cacheCreationTokens += buckets.cacheCreationTokens;
      modelStats.cacheReadTokens += buckets.cacheReadTokens;
      modelStats.totalLatencyMs = (modelStats.totalLatencyMs ?? 0) + (latency.totalMs ?? 0);
      modelStats.latencySampleCount += latency.sampleCount;
      modelStats.averageLatencyMs =
        modelStats.latencySampleCount > 0
          ? (modelStats.totalLatencyMs ?? 0) / modelStats.latencySampleCount
          : null;
      apiStats.models[entry.model] = modelStats;
      apiStats.totalRequests += requests;
      apiStats.successCount += success;
      apiStats.failureCount += failure;
      apiStats.totalTokens += tokens;
      apiStats.inputTokens += buckets.inputTokens;
      apiStats.outputTokens += buckets.outputTokens;
      apiStats.reasoningTokens += buckets.reasoningTokens;
      apiStats.cachedTokens += buckets.cachedInputTokens;
      apiStats.cacheCreationTokens += buckets.cacheCreationTokens;
      apiStats.cacheReadTokens += buckets.cacheReadTokens;
      apiStats.totalLatencyMs = (apiStats.totalLatencyMs ?? 0) + (latency.totalMs ?? 0);
      apiStats.latencySampleCount += latency.sampleCount;
      apiStats.averageLatencyMs =
        apiStats.latencySampleCount > 0
          ? (apiStats.totalLatencyMs ?? 0) / apiStats.latencySampleCount
          : null;
      apiStats.totalCost += calculateUsageBucketCost(entry.model, entry.stats, modelPrices);
      apiMap.set(endpoint, apiStats);
    });
    return Array.from(apiMap.values()).sort((a, b) => b.totalRequests - a.totalRequests);
  }

  if (!apis) return [];
  const result: ApiStats[] = [];

  Object.entries(apis).forEach(([endpoint, apiData]) => {
    if (!isRecord(apiData)) return;
    const models: Record<
      string,
      ApiStats['models'][string]
    > = {};
    let derivedSuccessCount = 0;
    let derivedFailureCount = 0;
    let totalCost = 0;
    let inputTokens = 0;
    let outputTokens = 0;
    let reasoningTokens = 0;
    let cachedTokens = 0;
    let cacheCreationTokens = 0;
    let cacheReadTokens = 0;
    const apiLatency = createLatencyAccumulator();

    const modelsData = isRecord(apiData.models) ? apiData.models : {};
    Object.entries(modelsData).forEach(([modelName, modelData]) => {
      if (!isRecord(modelData)) return;
      const details = Array.isArray(modelData.details) ? modelData.details : [];
      const hasExplicitCounts =
        typeof modelData.success_count === 'number' || typeof modelData.failure_count === 'number';

      let successCount = 0;
      let failureCount = 0;
      let modelInputTokens = 0;
      let modelOutputTokens = 0;
      let modelReasoningTokens = 0;
      let modelCachedTokens = 0;
      let modelCacheCreationTokens = 0;
      let modelCacheReadTokens = 0;
      const modelLatency = createLatencyAccumulator();
      if (hasExplicitCounts) {
        successCount += Number(modelData.success_count) || 0;
        failureCount += Number(modelData.failure_count) || 0;
      }

      const price = resolveModelPrice(modelName, modelPrices);
      if (details.length > 0 && (!hasExplicitCounts || price)) {
        details.forEach((detail) => {
          const detailRecord = isRecord(detail) ? detail : null;
          if (!hasExplicitCounts) {
            if (detailRecord?.failed === true) {
              failureCount += 1;
            } else {
              successCount += 1;
            }
          }

          if (price && detailRecord) {
            totalCost += calculateCost(
              { ...(detailRecord as unknown as UsageDetail), __modelName: modelName },
              modelPrices
            );
          }
          if (detailRecord) {
            const buckets = getUsageTokenBuckets(detailRecord as unknown as UsageDetail);
            modelInputTokens += buckets.inputTokens;
            modelOutputTokens += buckets.outputTokens;
            modelReasoningTokens += buckets.reasoningTokens;
            modelCachedTokens += buckets.cachedInputTokens;
            modelCacheCreationTokens += buckets.cacheCreationTokens;
            modelCacheReadTokens += buckets.cacheReadTokens;
            addLatencySample(modelLatency, extractLatencyMs(detailRecord));
            addLatencySample(apiLatency, extractLatencyMs(detailRecord));
          }
        });
      }
      const modelLatencyStats = finalizeLatencyStats(modelLatency);

      models[modelName] = {
        requests: Number(modelData.total_requests) || 0,
        successCount,
        failureCount,
        tokens: Number(modelData.total_tokens) || 0,
        inputTokens: modelInputTokens,
        outputTokens: modelOutputTokens,
        reasoningTokens: modelReasoningTokens,
        cachedTokens: modelCachedTokens,
        cacheCreationTokens: modelCacheCreationTokens,
        cacheReadTokens: modelCacheReadTokens,
        averageLatencyMs: modelLatencyStats.averageMs,
        totalLatencyMs: modelLatencyStats.totalMs,
        latencySampleCount: modelLatencyStats.sampleCount,
      };
      inputTokens += modelInputTokens;
      outputTokens += modelOutputTokens;
      reasoningTokens += modelReasoningTokens;
      cachedTokens += modelCachedTokens;
      cacheCreationTokens += modelCacheCreationTokens;
      cacheReadTokens += modelCacheReadTokens;
      derivedSuccessCount += successCount;
      derivedFailureCount += failureCount;
    });

    const hasApiExplicitCounts =
      typeof apiData.success_count === 'number' || typeof apiData.failure_count === 'number';
    const successCount = hasApiExplicitCounts
      ? Number(apiData.success_count) || 0
      : derivedSuccessCount;
    const failureCount = hasApiExplicitCounts
      ? Number(apiData.failure_count) || 0
      : derivedFailureCount;
    const apiLatencyStats = finalizeLatencyStats(apiLatency);

    result.push({
      endpoint: maskUsageSensitiveValue(endpoint) || endpoint,
      totalRequests: Number(apiData.total_requests) || 0,
      successCount,
      failureCount,
      totalTokens: Number(apiData.total_tokens) || 0,
      totalCost,
      inputTokens,
      outputTokens,
      reasoningTokens,
      cachedTokens,
      cacheCreationTokens,
      cacheReadTokens,
      averageLatencyMs: apiLatencyStats.averageMs,
      totalLatencyMs: apiLatencyStats.totalMs,
      latencySampleCount: apiLatencyStats.sampleCount,
      models,
    });
  });

  return result;
}

/**
 * 获取模型统计数据
 */
export function getModelStats(
  usageData: unknown,
  modelPrices: Record<string, ModelPrice>
): ModelStatsSummary[] {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    const modelMap = new Map<
      string,
      {
        requests: number;
        successCount: number;
        failureCount: number;
        tokens: number;
        cost: number;
        inputTokens: number;
        outputTokens: number;
        reasoningTokens: number;
        cachedTokens: number;
        cacheCreationTokens: number;
        cacheReadTokens: number;
        latencyTotalMs: number;
        latencySampleCount: number;
      }
    >();

    summaryEntries.forEach((entry) => {
      const existing =
        modelMap.get(entry.model) ?? {
          requests: 0,
          successCount: 0,
          failureCount: 0,
          tokens: 0,
          cost: 0,
          inputTokens: 0,
          outputTokens: 0,
          reasoningTokens: 0,
          cachedTokens: 0,
          cacheCreationTokens: 0,
          cacheReadTokens: 0,
          latencyTotalMs: 0,
          latencySampleCount: 0,
        };
      const buckets = usageBucketTokenBuckets(entry.stats);
      const latency = usageBucketLatencyStats(entry.stats);
      existing.requests += tokenCount(entry.stats.total_requests);
      existing.successCount += tokenCount(entry.stats.success_count);
      existing.failureCount += tokenCount(entry.stats.failure_count);
      existing.tokens += buckets.totalTokens;
      existing.inputTokens += buckets.inputTokens;
      existing.outputTokens += buckets.outputTokens;
      existing.reasoningTokens += buckets.reasoningTokens;
      existing.cachedTokens += buckets.cachedInputTokens;
      existing.cacheCreationTokens += buckets.cacheCreationTokens;
      existing.cacheReadTokens += buckets.cacheReadTokens;
      existing.latencyTotalMs += latency.totalMs ?? 0;
      existing.latencySampleCount += latency.sampleCount;
      existing.cost += calculateUsageBucketCost(entry.model, entry.stats, modelPrices);
      modelMap.set(entry.model, existing);
    });

    return Array.from(modelMap.entries())
      .map(([model, stats]) => ({
        model,
        requests: stats.requests,
        successCount: stats.successCount,
        failureCount: stats.failureCount,
        tokens: stats.tokens,
        cost: stats.cost,
        inputTokens: stats.inputTokens,
        outputTokens: stats.outputTokens,
        reasoningTokens: stats.reasoningTokens,
        cachedTokens: stats.cachedTokens,
        cacheCreationTokens: stats.cacheCreationTokens,
        cacheReadTokens: stats.cacheReadTokens,
        averageLatencyMs:
          stats.latencySampleCount > 0 ? stats.latencyTotalMs / stats.latencySampleCount : null,
        totalLatencyMs: stats.latencySampleCount > 0 ? stats.latencyTotalMs : null,
        latencySampleCount: stats.latencySampleCount,
      }))
      .sort((a, b) => b.requests - a.requests);
  }

  const apis = getApisRecord(usageData);
  if (!apis) return [];

  const modelMap = new Map<
    string,
    {
      requests: number;
      successCount: number;
      failureCount: number;
      tokens: number;
      cost: number;
      inputTokens: number;
      outputTokens: number;
      reasoningTokens: number;
      cachedTokens: number;
      cacheCreationTokens: number;
      cacheReadTokens: number;
      latency: LatencyAccumulator;
    }
  >();

  Object.values(apis).forEach((apiData) => {
    if (!isRecord(apiData)) return;
    const modelsRaw = apiData.models;
    const models = isRecord(modelsRaw) ? modelsRaw : null;
    if (!models) return;

    Object.entries(models).forEach(([modelName, modelData]) => {
      if (!isRecord(modelData)) return;
      const existing = modelMap.get(modelName) || {
        requests: 0,
        successCount: 0,
        failureCount: 0,
        tokens: 0,
        cost: 0,
        inputTokens: 0,
        outputTokens: 0,
        reasoningTokens: 0,
        cachedTokens: 0,
        cacheCreationTokens: 0,
        cacheReadTokens: 0,
        latency: createLatencyAccumulator(),
      };
      existing.requests += Number(modelData.total_requests) || 0;
      existing.tokens += Number(modelData.total_tokens) || 0;

      const details = Array.isArray(modelData.details) ? modelData.details : [];

      const price = resolveModelPrice(modelName, modelPrices);

      const hasExplicitCounts =
        typeof modelData.success_count === 'number' || typeof modelData.failure_count === 'number';
      if (hasExplicitCounts) {
        existing.successCount += Number(modelData.success_count) || 0;
        existing.failureCount += Number(modelData.failure_count) || 0;
      }

      if (details.length > 0) {
        details.forEach((detail) => {
          const detailRecord = isRecord(detail) ? detail : null;
          const latencyMs = extractLatencyMs(detailRecord);
          if (!hasExplicitCounts) {
            if (detailRecord?.failed === true) {
              existing.failureCount += 1;
            } else {
              existing.successCount += 1;
            }
          }

          addLatencySample(existing.latency, latencyMs);

          if (price && detailRecord) {
            existing.cost += calculateCost(
              { ...(detailRecord as unknown as UsageDetail), __modelName: modelName },
              modelPrices
            );
          }
          if (detailRecord) {
            const buckets = getUsageTokenBuckets(detailRecord as unknown as UsageDetail);
            existing.inputTokens += buckets.inputTokens;
            existing.outputTokens += buckets.outputTokens;
            existing.reasoningTokens += buckets.reasoningTokens;
            existing.cachedTokens += buckets.cachedInputTokens;
            existing.cacheCreationTokens += buckets.cacheCreationTokens;
            existing.cacheReadTokens += buckets.cacheReadTokens;
          }
        });
      }
      modelMap.set(modelName, existing);
    });
  });

  return Array.from(modelMap.entries())
    .map(([model, stats]) => {
      const latencyStats = finalizeLatencyStats(stats.latency);
      return {
        model,
        requests: stats.requests,
        successCount: stats.successCount,
        failureCount: stats.failureCount,
        tokens: stats.tokens,
        cost: stats.cost,
        inputTokens: stats.inputTokens,
        outputTokens: stats.outputTokens,
        reasoningTokens: stats.reasoningTokens,
        cachedTokens: stats.cachedTokens,
        cacheCreationTokens: stats.cacheCreationTokens,
        cacheReadTokens: stats.cacheReadTokens,
        averageLatencyMs: latencyStats.averageMs,
        totalLatencyMs: latencyStats.totalMs,
        latencySampleCount: latencyStats.sampleCount,
      };
    })
    .sort((a, b) => b.requests - a.requests);
}

/**
 * 格式化小时标签
 */
export function formatHourLabel(date: Date): string {
  if (!(date instanceof Date)) {
    return '';
  }
  const month = (date.getMonth() + 1).toString().padStart(2, '0');
  const day = date.getDate().toString().padStart(2, '0');
  const hour = date.getHours().toString().padStart(2, '0');
  return `${month}-${day} ${hour}:00`;
}

/**
 * 格式化日期标签
 */
export function formatDayLabel(date: Date): string {
  if (!(date instanceof Date)) {
    return '';
  }
  const year = date.getFullYear();
  const month = (date.getMonth() + 1).toString().padStart(2, '0');
  const day = date.getDate().toString().padStart(2, '0');
  return `${year}-${month}-${day}`;
}

/**
 * 构建小时级别的数据序列
 */
export function buildHourlySeriesByModel(
  usageData: unknown,
  metric: 'requests' | 'tokens' = 'requests',
  hourWindow: number = 24
): {
  labels: string[];
  dataByModel: Map<string, number[]>;
  hasData: boolean;
} {
  const hourMs = 60 * 60 * 1000;
  const resolvedHourWindow =
    Number.isFinite(hourWindow) && hourWindow > 0
      ? Math.min(Math.max(Math.floor(hourWindow), 1), 24 * 31)
      : 24;
  const now = new Date();
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);

  const earliestBucket = new Date(currentHour);
  earliestBucket.setHours(earliestBucket.getHours() - (resolvedHourWindow - 1));
  const earliestTime = earliestBucket.getTime();

  const labels: string[] = [];
  for (let i = 0; i < resolvedHourWindow; i++) {
    const bucketStart = earliestTime + i * hourMs;
    labels.push(formatHourLabel(new Date(bucketStart)));
  }

  const dataByModel = new Map<string, number[]>();
  let hasData = false;
  const lastBucketTime = earliestTime + (labels.length - 1) * hourMs;
  const summaryEntries = collectUsage20mSummaryEntries(usageData, {
    windowStartMs: earliestTime,
    windowEndMs: lastBucketTime + hourMs - 1,
  });

  if (summaryEntries.length > 0) {
    summaryEntries.forEach((entry) => {
      const normalized = new Date(entry.bucketStartMs);
      normalized.setMinutes(0, 0, 0);
      const bucketStart = normalized.getTime();
      if (bucketStart < earliestTime || bucketStart > lastBucketTime) return;
      const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
      if (bucketIndex < 0 || bucketIndex >= labels.length) return;

      const modelName = entry.model || 'Unknown';
      if (!dataByModel.has(modelName)) {
        dataByModel.set(modelName, new Array(labels.length).fill(0));
      }
      const bucketValues = dataByModel.get(modelName)!;
      bucketValues[bucketIndex] +=
        metric === 'tokens'
          ? tokenCount(entry.stats.total_tokens)
          : tokenCount(entry.stats.total_requests);
      hasData = true;
    });
    return { labels, dataByModel, hasData };
  }

  const details = collectUsageDetails(usageData);

  if (!details.length) {
    return { labels, dataByModel, hasData };
  }

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) {
      return;
    }

    const normalized = new Date(timestamp);
    normalized.setMinutes(0, 0, 0);
    const bucketStart = normalized.getTime();
    if (bucketStart < earliestTime || bucketStart > lastBucketTime) {
      return;
    }

    const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
    if (bucketIndex < 0 || bucketIndex >= labels.length) {
      return;
    }

    const modelName = detail.__modelName || 'Unknown';
    if (!dataByModel.has(modelName)) {
      dataByModel.set(modelName, new Array(labels.length).fill(0));
    }

    const bucketValues = dataByModel.get(modelName)!;
    if (metric === 'tokens') {
      bucketValues[bucketIndex] += extractTotalTokens(detail);
    } else {
      bucketValues[bucketIndex] += 1;
    }
    hasData = true;
  });

  return { labels, dataByModel, hasData };
}

/**
 * 构建日级别的数据序列
 */
export function buildDailySeriesByModel(
  usageData: unknown,
  metric: 'requests' | 'tokens' = 'requests'
): {
  labels: string[];
  dataByModel: Map<string, number[]>;
  hasData: boolean;
} {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    const valuesByModel = new Map<string, Map<string, number>>();
    const labelsSet = new Set<string>();
    summaryEntries.forEach((entry) => {
      const dayLabel = formatDayLabel(new Date(entry.bucketStartMs));
      if (!dayLabel) return;
      const modelName = entry.model || 'Unknown';
      if (!valuesByModel.has(modelName)) {
        valuesByModel.set(modelName, new Map());
      }
      const modelDayMap = valuesByModel.get(modelName)!;
      const increment =
        metric === 'tokens'
          ? tokenCount(entry.stats.total_tokens)
          : tokenCount(entry.stats.total_requests);
      modelDayMap.set(dayLabel, (modelDayMap.get(dayLabel) || 0) + increment);
      labelsSet.add(dayLabel);
    });

    const labels = Array.from(labelsSet).sort();
    const dataByModel = new Map<string, number[]>();
    valuesByModel.forEach((dayMap, modelName) => {
      dataByModel.set(
        modelName,
        labels.map((label) => dayMap.get(label) || 0)
      );
    });
    return { labels, dataByModel, hasData: labels.length > 0 };
  }

  const details = collectUsageDetails(usageData);
  const valuesByModel = new Map<string, Map<string, number>>();
  const labelsSet = new Set<string>();
  let hasData = false;

  if (!details.length) {
    return { labels: [], dataByModel: new Map(), hasData };
  }

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) {
      return;
    }
    const dayLabel = formatDayLabel(new Date(timestamp));
    if (!dayLabel) {
      return;
    }

    const modelName = detail.__modelName || 'Unknown';
    if (!valuesByModel.has(modelName)) {
      valuesByModel.set(modelName, new Map());
    }
    const modelDayMap = valuesByModel.get(modelName)!;
    const increment = metric === 'tokens' ? extractTotalTokens(detail) : 1;
    modelDayMap.set(dayLabel, (modelDayMap.get(dayLabel) || 0) + increment);
    labelsSet.add(dayLabel);
    hasData = true;
  });

  const labels = Array.from(labelsSet).sort();
  const dataByModel = new Map<string, number[]>();
  valuesByModel.forEach((dayMap, modelName) => {
    const series = labels.map((label) => dayMap.get(label) || 0);
    dataByModel.set(modelName, series);
  });

  return { labels, dataByModel, hasData };
}

export interface ChartDataset {
  label: string;
  data: number[];
  borderColor: string;
  backgroundColor:
    | string
    | CanvasGradient
    | ((context: ScriptableContext<'line'>) => string | CanvasGradient);
  pointBackgroundColor?: string;
  pointBorderColor?: string;
  fill: boolean;
  tension: number;
}

export interface ChartData {
  labels: string[];
  datasets: ChartDataset[];
}

const CHART_COLORS = [
  { borderColor: '#8b8680', backgroundColor: 'rgba(139, 134, 128, 0.15)' },
  { borderColor: '#22c55e', backgroundColor: 'rgba(34, 197, 94, 0.15)' },
  { borderColor: '#f59e0b', backgroundColor: 'rgba(245, 158, 11, 0.15)' },
  { borderColor: '#c65746', backgroundColor: 'rgba(198, 87, 70, 0.15)' },
  { borderColor: '#8b5cf6', backgroundColor: 'rgba(139, 92, 246, 0.15)' },
  { borderColor: '#06b6d4', backgroundColor: 'rgba(6, 182, 212, 0.15)' },
  { borderColor: '#ec4899', backgroundColor: 'rgba(236, 72, 153, 0.15)' },
  { borderColor: '#84cc16', backgroundColor: 'rgba(132, 204, 22, 0.15)' },
  { borderColor: '#f97316', backgroundColor: 'rgba(249, 115, 22, 0.15)' },
];

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

const hexToRgb = (hex: string): { r: number; g: number; b: number } | null => {
  const normalized = hex.trim().replace('#', '');
  if (normalized.length !== 6) {
    return null;
  }
  const r = Number.parseInt(normalized.slice(0, 2), 16);
  const g = Number.parseInt(normalized.slice(2, 4), 16);
  const b = Number.parseInt(normalized.slice(4, 6), 16);
  if (![r, g, b].every((channel) => Number.isFinite(channel))) {
    return null;
  }
  return { r, g, b };
};

const withAlpha = (hex: string, alpha: number) => {
  const rgb = hexToRgb(hex);
  if (!rgb) {
    return hex;
  }
  const clamped = clamp(alpha, 0, 1);
  return `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, ${clamped})`;
};

const buildAreaGradient = (
  context: ScriptableContext<'line'>,
  baseHex: string,
  fallback: string
) => {
  const chart = context.chart;
  const ctx = chart.ctx;
  const area = chart.chartArea;

  if (!area) {
    return fallback;
  }

  const gradient = ctx.createLinearGradient(0, area.top, 0, area.bottom);
  gradient.addColorStop(0, withAlpha(baseHex, 0.28));
  gradient.addColorStop(0.6, withAlpha(baseHex, 0.12));
  gradient.addColorStop(1, withAlpha(baseHex, 0.02));
  return gradient;
};

/**
 * 构建图表数据
 */
export function buildChartData(
  usageData: unknown,
  period: 'hour' | 'day' = 'day',
  metric: 'requests' | 'tokens' = 'requests',
  selectedModels: string[] = [],
  options: { hourWindowHours?: number } = {}
): ChartData {
  const baseSeries =
    period === 'hour'
      ? buildHourlySeriesByModel(usageData, metric, options.hourWindowHours)
      : buildDailySeriesByModel(usageData, metric);

  const { labels, dataByModel } = baseSeries;

  // Build "All" series as sum of all models
  const getAllSeries = (): number[] => {
    const summed = new Array(labels.length).fill(0);
    dataByModel.forEach((values) => {
      values.forEach((value, idx) => {
        summed[idx] = (summed[idx] || 0) + value;
      });
    });
    return summed;
  };

  // Determine which models to show
  const modelsToShow = selectedModels.length > 0 ? selectedModels : ['all'];

  const datasets: ChartDataset[] = modelsToShow.map((model, index) => {
    const isAll = model === 'all';
    const data = isAll
      ? getAllSeries()
      : dataByModel.get(model) || new Array(labels.length).fill(0);
    const colorIndex = index % CHART_COLORS.length;
    const style = CHART_COLORS[colorIndex];
    const shouldFill = modelsToShow.length === 1 || (isAll && modelsToShow.length > 1);

    return {
      label: isAll ? 'All Models' : model,
      data,
      borderColor: style.borderColor,
      backgroundColor: shouldFill
        ? (ctx) => buildAreaGradient(ctx, style.borderColor, style.backgroundColor)
        : style.backgroundColor,
      pointBackgroundColor: style.borderColor,
      pointBorderColor: style.borderColor,
      fill: shouldFill,
      tension: 0.35,
    };
  });

  return { labels, datasets };
}

/**
 * 依据 usage 数据计算密钥使用统计
 */
/**
 * 状态栏单个格子的状态
 */
export type StatusBlockState = 'success' | 'failure' | 'mixed' | 'idle';

/**
 * 状态栏单个格子的详细信息
 */
export interface StatusBlockDetail {
  success: number;
  failure: number;
  /** 该格子的成功率 (0–1)，无请求时为 -1 */
  rate: number;
  /** 格子起始时间戳 (ms) */
  startTime: number;
  /** 格子结束时间戳 (ms) */
  endTime: number;
}

/**
 * 状态栏数据
 */
export interface StatusBarData {
  blocks: StatusBlockState[];
  blockDetails: StatusBlockDetail[];
  successRate: number;
  totalSuccess: number;
  totalFailure: number;
}

/**
 * Calculates status bar data for the last 400 minutes as twenty 20-minute blocks.
 * Each block represents one equal time slice for success/failure trends.
 */
export function calculateStatusBarData(
  usageDetails: UsageDetail[],
  sourceFilter?: string,
  authIndexFilter?: string | number
): StatusBarData {
  const BLOCK_COUNT = 20;
  const BLOCK_DURATION_MS = USAGE_BUCKET_DURATION_MS;
  const WINDOW_MS = BLOCK_COUNT * BLOCK_DURATION_MS;

  const now = Date.now();
  const windowStart = now - WINDOW_MS;

  const blockStats: Array<{ success: number; failure: number }> = Array.from(
    { length: BLOCK_COUNT },
    () => ({ success: 0, failure: 0 })
  );

  let totalSuccess = 0;
  let totalFailure = 0;

  usageDetails.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (
      !Number.isFinite(timestamp) ||
      timestamp <= 0 ||
      timestamp < windowStart ||
      timestamp > now
    ) {
      return;
    }

    if (sourceFilter !== undefined && detail.source !== sourceFilter) {
      return;
    }
    if (authIndexFilter !== undefined && detail.auth_index !== authIndexFilter) {
      return;
    }

    const ageMs = now - timestamp;
    const blockIndex = BLOCK_COUNT - 1 - Math.floor(ageMs / BLOCK_DURATION_MS);

    if (blockIndex >= 0 && blockIndex < BLOCK_COUNT) {
      if (detail.failed) {
        blockStats[blockIndex].failure += 1;
        totalFailure += 1;
      } else {
        blockStats[blockIndex].success += 1;
        totalSuccess += 1;
      }
    }
  });

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBlockDetail[] = [];

  blockStats.forEach((stat, idx) => {
    const total = stat.success + stat.failure;
    if (total === 0) {
      blocks.push('idle');
    } else if (stat.failure === 0) {
      blocks.push('success');
    } else if (stat.success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }

    const blockStartTime = windowStart + idx * BLOCK_DURATION_MS;
    blockDetails.push({
      success: stat.success,
      failure: stat.failure,
      rate: total > 0 ? stat.success / total : -1,
      startTime: blockStartTime,
      endTime: blockStartTime + BLOCK_DURATION_MS,
    });
  });

  const total = totalSuccess + totalFailure;
  const successRate = total > 0 ? (totalSuccess / total) * 100 : 100;

  return {
    blocks,
    blockDetails,
    successRate,
    totalSuccess,
    totalFailure,
  };
}

export function calculateStatusBarDataFromUsage20m(
  usageData: unknown,
  identityKeys: Iterable<string>,
  options: { provider?: string; nowMs?: number } = {}
): StatusBarData | null {
  const usageRecord = isRecord(usageData) ? usageData : null;
  const root = usageRecord && isRecord(usageRecord.usage_20m) ? usageRecord.usage_20m : null;
  if (!root) {
    return null;
  }

  const identities = new Set(
    Array.from(identityKeys)
      .map((item) => String(item || '').trim())
      .filter(Boolean)
  );
  if (!identities.size) {
    return null;
  }

  const BLOCK_COUNT = 20;
  const BLOCK_DURATION_MS = USAGE_BUCKET_DURATION_MS;
  const now = Number.isFinite(options.nowMs) ? Number(options.nowMs) : Date.now();
  const currentBucketStart = Math.floor(now / BLOCK_DURATION_MS) * BLOCK_DURATION_MS;
  const windowStart = currentBucketStart - (BLOCK_COUNT - 1) * BLOCK_DURATION_MS;
  const windowEnd = currentBucketStart + BLOCK_DURATION_MS;

  const blockStats: Array<{ success: number; failure: number }> = Array.from(
    { length: BLOCK_COUNT },
    () => ({ success: 0, failure: 0 })
  );
  let totalSuccess = 0;
  let totalFailure = 0;

  const providerEntries =
    options.provider && isRecord(root[options.provider])
      ? [[options.provider, root[options.provider]]]
      : Object.entries(root);

  providerEntries.forEach(([, providerBuckets]) => {
    if (!isRecord(providerBuckets)) return;
    Object.entries(providerBuckets).forEach(([bucketLabel, identityBuckets]) => {
      if (!isRecord(identityBuckets)) return;
      const bucketStart = parseTimestampMs(bucketLabel);
      if (
        !Number.isFinite(bucketStart) ||
        bucketStart < windowStart ||
        bucketStart >= windowEnd
      ) {
        return;
      }
      const blockIndex = Math.floor((bucketStart - windowStart) / BLOCK_DURATION_MS);
      if (blockIndex < 0 || blockIndex >= BLOCK_COUNT) {
        return;
      }

      identities.forEach((identity) => {
        const models = identityBuckets[identity];
        if (!isRecord(models)) return;
        Object.values(models).forEach((rawStats) => {
          const stats = isRecord(rawStats) ? rawStats : null;
          if (!stats) return;
          const success = tokenCount(stats.success_count);
          const failure = tokenCount(stats.failure_count);
          blockStats[blockIndex].success += success;
          blockStats[blockIndex].failure += failure;
          totalSuccess += success;
          totalFailure += failure;
        });
      });
    });
  });

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBlockDetail[] = [];
  blockStats.forEach((stat, idx) => {
    const total = stat.success + stat.failure;
    if (total === 0) {
      blocks.push('idle');
    } else if (stat.failure === 0) {
      blocks.push('success');
    } else if (stat.success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }
    const blockStartTime = windowStart + idx * BLOCK_DURATION_MS;
    blockDetails.push({
      success: stat.success,
      failure: stat.failure,
      rate: total > 0 ? stat.success / total : -1,
      startTime: blockStartTime,
      endTime: blockStartTime + BLOCK_DURATION_MS,
    });
  });

  const total = totalSuccess + totalFailure;
  return {
    blocks,
    blockDetails,
    successRate: total > 0 ? (totalSuccess / total) * 100 : 100,
    totalSuccess,
    totalFailure,
  };
}

export interface ServiceHealthData {
  blocks: StatusBlockState[];
  blockDetails: StatusBlockDetail[];
  successRate: number;
  totalSuccess: number;
  totalFailure: number;
  rows: number;
  cols: number;
}

export function calculateServiceHealthData(
  usageDetails: UsageDetail[],
  nowMs: number = Date.now()
): ServiceHealthData {
  const ROWS = 7;
  const COLS = 72;
  const BLOCK_COUNT = ROWS * COLS;
  const BLOCK_DURATION_MS = USAGE_BUCKET_DURATION_MS;

  const now = Number.isFinite(nowMs) && nowMs > 0 ? nowMs : Date.now();
  const currentBucketStart = Math.floor(now / BLOCK_DURATION_MS) * BLOCK_DURATION_MS;
  const windowStart = currentBucketStart - (BLOCK_COUNT - 1) * BLOCK_DURATION_MS;

  const blockStats: Array<{ success: number; failure: number }> = Array.from(
    { length: BLOCK_COUNT },
    () => ({ success: 0, failure: 0 })
  );

  let totalSuccess = 0;
  let totalFailure = 0;

  usageDetails.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (
      !Number.isFinite(timestamp) ||
      timestamp <= 0 ||
      timestamp < windowStart ||
      timestamp > now
    ) {
      return;
    }

    const blockIndex = Math.floor((timestamp - windowStart) / BLOCK_DURATION_MS);

    if (blockIndex >= 0 && blockIndex < BLOCK_COUNT) {
      if (detail.failed) {
        blockStats[blockIndex].failure += 1;
        totalFailure += 1;
      } else {
        blockStats[blockIndex].success += 1;
        totalSuccess += 1;
      }
    }
  });

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBlockDetail[] = [];

  blockStats.forEach((stat, idx) => {
    const total = stat.success + stat.failure;
    if (total === 0) {
      blocks.push('idle');
    } else if (stat.failure === 0) {
      blocks.push('success');
    } else if (stat.success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }

    const blockStartTime = windowStart + idx * BLOCK_DURATION_MS;
    blockDetails.push({
      success: stat.success,
      failure: stat.failure,
      rate: total > 0 ? stat.success / total : -1,
      startTime: blockStartTime,
      endTime: blockStartTime + BLOCK_DURATION_MS,
    });
  });

  const total = totalSuccess + totalFailure;
  const successRate = total > 0 ? (totalSuccess / total) * 100 : 100;

  return {
    blocks,
    blockDetails,
    successRate,
    totalSuccess,
    totalFailure,
    rows: ROWS,
    cols: COLS,
  };
}

export function calculateServiceHealthDataFromUsage(
  usageData: unknown,
  nowMs: number = Date.now()
): ServiceHealthData {
  const entries = collectUsage20mSummaryEntries(usageData);
  if (!entries.length) {
    return calculateServiceHealthData(collectUsageDetails(usageData), nowMs);
  }

  const ROWS = 7;
  const COLS = 72;
  const BLOCK_COUNT = ROWS * COLS;
  const BLOCK_DURATION_MS = USAGE_BUCKET_DURATION_MS;
  const now = Number.isFinite(nowMs) && nowMs > 0 ? nowMs : Date.now();
  const currentBucketStart = Math.floor(now / BLOCK_DURATION_MS) * BLOCK_DURATION_MS;
  const windowStart = currentBucketStart - (BLOCK_COUNT - 1) * BLOCK_DURATION_MS;
  const windowEnd = currentBucketStart + BLOCK_DURATION_MS;

  const blockStats: Array<{ success: number; failure: number }> = Array.from(
    { length: BLOCK_COUNT },
    () => ({ success: 0, failure: 0 })
  );
  let totalSuccess = 0;
  let totalFailure = 0;

  entries.forEach((entry) => {
    if (entry.bucketStartMs < windowStart || entry.bucketStartMs >= windowEnd) return;
    const blockIndex = Math.floor((entry.bucketStartMs - windowStart) / BLOCK_DURATION_MS);
    if (blockIndex < 0 || blockIndex >= BLOCK_COUNT) return;
    const success = tokenCount(entry.stats.success_count);
    const failure = tokenCount(entry.stats.failure_count);
    blockStats[blockIndex].success += success;
    blockStats[blockIndex].failure += failure;
    totalSuccess += success;
    totalFailure += failure;
  });

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBlockDetail[] = [];
  blockStats.forEach((stat, idx) => {
    const total = stat.success + stat.failure;
    if (total === 0) {
      blocks.push('idle');
    } else if (stat.failure === 0) {
      blocks.push('success');
    } else if (stat.success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }
    const blockStartTime = windowStart + idx * BLOCK_DURATION_MS;
    blockDetails.push({
      success: stat.success,
      failure: stat.failure,
      rate: total > 0 ? stat.success / total : -1,
      startTime: blockStartTime,
      endTime: blockStartTime + BLOCK_DURATION_MS,
    });
  });

  const total = totalSuccess + totalFailure;
  return {
    blocks,
    blockDetails,
    successRate: total > 0 ? (totalSuccess / total) * 100 : 100,
    totalSuccess,
    totalFailure,
    rows: ROWS,
    cols: COLS,
  };
}

export function computeKeyStats(
  usageData: unknown,
  masker: (val: string) => string = maskApiKey
): KeyStats {
  const usageRecord = isRecord(usageData) ? usageData : null;
  const sourceStatsRaw = usageRecord && isRecord(usageRecord.source_stats) ? usageRecord.source_stats : null;
  const authIndexStatsRaw =
    usageRecord && isRecord(usageRecord.auth_index_stats) ? usageRecord.auth_index_stats : null;
  if (sourceStatsRaw || authIndexStatsRaw) {
    const bySource: Record<string, KeyStatBucket> = {};
    const byAuthIndex: Record<string, KeyStatBucket> = {};

    if (sourceStatsRaw) {
      Object.entries(sourceStatsRaw).forEach(([rawKey, rawValue]) => {
        const record = isRecord(rawValue) ? rawValue : null;
        const key = normalizeUsageSourceId(rawKey, masker);
        if (!key) return;
        bySource[key] = {
          success: Number(record?.success) || 0,
          failure: Number(record?.failure) || 0,
        };
      });
    }

    if (authIndexStatsRaw) {
      Object.entries(authIndexStatsRaw).forEach(([rawKey, rawValue]) => {
        const record = isRecord(rawValue) ? rawValue : null;
        const key = normalizeAuthIndex(rawKey);
        if (!key) return;
        byAuthIndex[key] = {
          success: Number(record?.success) || 0,
          failure: Number(record?.failure) || 0,
        };
      });
    }

    return { bySource, byAuthIndex };
  }

  const apis = getApisRecord(usageData);
  if (!apis) {
    return { bySource: {}, byAuthIndex: {} };
  }

  const sourceStats: Record<string, KeyStatBucket> = {};
  const authIndexStats: Record<string, KeyStatBucket> = {};

  const ensureBucket = (bucket: Record<string, KeyStatBucket>, key: string) => {
    if (!bucket[key]) {
      bucket[key] = { success: 0, failure: 0 };
    }
    return bucket[key];
  };

  Object.values(apis).forEach((apiEntry) => {
    if (!isRecord(apiEntry)) return;
    const modelsRaw = apiEntry.models;
    const models = isRecord(modelsRaw) ? modelsRaw : null;
    if (!models) return;

    Object.values(models).forEach((modelEntry) => {
      if (!isRecord(modelEntry)) return;
      const details = Array.isArray(modelEntry.details) ? modelEntry.details : [];

      details.forEach((detail) => {
        const detailRecord = isRecord(detail) ? detail : null;
        const source = normalizeUsageSourceId(detailRecord?.source, masker);
        const authIndexKey = normalizeAuthIndex(detailRecord?.auth_index);
        const isFailed = detailRecord?.failed === true;

        if (source) {
          const bucket = ensureBucket(sourceStats, source);
          if (isFailed) {
            bucket.failure += 1;
          } else {
            bucket.success += 1;
          }
        }

        if (authIndexKey) {
          const bucket = ensureBucket(authIndexStats, authIndexKey);
          if (isFailed) {
            bucket.failure += 1;
          } else {
            bucket.success += 1;
          }
        }
      });
    });
  });

  return {
    bySource: sourceStats,
    byAuthIndex: authIndexStats,
  };
}

export function computeKeyStatsFromDetails(usageDetails: UsageDetail[]): KeyStats {
  const bySource: Record<string, KeyStatBucket> = {};
  const byAuthIndex: Record<string, KeyStatBucket> = {};

  const ensureBucket = (bucket: Record<string, KeyStatBucket>, key: string) => {
    if (!bucket[key]) {
      bucket[key] = { success: 0, failure: 0 };
    }
    return bucket[key];
  };

  usageDetails.forEach((detail) => {
    const source = detail.source;
    const authIndexKey = normalizeAuthIndex(detail.auth_index);
    const isFailed = detail.failed === true;

    if (source) {
      const bucket = ensureBucket(bySource, source);
      if (isFailed) {
        bucket.failure += 1;
      } else {
        bucket.success += 1;
      }
    }

    if (authIndexKey) {
      const bucket = ensureBucket(byAuthIndex, authIndexKey);
      if (isFailed) {
        bucket.failure += 1;
      } else {
        bucket.success += 1;
      }
    }
  });

  return { bySource, byAuthIndex };
}

export type TokenCategory =
  | 'input'
  | 'output'
  | 'cached'
  | 'cacheCreation'
  | 'cacheRead'
  | 'reasoning';

export interface TokenBreakdownSeries {
  labels: string[];
  dataByCategory: Record<TokenCategory, number[]>;
  hasData: boolean;
}

/**
 * 按 token 类别构建小时级别的堆叠序列
 */
export function buildHourlyTokenBreakdown(
  usageData: unknown,
  hourWindow: number = 24
): TokenBreakdownSeries {
  const hourMs = 60 * 60 * 1000;
  const resolvedHourWindow =
    Number.isFinite(hourWindow) && hourWindow > 0
      ? Math.min(Math.max(Math.floor(hourWindow), 1), 24 * 31)
      : 24;
  const now = new Date();
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);

  const earliestBucket = new Date(currentHour);
  earliestBucket.setHours(earliestBucket.getHours() - (resolvedHourWindow - 1));
  const earliestTime = earliestBucket.getTime();

  const labels: string[] = [];
  for (let i = 0; i < resolvedHourWindow; i++) {
    labels.push(formatHourLabel(new Date(earliestTime + i * hourMs)));
  }

  const dataByCategory: Record<TokenCategory, number[]> = {
    input: new Array(labels.length).fill(0),
    output: new Array(labels.length).fill(0),
    cached: new Array(labels.length).fill(0),
    cacheCreation: new Array(labels.length).fill(0),
    cacheRead: new Array(labels.length).fill(0),
    reasoning: new Array(labels.length).fill(0),
  };

  let hasData = false;
  const lastBucketTime = earliestTime + (labels.length - 1) * hourMs;
  const summaryEntries = collectUsage20mSummaryEntries(usageData, {
    windowStartMs: earliestTime,
    windowEndMs: lastBucketTime + hourMs - 1,
  });
  if (summaryEntries.length > 0) {
    summaryEntries.forEach((entry) => {
      const normalized = new Date(entry.bucketStartMs);
      normalized.setMinutes(0, 0, 0);
      const bucketStart = normalized.getTime();
      if (bucketStart < earliestTime || bucketStart > lastBucketTime) return;
      const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
      if (bucketIndex < 0 || bucketIndex >= labels.length) return;
      const buckets = usageBucketTokenBuckets(entry.stats);
      dataByCategory.input[bucketIndex] += buckets.inputTokens;
      dataByCategory.output[bucketIndex] += buckets.outputTokens;
      dataByCategory.cached[bucketIndex] += buckets.cachedInputTokens;
      dataByCategory.cacheCreation[bucketIndex] += buckets.cacheCreationTokens;
      dataByCategory.cacheRead[bucketIndex] += buckets.cacheReadTokens;
      dataByCategory.reasoning[bucketIndex] += buckets.reasoningTokens;
      hasData = true;
    });
    return { labels, dataByCategory, hasData };
  }

  const details = collectUsageDetails(usageData);

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return;
    const normalized = new Date(timestamp);
    normalized.setMinutes(0, 0, 0);
    const bucketStart = normalized.getTime();
    if (bucketStart < earliestTime || bucketStart > lastBucketTime) return;
    const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
    if (bucketIndex < 0 || bucketIndex >= labels.length) return;

    const buckets = getUsageTokenBuckets(detail);

    dataByCategory.input[bucketIndex] += buckets.inputTokens;
    dataByCategory.output[bucketIndex] += buckets.outputTokens;
    dataByCategory.cached[bucketIndex] += buckets.cachedInputTokens;
    dataByCategory.cacheCreation[bucketIndex] += buckets.cacheCreationTokens;
    dataByCategory.cacheRead[bucketIndex] += buckets.cacheReadTokens;
    dataByCategory.reasoning[bucketIndex] += buckets.reasoningTokens;
    hasData = true;
  });

  return { labels, dataByCategory, hasData };
}

/**
 * 按 token 类别构建日级别的堆叠序列
 */
export function buildDailyTokenBreakdown(usageData: unknown): TokenBreakdownSeries {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    const dayMap: Record<string, Record<TokenCategory, number>> = {};
    summaryEntries.forEach((entry) => {
      const dayLabel = formatDayLabel(new Date(entry.bucketStartMs));
      if (!dayLabel) return;
      if (!dayMap[dayLabel]) {
        dayMap[dayLabel] = {
          input: 0,
          output: 0,
          cached: 0,
          cacheCreation: 0,
          cacheRead: 0,
          reasoning: 0,
        };
      }
      const buckets = usageBucketTokenBuckets(entry.stats);
      dayMap[dayLabel].input += buckets.inputTokens;
      dayMap[dayLabel].output += buckets.outputTokens;
      dayMap[dayLabel].cached += buckets.cachedInputTokens;
      dayMap[dayLabel].cacheCreation += buckets.cacheCreationTokens;
      dayMap[dayLabel].cacheRead += buckets.cacheReadTokens;
      dayMap[dayLabel].reasoning += buckets.reasoningTokens;
    });

    const labels = Object.keys(dayMap).sort();
    return {
      labels,
      dataByCategory: {
        input: labels.map((l) => dayMap[l].input),
        output: labels.map((l) => dayMap[l].output),
        cached: labels.map((l) => dayMap[l].cached),
        cacheCreation: labels.map((l) => dayMap[l].cacheCreation),
        cacheRead: labels.map((l) => dayMap[l].cacheRead),
        reasoning: labels.map((l) => dayMap[l].reasoning),
      },
      hasData: labels.length > 0,
    };
  }

  const details = collectUsageDetails(usageData);
  const dayMap: Record<string, Record<TokenCategory, number>> = {};
  let hasData = false;

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return;
    const dayLabel = formatDayLabel(new Date(timestamp));
    if (!dayLabel) return;

    if (!dayMap[dayLabel]) {
      dayMap[dayLabel] = {
        input: 0,
        output: 0,
        cached: 0,
        cacheCreation: 0,
        cacheRead: 0,
        reasoning: 0,
      };
    }

    const buckets = getUsageTokenBuckets(detail);

    dayMap[dayLabel].input += buckets.inputTokens;
    dayMap[dayLabel].output += buckets.outputTokens;
    dayMap[dayLabel].cached += buckets.cachedInputTokens;
    dayMap[dayLabel].cacheCreation += buckets.cacheCreationTokens;
    dayMap[dayLabel].cacheRead += buckets.cacheReadTokens;
    dayMap[dayLabel].reasoning += buckets.reasoningTokens;
    hasData = true;
  });

  const labels = Object.keys(dayMap).sort();
  const dataByCategory: Record<TokenCategory, number[]> = {
    input: labels.map((l) => dayMap[l].input),
    output: labels.map((l) => dayMap[l].output),
    cached: labels.map((l) => dayMap[l].cached),
    cacheCreation: labels.map((l) => dayMap[l].cacheCreation),
    cacheRead: labels.map((l) => dayMap[l].cacheRead),
    reasoning: labels.map((l) => dayMap[l].reasoning),
  };

  return { labels, dataByCategory, hasData };
}

export interface CostSeries {
  labels: string[];
  data: number[];
  hasData: boolean;
}

/**
 * 按小时构建费用时间序列
 */
export function buildHourlyCostSeries(
  usageData: unknown,
  modelPrices: Record<string, ModelPrice>,
  hourWindow: number = 24
): CostSeries {
  const hourMs = 60 * 60 * 1000;
  const resolvedHourWindow =
    Number.isFinite(hourWindow) && hourWindow > 0
      ? Math.min(Math.max(Math.floor(hourWindow), 1), 24 * 31)
      : 24;
  const now = new Date();
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);

  const earliestBucket = new Date(currentHour);
  earliestBucket.setHours(earliestBucket.getHours() - (resolvedHourWindow - 1));
  const earliestTime = earliestBucket.getTime();

  const labels: string[] = [];
  for (let i = 0; i < resolvedHourWindow; i++) {
    labels.push(formatHourLabel(new Date(earliestTime + i * hourMs)));
  }

  const data = new Array(labels.length).fill(0);
  let hasData = false;
  const lastBucketTime = earliestTime + (labels.length - 1) * hourMs;
  const summaryEntries = collectUsage20mSummaryEntries(usageData, {
    windowStartMs: earliestTime,
    windowEndMs: lastBucketTime + hourMs - 1,
  });
  if (summaryEntries.length > 0) {
    summaryEntries.forEach((entry) => {
      const normalized = new Date(entry.bucketStartMs);
      normalized.setMinutes(0, 0, 0);
      const bucketStart = normalized.getTime();
      if (bucketStart < earliestTime || bucketStart > lastBucketTime) return;
      const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
      if (bucketIndex < 0 || bucketIndex >= labels.length) return;
      const cost = calculateUsageBucketCost(entry.model, entry.stats, modelPrices);
      if (cost > 0) {
        data[bucketIndex] += cost;
        hasData = true;
      }
    });
    return { labels, data, hasData };
  }

  const details = collectUsageDetails(usageData);

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return;
    const normalized = new Date(timestamp);
    normalized.setMinutes(0, 0, 0);
    const bucketStart = normalized.getTime();
    if (bucketStart < earliestTime || bucketStart > lastBucketTime) return;
    const bucketIndex = Math.floor((bucketStart - earliestTime) / hourMs);
    if (bucketIndex < 0 || bucketIndex >= labels.length) return;

    const cost = calculateCost(detail, modelPrices);
    if (cost > 0) {
      data[bucketIndex] += cost;
      hasData = true;
    }
  });

  return { labels, data, hasData };
}

/**
 * 按天构建费用时间序列
 */
export function buildDailyCostSeries(
  usageData: unknown,
  modelPrices: Record<string, ModelPrice>
): CostSeries {
  const summaryEntries = collectUsage20mSummaryEntries(usageData);
  if (summaryEntries.length > 0) {
    const dayMap: Record<string, number> = {};
    summaryEntries.forEach((entry) => {
      const dayLabel = formatDayLabel(new Date(entry.bucketStartMs));
      if (!dayLabel) return;
      const cost = calculateUsageBucketCost(entry.model, entry.stats, modelPrices);
      if (cost > 0) {
        dayMap[dayLabel] = (dayMap[dayLabel] || 0) + cost;
      }
    });
    const labels = Object.keys(dayMap).sort();
    return { labels, data: labels.map((l) => dayMap[l]), hasData: labels.length > 0 };
  }

  const details = collectUsageDetails(usageData);
  const dayMap: Record<string, number> = {};
  let hasData = false;

  details.forEach((detail) => {
    const timestamp =
      typeof detail.__timestampMs === 'number'
        ? detail.__timestampMs
        : parseTimestampMs(detail.timestamp);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return;
    const dayLabel = formatDayLabel(new Date(timestamp));
    if (!dayLabel) return;

    const cost = calculateCost(detail, modelPrices);
    if (cost > 0) {
      dayMap[dayLabel] = (dayMap[dayLabel] || 0) + cost;
      hasData = true;
    }
  });

  const labels = Object.keys(dayMap).sort();
  const data = labels.map((l) => dayMap[l]);

  return { labels, data, hasData };
}
