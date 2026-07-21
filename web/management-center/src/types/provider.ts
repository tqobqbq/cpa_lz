/**
 * AI 提供商相关类型
 * 基于原项目 src/modules/ai-providers.js
 */

export interface ModelAlias {
  name: string;
  alias?: string;
  priority?: number;
  testModel?: string;
  image?: boolean;
  thinking?: Record<string, unknown>;
}

export type BackoffMode = 'default' | 'off' | 'custom';
export type ClaudeAuthMode = 'auto' | 'api-key' | 'bearer';

export interface ProviderCooldownConfig {
  start?: number;
  exponent?: number;
  max?: number;
}

export interface ApiKeyEntry {
  apiKey: string;
  proxyUrl?: string;
  headers?: Record<string, string>;
  authIndex?: string;
}

export interface CloakConfig {
  mode?: string;
  strictMode?: boolean;
  sensitiveWords?: string[];
  cacheUserId?: boolean;
}

export interface GeminiKeyConfig {
  apiKey: string;
  priority?: number;
  prefix?: string;
  baseUrl?: string;
  proxyUrl?: string;
  backoffMode?: BackoffMode;
  requestRetry?: number;
  cooldown?: ProviderCooldownConfig;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  excludedModels?: string[];
  disableCooling?: boolean;
  authIndex?: string;
}

export interface ProviderKeyConfig {
  apiKey: string;
  priority?: number;
  prefix?: string;
  baseUrl?: string;
  authMode?: ClaudeAuthMode;
  useV1?: boolean;
  websockets?: boolean;
  proxyUrl?: string;
  backoffMode?: BackoffMode;
  requestRetry?: number;
  cooldown?: ProviderCooldownConfig;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  excludedModels?: string[];
  disableCooling?: boolean;
  cloak?: CloakConfig;
  experimentalCchSigning?: boolean;
  authIndex?: string;
}

export interface OpenAIProviderConfig {
  name: string;
  prefix?: string;
  baseUrl: string;
  apiKeyEntries: ApiKeyEntry[];
  disabled?: boolean;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  priority?: number;
  backoffMode?: BackoffMode;
  requestRetry?: number;
  cooldown?: ProviderCooldownConfig;
  testModel?: string;
  disableCooling?: boolean;
  authIndex?: string;
  /** Original index in the backend openai-compatibility array. */
  sourceIndex?: number;
  [key: string]: unknown;
}
