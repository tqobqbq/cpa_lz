/**
 * 配置相关类型定义
 * 与基线 /config 返回结构保持一致（内部使用驼峰形式）
 */

import type { GeminiKeyConfig, ProviderKeyConfig, OpenAIProviderConfig } from './provider';

export interface AuthFilesGroupConfig {
  enabled: boolean;
  priority: number;
  proxyUrl: string;
}

export interface RoutingUserAgentRuleConfig {
  match?: 'equals' | 'contains';
  value?: string;
}

export interface RoutingInputCharsRuleConfig {
  operator?: 'gt' | 'lt';
  value?: number;
}

export interface RoutingRuleConfig {
  provider: string;
  userAgent?: RoutingUserAgentRuleConfig;
  inputChars?: RoutingInputCharsRuleConfig;
}


export interface QuotaExceededConfig {
  switchProject?: boolean;
  switchPreviewModel?: boolean;
  antigravityCredits?: boolean;
}

export interface Config {
  debug?: boolean;
  proxyUrl?: string;
  requestRetry?: number;
  quotaExceeded?: QuotaExceededConfig;
  requestLog?: boolean;
  loggingToFile?: boolean;
  logsMaxTotalSizeMb?: number;
  wsAuth?: boolean;
  forceModelPrefix?: boolean;
  codexRemoveEmptyInputName?: boolean;
  routingStrategy?: string;
  routingRules?: RoutingRuleConfig[];
  authFilesGroup?: AuthFilesGroupConfig;
  apiKeys?: string[];
  geminiApiKeys?: GeminiKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
  oauthExcludedModels?: Record<string, string[]>;
  raw?: Record<string, unknown>;
}

export type RawConfigSection =
  | 'debug'
  | 'proxy-url'
  | 'request-retry'
  | 'quota-exceeded'
  | 'request-log'
  | 'logging-to-file'
  | 'logs-max-total-size-mb'
  | 'ws-auth'
  | 'force-model-prefix'
  | 'codex-remove-empty-input-name'
  | 'routing/strategy'
  | 'routing/rules'
  | 'auth-files-group'
  | 'api-keys'
  | 'gemini-api-key'
  | 'codex-api-key'
  | 'claude-api-key'
  | 'vertex-api-key'
  | 'openai-compatibility'
  | 'oauth-excluded-models';

export interface ConfigCache {
  data: Config;
  timestamp: number;
}
