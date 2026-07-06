export type PayloadParamValueType = 'string' | 'number' | 'boolean' | 'json';
export type PayloadParamValidationErrorCode =
  | 'payload_invalid_number'
  | 'payload_invalid_boolean'
  | 'payload_invalid_json';

export type VisualConfigFieldPath =
  | 'port'
  | 'logsMaxTotalSizeMb'
  | 'requestRetry'
  | 'maxRetryInterval'
  | 'errorControl.default.retryRounds'
  | 'errorControl.default.roundBackoffBase'
  | 'errorControl.default.roundBackoffExponent'
  | 'errorControl.default.roundBackoffMax'
  | 'routingParallelRequestsMinRound'
  | 'routingParallelRequestsMinFailures'
  | 'streaming.keepaliveSeconds'
  | 'streaming.bootstrapRetries'
  | 'streaming.nonstreamKeepaliveInterval'
  | 'outputFilter.maxLength'
  | `outputFilter.providers.${string}.maxLength`
  | `errorControl.providers.${string}.${ErrorControlPolicyField}`;

export type VisualConfigValidationErrorCode =
  | 'port_range'
  | 'non_negative_integer'
  | 'positive_integer'
  | 'positive_number';

export type VisualConfigValidationErrors = Partial<
  Record<VisualConfigFieldPath, VisualConfigValidationErrorCode>
>;

export type PayloadParamEntry = {
  id: string;
  path: string;
  valueType: PayloadParamValueType;
  value: string;
};

export type PayloadModelEntry = {
  id: string;
  name: string;
  protocol?: string;
};

export type PayloadRule = {
  id: string;
  models: PayloadModelEntry[];
  params: PayloadParamEntry[];
};

export type PayloadFilterRule = {
  id: string;
  models: PayloadModelEntry[];
  params: string[];
};

export interface StreamingConfig {
  keepaliveSeconds: string;
  bootstrapRetries: string;
  nonstreamKeepaliveInterval: string;
}

export interface OutputFilterRuleValues {
  enabled: boolean;
  maxLength: string;
  keywordsText: string;
}

export type OutputFilterProviderRule = OutputFilterRuleValues & {
  id: string;
  provider: string;
};

export interface OutputFilterValues extends OutputFilterRuleValues {
  providers: OutputFilterProviderRule[];
}

export type ErrorControlPolicyField =
  | 'retryRounds'
  | 'roundBackoffBase'
  | 'roundBackoffExponent'
  | 'roundBackoffMax';

export type ErrorControlPolicyValues = Record<ErrorControlPolicyField, string>;

export type ErrorControlProviderPolicy = {
  id: string;
  provider: string;
  policy: ErrorControlPolicyValues;
};

export interface ErrorControlValues {
  default: ErrorControlPolicyValues;
  providers: ErrorControlProviderPolicy[];
}

export type VisualConfigValues = {
  host: string;
  port: string;
  tlsEnable: boolean;
  tlsCert: string;
  tlsKey: string;
  rmAllowRemote: boolean;
  rmSecretKey: string;
  rmDisableControlPanel: boolean;
  rmPanelRepo: string;
  authDir: string;
  apiKeysText: string;
  debug: boolean;
  commercialMode: boolean;
  loggingToFile: boolean;
  logsMaxTotalSizeMb: string;
  usageStatisticsEnabled: boolean;
  proxyUrl: string;
  forceModelPrefix: boolean;
  codexRemoveEmptyInputName: boolean;
  requestRetry: string;
  maxRetryInterval: string;
  errorControl: ErrorControlValues;
  defaultTestModelCodex: string;
  defaultTestModelClaude: string;
  quotaSwitchProject: boolean;
  quotaSwitchPreviewModel: boolean;
  quotaAntigravityCredits: boolean;
  routingStrategy: 'random' | 'last-success';
  routingParallelRequestsEnabled: boolean;
  routingParallelRequestsMinRound: string;
  routingParallelRequestsMinFailures: string;
  routingSessionAffinity: boolean;
  routingSessionAffinityTTL: string;
  wsAuth: boolean;
  payloadDefaultRules: PayloadRule[];
  payloadDefaultRawRules: PayloadRule[];
  payloadOverrideRules: PayloadRule[];
  payloadOverrideRawRules: PayloadRule[];
  payloadFilterRules: PayloadFilterRule[];
  streaming: StreamingConfig;
  outputFilter: OutputFilterValues;
};

export const makeClientId = () => {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
};

export const DEFAULT_VISUAL_VALUES: VisualConfigValues = {
  host: '',
  port: '',
  tlsEnable: false,
  tlsCert: '',
  tlsKey: '',
  rmAllowRemote: false,
  rmSecretKey: '',
  rmDisableControlPanel: false,
  rmPanelRepo: '',
  authDir: '',
  apiKeysText: '',
  debug: false,
  commercialMode: false,
  loggingToFile: false,
  logsMaxTotalSizeMb: '',
  usageStatisticsEnabled: false,
  proxyUrl: '',
  forceModelPrefix: false,
  codexRemoveEmptyInputName: false,
  requestRetry: '',
  maxRetryInterval: '',
  errorControl: {
    default: {
      retryRounds: '',
      roundBackoffBase: '',
      roundBackoffExponent: '',
      roundBackoffMax: '',
    },
    providers: [],
  },
  defaultTestModelCodex: '',
  defaultTestModelClaude: '',
  quotaSwitchProject: true,
  quotaSwitchPreviewModel: true,
  quotaAntigravityCredits: true,
  routingStrategy: 'random',
  routingParallelRequestsEnabled: false,
  routingParallelRequestsMinRound: '',
  routingParallelRequestsMinFailures: '',
  routingSessionAffinity: false,
  routingSessionAffinityTTL: '',
  wsAuth: false,
  payloadDefaultRules: [],
  payloadDefaultRawRules: [],
  payloadOverrideRules: [],
  payloadOverrideRawRules: [],
  payloadFilterRules: [],
  streaming: {
    keepaliveSeconds: '',
    bootstrapRetries: '',
    nonstreamKeepaliveInterval: '',
  },
  outputFilter: {
    enabled: false,
    maxLength: '',
    keywordsText: '',
    providers: [],
  },
};
