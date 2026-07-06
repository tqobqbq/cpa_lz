import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import {
  AmpcodeSection,
  ClaudeSection,
  CodexSection,
  GeminiSection,
  OpenAISection,
  VertexSection,
  ProviderCooldownFields,
  ProviderNav,
  AiProviderQuickImportPanel,
  type ProviderId,
  type QuickImportTestState,
  useProviderStats,
} from '@/components/providers';
import { ProviderTestPanel } from '@/components/providers/ProviderTestPanel';
import {
  hasDisableAllModelsRule,
  withDisableAllModelsRule,
  withoutDisableAllModelsRule,
} from '@/components/providers/utils';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { ampcodeApi, apiCallApi, configApi, getApiCallErrorMessage, providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore, useThemeStore } from '@/stores';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderCooldownConfig,
  ProviderKeyConfig,
} from '@/types';
import {
  areProviderCooldownConfigsEqual,
  normalizeProviderCooldown,
  withProviderCooldownDefaults,
} from '@/utils/providerCooldown';
import {
  buildProviderTestModelOptions,
  buildProviderTestModelPlaceholder,
  resolveProviderTestExecutionCommand,
  resolveProviderTestModel,
  syncEditableTestCommand,
} from '@/components/providers/providerTest';
import {
  buildClaudeProviderTestRequest,
  buildCodexProviderTestRequest,
} from '@/components/providers/providerTestRequest';
import { parseApiCallCommand } from '@/components/providers/testCommand';
import { indexUsageDetailsByAuthIndex, indexUsageDetailsBySource } from '@/utils/usageIndex';
import {
  buildApiCallCommandPreview,
  buildClaudeMessagesEndpoint,
  buildOpenAIResponsesEndpoint,
  formatApiCallResultPreview,
} from '@/components/providers/utils';
import { getProviderConfigKey } from '@/components/providers/utils';
import { requestAiProviderEditModalOpen } from '@/utils/aiProviderEditModal';
import {
  isQuickImportShortcut,
  type QuickImportProvider,
} from '@/components/providers/quickImport';
import styles from './AiProvidersPage.module.scss';

const PROVIDER_TEST_TIMEOUT_MS = 30_000;
const PROVIDER_IDS: ProviderId[] = ['gemini', 'codex', 'claude', 'vertex', 'ampcode', 'openai'];

type ProviderTestDialogState = {
  provider: 'codex' | 'claude';
  index: number;
};

type EnabledProviderSummaryItem = {
  key: string;
  title: string;
  path: string;
  meta: Array<{ label: string; value: string | number }>;
};

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

export function AiProvidersPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const isCacheValid = useConfigStore((state) => state.isCacheValid);

  const hasMounted = useRef(false);
  const [loading, setLoading] = useState(() => !isCacheValid());
  const [error, setError] = useState('');

  const [geminiKeys, setGeminiKeys] = useState<GeminiKeyConfig[]>(
    () => config?.geminiApiKeys || []
  );
  const [codexConfigs, setCodexConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.codexApiKeys || []
  );
  const [claudeConfigs, setClaudeConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.claudeApiKeys || []
  );
  const [vertexConfigs, setVertexConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.vertexApiKeys || []
  );
  const [openaiProviders, setOpenaiProviders] = useState<OpenAIProviderConfig[]>(
    () => config?.openaiCompatibility || []
  );
  const [providerCooldown, setProviderCooldown] = useState<ProviderCooldownConfig>(() =>
    withProviderCooldownDefaults(config?.providerCooldown)
  );
  const [savingProviderCooldown, setSavingProviderCooldown] = useState(false);

  const [activeProviderId, setActiveProviderId] = useState<ProviderId>('gemini');
  const [configSwitchingKey, setConfigSwitchingKey] = useState<string | null>(null);
  const [testingProviderKey, setTestingProviderKey] = useState<string | null>(null);
  const [providerTestDialog, setProviderTestDialog] = useState<ProviderTestDialogState | null>(
    null
  );
  const [providerTestModel, setProviderTestModel] = useState('');
  const [providerTestStatus, setProviderTestStatus] = useState<
    'idle' | 'loading' | 'success' | 'error'
  >('idle');
  const [providerTestMessage, setProviderTestMessage] = useState('');
  const [providerTestCommand, setProviderTestCommand] = useState('');
  const [providerTestResult, setProviderTestResult] = useState('');
  const previousGeneratedProviderTestCommandRef = useRef('');
  const [quickImportOpen, setQuickImportOpen] = useState(false);
  const [quickImportProvider, setQuickImportProvider] = useState<QuickImportProvider>('codex');
  const [quickImportBaseUrl, setQuickImportBaseUrl] = useState('');
  const [quickImportApiKey, setQuickImportApiKey] = useState('');
  const [quickImportModel, setQuickImportModel] = useState('');
  const [quickImportCommand, setQuickImportCommand] = useState('');
  const [quickImportStatus, setQuickImportStatus] = useState<
    'idle' | 'loading' | 'success' | 'error'
  >('idle');
  const [quickImportMessage, setQuickImportMessage] = useState('');
  const [quickImportResult, setQuickImportResult] = useState('');
  const previousGeneratedQuickImportCommandRef = useRef('');

  const disableControls = connectionStatus !== 'connected';
  const isSwitching = Boolean(configSwitchingKey);

  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;

  const { keyStats, usage, usageDetails, loadKeyStats, refreshKeyStats } = useProviderStats({
    enabled: isCurrentLayer,
  });
  const usageDetailsBySource = useMemo(
    () => indexUsageDetailsBySource(usageDetails),
    [usageDetails]
  );
  const usageDetailsByAuthIndex = useMemo(
    () => indexUsageDetailsByAuthIndex(usageDetails),
    [usageDetails]
  );

  const getErrorMessage = (err: unknown) => {
    if (err instanceof Error) return err.message;
    if (typeof err === 'string') return err;
    return '';
  };

  const savedProviderCooldown = useMemo(
    () => withProviderCooldownDefaults(config?.providerCooldown),
    [config?.providerCooldown]
  );
  const normalizedProviderCooldown = useMemo(
    () => withProviderCooldownDefaults(providerCooldown),
    [providerCooldown]
  );
  const isProviderCooldownDirty = useMemo(
    () =>
      !areProviderCooldownConfigsEqual(savedProviderCooldown, normalizedProviderCooldown),
    [normalizedProviderCooldown, savedProviderCooldown]
  );

  const saveProviderCooldown = useCallback(async () => {
    if (disableControls || savingProviderCooldown || !isProviderCooldownDirty) return;

    const payload = normalizeProviderCooldown(normalizedProviderCooldown);
    if (!payload) return;

    setSavingProviderCooldown(true);
    try {
      await configApi.updateProviderCooldown(payload);
      updateConfigValue('provider-cooldown', payload);
      clearCache('provider-cooldown');
      showNotification(t('ai_providers.cooldown_global_saved'), 'success');
    } catch (err: unknown) {
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSavingProviderCooldown(false);
    }
  }, [
    clearCache,
    disableControls,
    getErrorMessage,
    isProviderCooldownDirty,
    normalizedProviderCooldown,
    savingProviderCooldown,
    showNotification,
    t,
    updateConfigValue,
  ]);

  const loadConfigs = useCallback(async () => {
    const hasValidCache = isCacheValid();
    if (!hasValidCache) {
      setLoading(true);
    }
    setError('');
    try {
      const [configResult, vertexResult, ampcodeResult] = await Promise.allSettled([
        fetchConfig(),
        providersApi.getVertexConfigs(),
        ampcodeApi.getAmpcode(),
      ]);

      if (configResult.status !== 'fulfilled') {
        throw configResult.reason;
      }

      const data = configResult.value;
      setGeminiKeys(data?.geminiApiKeys || []);
      setCodexConfigs(data?.codexApiKeys || []);
      setClaudeConfigs(data?.claudeApiKeys || []);
      setVertexConfigs(data?.vertexApiKeys || []);
      setOpenaiProviders(data?.openaiCompatibility || []);
      setProviderCooldown(withProviderCooldownDefaults(data?.providerCooldown));

      if (vertexResult.status === 'fulfilled') {
        setVertexConfigs(vertexResult.value || []);
        updateConfigValue('vertex-api-key', vertexResult.value || []);
        clearCache('vertex-api-key');
      }

      if (ampcodeResult.status === 'fulfilled') {
        updateConfigValue('ampcode', ampcodeResult.value);
        clearCache('ampcode');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err) || t('notification.refresh_failed');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [clearCache, fetchConfig, isCacheValid, t, updateConfigValue]);

  useEffect(() => {
    if (hasMounted.current) return;
    hasMounted.current = true;
    loadConfigs();
  }, [loadConfigs]);

  useEffect(() => {
    if (!isCurrentLayer) return;
    void loadKeyStats().catch(() => {});
  }, [isCurrentLayer, loadKeyStats]);

  useEffect(() => {
    if (config?.geminiApiKeys) setGeminiKeys(config.geminiApiKeys);
    if (config?.codexApiKeys) setCodexConfigs(config.codexApiKeys);
    if (config?.claudeApiKeys) setClaudeConfigs(config.claudeApiKeys);
    if (config?.vertexApiKeys) setVertexConfigs(config.vertexApiKeys);
    if (config?.openaiCompatibility) setOpenaiProviders(config.openaiCompatibility);
    if (config) setProviderCooldown(withProviderCooldownDefaults(config.providerCooldown));
  }, [
    config?.geminiApiKeys,
    config?.codexApiKeys,
    config?.claudeApiKeys,
    config?.vertexApiKeys,
    config?.openaiCompatibility,
    config?.providerCooldown,
  ]);

  useHeaderRefresh(refreshKeyStats, isCurrentLayer);

  useEffect(() => {
    if (!isCurrentLayer) return;

    const resolveScrollParent = () => {
      const content = document.querySelector('.content') as HTMLElement | null;
      return content;
    };

    const updateActiveProvider = () => {
      const content = resolveScrollParent();
      const containerTop = content?.getBoundingClientRect().top ?? 0;
      const activationLine = containerTop + 32;
      let nextActive: ProviderId = PROVIDER_IDS[0];

      PROVIDER_IDS.forEach((providerId) => {
        const element = document.getElementById(`provider-${providerId}`);
        if (!element) return;
        if (element.getBoundingClientRect().top <= activationLine) {
          nextActive = providerId;
        }
      });

      setActiveProviderId(nextActive);
    };

    const content = resolveScrollParent();
    window.addEventListener('scroll', updateActiveProvider, { passive: true });
    window.addEventListener('resize', updateActiveProvider);
    content?.addEventListener('scroll', updateActiveProvider, { passive: true });
    const raf = requestAnimationFrame(updateActiveProvider);

    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener('scroll', updateActiveProvider);
      window.removeEventListener('resize', updateActiveProvider);
      content?.removeEventListener('scroll', updateActiveProvider);
    };
  }, [isCurrentLayer]);

  useEffect(() => {
    if (!isCurrentLayer) return;
    const handleQuickImportShortcut = (event: KeyboardEvent) => {
      if (isQuickImportShortcut(event)) {
        event.preventDefault();
        setQuickImportOpen(true);
      }
    };
    window.addEventListener('keydown', handleQuickImportShortcut);
    return () => window.removeEventListener('keydown', handleQuickImportShortcut);
  }, [isCurrentLayer]);

  const activeProviderTest = useMemo(() => {
    if (!providerTestDialog) return null;
    const source = providerTestDialog.provider === 'codex' ? codexConfigs : claudeConfigs;
    const item = source[providerTestDialog.index];
    if (!item) return null;
    return {
      ...providerTestDialog,
      item,
      key: `${providerTestDialog.provider}:${getProviderConfigKey(item, providerTestDialog.index)}`,
    };
  }, [claudeConfigs, codexConfigs, providerTestDialog]);

  useEffect(() => {
    if (providerTestDialog && !activeProviderTest) {
      setProviderTestDialog(null);
    }
  }, [activeProviderTest, providerTestDialog]);

  useEffect(() => {
    if (!providerTestDialog) return;
    setProviderTestModel('');
    setProviderTestStatus('idle');
    setProviderTestMessage('');
    setProviderTestCommand('');
    setProviderTestResult('');
    previousGeneratedProviderTestCommandRef.current = '';
  }, [providerTestDialog]);

  const providerTestAvailableModels = useMemo(
    () =>
      (activeProviderTest?.item.models ?? [])
        .map((model) => String(model?.name ?? '').trim())
        .filter(Boolean),
    [activeProviderTest]
  );
  const providerTestDefaultModel = useMemo(() => {
    if (activeProviderTest?.provider === 'claude') {
      return String(config?.defaultTestModels?.claude ?? '').trim();
    }
    if (activeProviderTest?.provider === 'codex') {
      return String(config?.defaultTestModels?.codex ?? '').trim();
    }
    return '';
  }, [
    activeProviderTest?.provider,
    config?.defaultTestModels?.claude,
    config?.defaultTestModels?.codex,
  ]);
  const resolvedProviderTestModel = useMemo(
    () =>
      resolveProviderTestModel(
        providerTestModel,
        providerTestDefaultModel,
        providerTestAvailableModels
      ),
    [providerTestAvailableModels, providerTestDefaultModel, providerTestModel]
  );
  const providerTestModelOptions = useMemo(
    () =>
      buildProviderTestModelOptions(
        activeProviderTest?.item.models ?? [],
        providerTestDefaultModel,
        {
          defaultModel: (model) =>
            t('ai_providers.provider_test_use_default_model', {
              model,
            }),
          firstAvailable: t('ai_providers.provider_test_use_first_model'),
        }
      ),
    [activeProviderTest?.item.models, providerTestDefaultModel, t]
  );
  const generatedProviderTestCommand = useMemo(() => {
    if (!activeProviderTest) return '';

    if (activeProviderTest.provider === 'codex') {
      const headers = {
        'Content-Type': 'application/json',
        ...(activeProviderTest.item.headers ?? {}),
      } as Record<string, string>;
      const apiKey = String(activeProviderTest.item.apiKey ?? '').trim();
      if (!hasHeader(headers, 'authorization')) {
        headers.Authorization = apiKey ? `Bearer ${apiKey}` : 'Bearer <api-key>';
      }

      return buildApiCallCommandPreview({
        method: 'POST',
        url:
          buildOpenAIResponsesEndpoint(
            activeProviderTest.item.baseUrl ?? '',
            activeProviderTest.item.useV1 !== false
          ) || 'https://api.openai.com/v1/responses',
        header: headers,
        data: JSON.stringify({
          model:
            resolvedProviderTestModel ||
            buildProviderTestModelPlaceholder(providerTestDefaultModel),
          input: 'Hi',
          max_output_tokens: 128,
        }),
      });
    }

    return buildApiCallCommandPreview(
      buildClaudeProviderTestRequest({
        apiKey: String(activeProviderTest.item.apiKey ?? '').trim() || '<api-key>',
        authMode: activeProviderTest.item.authMode ?? 'auto',
        baseUrl: activeProviderTest.item.baseUrl ?? '',
        headers: activeProviderTest.item.headers ?? {},
        model:
          resolvedProviderTestModel || buildProviderTestModelPlaceholder(providerTestDefaultModel),
        proxyUrl: activeProviderTest.item.proxyUrl?.trim() || undefined,
      })
    );
  }, [activeProviderTest, providerTestDefaultModel, resolvedProviderTestModel]);

  useEffect(() => {
    if (!providerTestDialog) return;
    setProviderTestCommand((current) => {
      const next = syncEditableTestCommand(
        current,
        previousGeneratedProviderTestCommandRef.current,
        generatedProviderTestCommand
      );
      previousGeneratedProviderTestCommandRef.current = generatedProviderTestCommand;
      return next;
    });
  }, [generatedProviderTestCommand, providerTestDialog]);

  const openProviderTestDialog = useCallback((provider: 'codex' | 'claude', index: number) => {
    setProviderTestDialog({ provider, index });
  }, []);

  const closeProviderTestDialog = useCallback(() => {
    setProviderTestDialog(null);
  }, []);

  const openEditor = useCallback((path: string) => {
    requestAiProviderEditModalOpen(path);
  }, []);

  const deleteGemini = async (index: number) => {
    const entry = geminiKeys[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.gemini_delete_title', { defaultValue: 'Delete Gemini Key' }),
      message: t('ai_providers.gemini_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteGeminiKey(entry.apiKey, entry.baseUrl);
          const next = geminiKeys.filter((_, idx) => idx !== index);
          setGeminiKeys(next);
          updateConfigValue('gemini-api-key', next);
          clearCache('gemini-api-key');
          showNotification(t('notification.gemini_key_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const setConfigEnabled = async (
    provider: 'gemini' | 'codex' | 'claude' | 'vertex',
    index: number,
    enabled: boolean
  ) => {
    if (provider === 'gemini') {
      const current = geminiKeys[index];
      if (!current) return;

      const switchingKey = `${provider}:${current.apiKey}`;
      setConfigSwitchingKey(switchingKey);

      const previousList = geminiKeys;
      const nextExcluded = enabled
        ? withoutDisableAllModelsRule(current.excludedModels)
        : withDisableAllModelsRule(current.excludedModels);
      const nextItem: GeminiKeyConfig = { ...current, excludedModels: nextExcluded };
      const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

      setGeminiKeys(nextList);
      updateConfigValue('gemini-api-key', nextList);
      clearCache('gemini-api-key');

      try {
        await providersApi.saveGeminiKeys(nextList);
        showNotification(
          enabled ? t('notification.config_enabled') : t('notification.config_disabled'),
          'success'
        );
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setGeminiKeys(previousList);
        updateConfigValue('gemini-api-key', previousList);
        clearCache('gemini-api-key');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    const source =
      provider === 'codex' ? codexConfigs : provider === 'claude' ? claudeConfigs : vertexConfigs;
    const current = source[index];
    if (!current) return;

    const switchingKey = `${provider}:${current.apiKey}`;
    setConfigSwitchingKey(switchingKey);

    const previousList = source;
    const nextExcluded = enabled
      ? withoutDisableAllModelsRule(current.excludedModels)
      : withDisableAllModelsRule(current.excludedModels);
    const nextItem: ProviderKeyConfig = { ...current, excludedModels: nextExcluded };
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    if (provider === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else if (provider === 'claude') {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    } else {
      setVertexConfigs(nextList);
      updateConfigValue('vertex-api-key', nextList);
      clearCache('vertex-api-key');
    }

    try {
      if (provider === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
      } else if (provider === 'claude') {
        await providersApi.saveClaudeConfigs(nextList);
      } else {
        await providersApi.saveVertexConfigs(nextList);
      }
      showNotification(
        enabled ? t('notification.config_enabled') : t('notification.config_disabled'),
        'success'
      );
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (provider === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else if (provider === 'claude') {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      } else {
        setVertexConfigs(previousList);
        updateConfigValue('vertex-api-key', previousList);
        clearCache('vertex-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const deleteProviderEntry = async (type: 'codex' | 'claude', index: number) => {
    const source = type === 'codex' ? codexConfigs : claudeConfigs;
    const entry = source[index];
    if (!entry) return;
    showConfirmation({
      title: t(`ai_providers.${type}_delete_title`, {
        defaultValue: `Delete ${type === 'codex' ? 'Codex' : 'Claude'} Config`,
      }),
      message: t(`ai_providers.${type}_delete_confirm`),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          if (type === 'codex') {
            await providersApi.deleteCodexConfig(entry.apiKey, entry.baseUrl);
            const next = codexConfigs.filter((_, idx) => idx !== index);
            setCodexConfigs(next);
            updateConfigValue('codex-api-key', next);
            clearCache('codex-api-key');
            showNotification(t('notification.codex_config_deleted'), 'success');
          } else {
            await providersApi.deleteClaudeConfig(entry.apiKey, entry.baseUrl);
            const next = claudeConfigs.filter((_, idx) => idx !== index);
            setClaudeConfigs(next);
            updateConfigValue('claude-api-key', next);
            clearCache('claude-api-key');
            showNotification(t('notification.claude_config_deleted'), 'success');
          }
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const deleteVertex = async (index: number) => {
    const entry = vertexConfigs[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.vertex_delete_title', { defaultValue: 'Delete Vertex Config' }),
      message: t('ai_providers.vertex_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteVertexConfig(entry.apiKey, entry.baseUrl);
          const next = vertexConfigs.filter((_, idx) => idx !== index);
          setVertexConfigs(next);
          updateConfigValue('vertex-api-key', next);
          clearCache('vertex-api-key');
          showNotification(t('notification.vertex_config_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const deleteOpenai = async (index: number) => {
    const entry = openaiProviders[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.openai_delete_title', { defaultValue: 'Delete OpenAI Provider' }),
      message: t('ai_providers.openai_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteOpenAIProvider(entry.name);
          const next = openaiProviders.filter((_, idx) => idx !== index);
          setOpenaiProviders(next);
          updateConfigValue('openai-compatibility', next);
          clearCache('openai-compatibility');
          showNotification(t('notification.openai_provider_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const runProviderTest = useCallback(
    (provider: 'codex' | 'claude', index: number) => {
      openProviderTestDialog(provider, index);
    },
    [openProviderTestDialog]
  );

  const executeProviderTest = useCallback(
    async (mode: 'generated' | 'edited') => {
      if (!activeProviderTest) return;

      const activeCommand = resolveProviderTestExecutionCommand(
        providerTestCommand,
        generatedProviderTestCommand,
        mode
      );
      const usingGeneratedCommand = mode === 'generated';
      let requestPayload;

      try {
        if (activeProviderTest.provider === 'codex' && usingGeneratedCommand) {
          const modelName = resolveProviderTestModel(
            providerTestModel,
            providerTestDefaultModel,
            providerTestAvailableModels
          );
          if (!modelName) {
            throw new Error(t('ai_providers.codex_test_model_required'));
          }

          const endpoint = buildOpenAIResponsesEndpoint(
            activeProviderTest.item.baseUrl ?? '',
            activeProviderTest.item.useV1 !== false
          );
          if (!endpoint) {
            throw new Error(t('ai_providers.codex_test_endpoint_invalid'));
          }

          const headers = {
            'Content-Type': 'application/json',
            ...(activeProviderTest.item.headers ?? {}),
          } as Record<string, string>;
          const apiKey = String(activeProviderTest.item.apiKey ?? '').trim();
          if (!apiKey && !hasHeader(headers, 'authorization')) {
            throw new Error(t('ai_providers.codex_test_key_required'));
          }
          if (apiKey && !hasHeader(headers, 'authorization')) {
            headers.Authorization = `Bearer ${apiKey}`;
          }

          requestPayload = {
            method: 'POST',
            url: endpoint,
            header: headers,
            data: JSON.stringify({
              model: modelName,
              input: 'Hi',
              max_output_tokens: 128,
            }),
            proxyUrl: activeProviderTest.item.proxyUrl?.trim() || undefined,
          };
        } else if (activeProviderTest.provider === 'claude' && usingGeneratedCommand) {
          const modelName = resolveProviderTestModel(
            providerTestModel,
            providerTestDefaultModel,
            providerTestAvailableModels
          );
          if (!modelName) {
            throw new Error(t('ai_providers.claude_test_model_required'));
          }

          const endpoint = buildClaudeMessagesEndpoint(activeProviderTest.item.baseUrl ?? '');
          if (!endpoint) {
            throw new Error(t('ai_providers.claude_test_endpoint_invalid'));
          }

          const headers = { ...(activeProviderTest.item.headers ?? {}) } as Record<string, string>;
          const hasApiKeyHeader = hasHeader(headers, 'x-api-key');
          const hasAuthorizationHeader = hasHeader(headers, 'authorization');
          const apiKey = String(activeProviderTest.item.apiKey ?? '').trim();
          const resolvedApiKey = apiKey || resolveBearerTokenFromAuthorization(headers);
          if (!resolvedApiKey && !hasApiKeyHeader && !hasAuthorizationHeader) {
            throw new Error(t('ai_providers.claude_test_key_required'));
          }

          requestPayload = buildClaudeProviderTestRequest({
            apiKey: resolvedApiKey,
            authMode: activeProviderTest.item.authMode ?? 'auto',
            baseUrl: endpoint,
            headers,
            model: modelName,
            proxyUrl: activeProviderTest.item.proxyUrl?.trim() || undefined,
          });
        } else {
          if (!activeCommand) {
            throw new Error(t('ai_providers.provider_test_command_invalid'));
          }
          requestPayload = {
            ...parseApiCallCommand(activeCommand),
            proxyUrl: activeProviderTest.item.proxyUrl?.trim() || undefined,
          };
        }
      } catch (err: unknown) {
        const message = getErrorMessage(err) || t('common.unknown_error');
        setProviderTestStatus('error');
        setProviderTestMessage(message);
        showNotification(message, 'error');
        return;
      }

      setTestingProviderKey(activeProviderTest.key);
      setProviderTestStatus('loading');
      setProviderTestMessage(
        activeProviderTest.provider === 'codex'
          ? t('ai_providers.codex_test_running')
          : t('ai_providers.claude_test_running')
      );
      setProviderTestCommand(usingGeneratedCommand ? generatedProviderTestCommand : activeCommand);
      setProviderTestResult('');
      let resultPreview = '';

      try {
        const result = await apiCallApi.request(requestPayload, {
          timeout: PROVIDER_TEST_TIMEOUT_MS,
        });
        resultPreview = formatApiCallResultPreview(result);
        setProviderTestResult(resultPreview);

        if (result.statusCode < 200 || result.statusCode >= 300) {
          throw new Error(getApiCallErrorMessage(result));
        }

        const message =
          activeProviderTest.provider === 'codex'
            ? t('ai_providers.codex_test_success')
            : t('ai_providers.claude_test_success');
        setProviderTestStatus('success');
        setProviderTestMessage(message);
        showNotification(message, 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        const errorCode =
          typeof err === 'object' && err !== null && 'code' in err
            ? String((err as { code?: string }).code)
            : '';
        const isTimeout = errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
        if (!resultPreview) {
          setProviderTestResult(message || t('common.unknown_error'));
        }
        const resolvedMessage = isTimeout
          ? t(
              activeProviderTest.provider === 'codex'
                ? 'ai_providers.codex_test_timeout'
                : 'ai_providers.claude_test_timeout',
              { seconds: PROVIDER_TEST_TIMEOUT_MS / 1000 }
            )
          : `${t(
              activeProviderTest.provider === 'codex'
                ? 'ai_providers.codex_test_failed'
                : 'ai_providers.claude_test_failed'
            )}: ${message || t('common.unknown_error')}`;
        setProviderTestStatus('error');
        setProviderTestMessage(resolvedMessage);
        showNotification(resolvedMessage, 'error');
      } finally {
        setTestingProviderKey(null);
      }
    },
    [
      activeProviderTest,
      generatedProviderTestCommand,
      providerTestAvailableModels,
      providerTestCommand,
      providerTestDefaultModel,
      providerTestModel,
      showNotification,
      t,
    ]
  );

  const quickImportDefaultModel = useMemo(() => {
    if (quickImportProvider === 'claude') {
      return String(config?.defaultTestModels?.claude ?? '').trim();
    }
    return String(config?.defaultTestModels?.codex ?? '').trim();
  }, [config?.defaultTestModels?.claude, config?.defaultTestModels?.codex, quickImportProvider]);

  const quickImportModelOptions = useMemo(
    () =>
      buildProviderTestModelOptions([], quickImportDefaultModel, {
        defaultModel: (model) => t('ai_providers.provider_test_use_default_model', { model }),
        firstAvailable: t('ai_providers.provider_test_use_first_model'),
      }),
    [quickImportDefaultModel, t]
  );

  const resolvedQuickImportModel = useMemo(
    () => resolveProviderTestModel(quickImportModel, quickImportDefaultModel, []),
    [quickImportDefaultModel, quickImportModel]
  );

  const generatedQuickImportCommand = useMemo(() => {
    const model =
      resolvedQuickImportModel || buildProviderTestModelPlaceholder(quickImportDefaultModel);
    const request =
      quickImportProvider === 'codex'
        ? buildCodexProviderTestRequest({
            apiKey: quickImportApiKey.trim() || '<api-key>',
            baseUrl: quickImportBaseUrl.trim() || '<base-url>',
            headers: {},
            model,
            useV1: true,
          })
        : buildClaudeProviderTestRequest({
            apiKey: quickImportApiKey.trim() || '<api-key>',
            baseUrl: quickImportBaseUrl.trim() || '<base-url>',
            headers: {},
            model,
          });
    return buildApiCallCommandPreview(request);
  }, [
    quickImportApiKey,
    quickImportBaseUrl,
    quickImportDefaultModel,
    quickImportProvider,
    resolvedQuickImportModel,
  ]);

  useEffect(() => {
    if (!quickImportOpen) return;
    setQuickImportCommand((current) => {
      const next = syncEditableTestCommand(
        current,
        previousGeneratedQuickImportCommandRef.current,
        generatedQuickImportCommand
      );
      previousGeneratedQuickImportCommandRef.current = generatedQuickImportCommand;
      return next;
    });
  }, [generatedQuickImportCommand, quickImportOpen]);

  const executeQuickImportTest = useCallback(
    async (mode: 'generated' | 'edited') => {
      if (mode === 'generated') {
        if (!quickImportBaseUrl.trim() || !quickImportApiKey.trim()) {
          const message = t('ai_providers.quick_import_missing_fields');
          setQuickImportStatus('error');
          setQuickImportMessage(message);
          showNotification(message, 'error');
          return;
        }
        if (!resolvedQuickImportModel) {
          const message =
            quickImportProvider === 'codex'
              ? t('ai_providers.codex_test_model_required')
              : t('ai_providers.claude_test_model_required');
          setQuickImportStatus('error');
          setQuickImportMessage(message);
          showNotification(message, 'error');
          return;
        }
      }

      const activeCommand = resolveProviderTestExecutionCommand(
        quickImportCommand,
        generatedQuickImportCommand,
        mode
      );
      if (!activeCommand.trim()) {
        const message = t('ai_providers.provider_test_command_invalid');
        setQuickImportStatus('error');
        setQuickImportMessage(message);
        showNotification(message, 'error');
        return;
      }

      setQuickImportStatus('loading');
      setQuickImportMessage(
        quickImportProvider === 'codex'
          ? t('ai_providers.codex_test_running')
          : t('ai_providers.claude_test_running')
      );
      setQuickImportCommand(activeCommand);
      setQuickImportResult('');
      let resultPreview = '';

      try {
        const result = await apiCallApi.request(parseApiCallCommand(activeCommand), {
          timeout: PROVIDER_TEST_TIMEOUT_MS,
        });
        resultPreview = formatApiCallResultPreview(result);
        setQuickImportResult(resultPreview);
        if (result.statusCode < 200 || result.statusCode >= 300) {
          throw new Error(getApiCallErrorMessage(result));
        }
        const message =
          quickImportProvider === 'codex'
            ? t('ai_providers.codex_test_success')
            : t('ai_providers.claude_test_success');
        setQuickImportStatus('success');
        setQuickImportMessage(message);
        showNotification(message, 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err) || t('common.unknown_error');
        if (!resultPreview) {
          setQuickImportResult(message);
        }
        const resolvedMessage = `${t(
          quickImportProvider === 'codex'
            ? 'ai_providers.codex_test_failed'
            : 'ai_providers.claude_test_failed'
        )}: ${message}`;
        setQuickImportStatus('error');
        setQuickImportMessage(resolvedMessage);
        showNotification(resolvedMessage, 'error');
      }
    },
    [
      generatedQuickImportCommand,
      quickImportApiKey,
      quickImportBaseUrl,
      quickImportCommand,
      quickImportProvider,
      resolvedQuickImportModel,
      showNotification,
      t,
    ]
  );

  const appendQuickImportProvider = useCallback(
    async (provider: QuickImportProvider, entry: ProviderKeyConfig) => {
      if (provider === 'codex') {
        const previous = codexConfigs;
        const next = [...previous, entry];
        setCodexConfigs(next);
        updateConfigValue('codex-api-key', next);
        clearCache('codex-api-key');
        try {
          await providersApi.saveCodexConfigs(next);
          showNotification(t('ai_providers.quick_import_append_success'), 'success');
          setQuickImportOpen(false);
        } catch (err: unknown) {
          setCodexConfigs(previous);
          updateConfigValue('codex-api-key', previous);
          clearCache('codex-api-key');
          showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
        }
        return;
      }

      const previous = claudeConfigs;
      const next = [...previous, entry];
      setClaudeConfigs(next);
      updateConfigValue('claude-api-key', next);
      clearCache('claude-api-key');
      try {
        await providersApi.saveClaudeConfigs(next);
        showNotification(t('ai_providers.quick_import_append_success'), 'success');
        setQuickImportOpen(false);
      } catch (err: unknown) {
        setClaudeConfigs(previous);
        updateConfigValue('claude-api-key', previous);
        clearCache('claude-api-key');
        showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
      }
    },
    [claudeConfigs, clearCache, codexConfigs, showNotification, t, updateConfigValue]
  );

  const quickImportTestState: QuickImportTestState = {
    modelValue: quickImportModel,
    modelOptions: quickImportModelOptions,
    modelPlaceholder: quickImportDefaultModel
      ? t('ai_providers.provider_test_use_default_model', { model: quickImportDefaultModel })
      : t('ai_providers.quick_import_model_placeholder'),
    commandValue: quickImportCommand,
    commandPlaceholder: generatedQuickImportCommand,
    result: quickImportResult,
    status: quickImportStatus,
    message: quickImportMessage,
  };

  const enabledSummary = useMemo(() => {
    const providerLabel: Record<ProviderId, string> = {
      gemini: t('ai_providers.gemini_title'),
      codex: t('ai_providers.codex_title'),
      claude: t('ai_providers.claude_title'),
      vertex: t('ai_providers.vertex_title'),
      ampcode: t('ai_providers.ampcode_title'),
      openai: t('ai_providers.openai_title'),
    };

    const appendIfPresent = (
      meta: EnabledProviderSummaryItem['meta'],
      label: string,
      value: unknown
    ) => {
      if (value === undefined || value === null || value === '') return;
      meta.push({ label, value: typeof value === 'number' ? value : String(value) });
    };

    const buildProviderItem = (
      provider: 'gemini' | 'codex' | 'claude' | 'vertex',
      item: GeminiKeyConfig | ProviderKeyConfig,
      index: number
    ): EnabledProviderSummaryItem => {
      const meta: EnabledProviderSummaryItem['meta'] = [];
      appendIfPresent(meta, t('common.api_key'), item.apiKey);
      appendIfPresent(meta, t('common.priority'), item.priority);
      appendIfPresent(meta, t('common.prefix'), item.prefix);
      appendIfPresent(meta, t('common.base_url'), item.baseUrl);
      appendIfPresent(meta, t('common.proxy_url'), item.proxyUrl);
      appendIfPresent(meta, t('ai_providers.openai_models_count'), item.models?.length ?? 0);
      appendIfPresent(
        meta,
        t('common.custom_headers_label'),
        Object.keys(item.headers || {}).length
      );

      if (provider === 'codex') {
        const codexItem = item as ProviderKeyConfig;
        appendIfPresent(
          meta,
          t('ai_providers.codex_use_v1_label'),
          codexItem.useV1 !== false ? t('common.yes') : t('common.no')
        );
        appendIfPresent(
          meta,
          t('ai_providers.codex_websockets_label'),
          codexItem.websockets ? t('common.yes') : t('common.no')
        );
      }

      return {
        key: `${provider}:${getProviderConfigKey(item, index)}`,
        title: item.prefix?.trim() || `${providerLabel[provider]} #${index}`,
        path: `/ai-providers/${provider}/${index}`,
        meta,
      };
    };

    let items: EnabledProviderSummaryItem[] = [];
    let totalCount = 0;

    if (activeProviderId === 'gemini') {
      totalCount = geminiKeys.length;
      items = geminiKeys
        .map((item, index) =>
          hasDisableAllModelsRule(item.excludedModels)
            ? null
            : buildProviderItem('gemini', item, index)
        )
        .filter(Boolean) as EnabledProviderSummaryItem[];
    } else if (activeProviderId === 'codex') {
      totalCount = codexConfigs.length;
      items = codexConfigs
        .map((item, index) =>
          hasDisableAllModelsRule(item.excludedModels)
            ? null
            : buildProviderItem('codex', item, index)
        )
        .filter(Boolean) as EnabledProviderSummaryItem[];
    } else if (activeProviderId === 'claude') {
      totalCount = claudeConfigs.length;
      items = claudeConfigs
        .map((item, index) =>
          hasDisableAllModelsRule(item.excludedModels)
            ? null
            : buildProviderItem('claude', item, index)
        )
        .filter(Boolean) as EnabledProviderSummaryItem[];
    } else if (activeProviderId === 'vertex') {
      totalCount = vertexConfigs.length;
      items = vertexConfigs
        .map((item, index) =>
          hasDisableAllModelsRule(item.excludedModels)
            ? null
            : buildProviderItem('vertex', item, index)
        )
        .filter(Boolean) as EnabledProviderSummaryItem[];
    } else if (activeProviderId === 'openai') {
      totalCount = openaiProviders.length;
      items = openaiProviders.map((item, index) => {
        const meta: EnabledProviderSummaryItem['meta'] = [];
        appendIfPresent(
          meta,
          t('common.api_key'),
          item.apiKeyEntries.map((entry) => entry.apiKey).join(', ')
        );
        appendIfPresent(meta, t('common.priority'), item.priority);
        appendIfPresent(meta, t('common.prefix'), item.prefix);
        appendIfPresent(meta, t('common.base_url'), item.baseUrl);
        appendIfPresent(meta, t('ai_providers.openai_keys_count'), item.apiKeyEntries.length);
        appendIfPresent(meta, t('ai_providers.openai_models_count'), item.models?.length ?? 0);
        appendIfPresent(
          meta,
          t('common.custom_headers_label'),
          Object.keys(item.headers || {}).length
        );
        appendIfPresent(meta, t('ai_providers.openai_test_model_placeholder'), item.testModel);
        return {
          key: `openai:${item.name || index}`,
          title: item.prefix?.trim() || item.name || `${providerLabel.openai} #${index}`,
          path: `/ai-providers/openai/${index}`,
          meta,
        };
      });
    } else if (activeProviderId === 'ampcode') {
      const ampcode = config?.ampcode;
      const hasAmpcodeConfig =
        Boolean(ampcode?.upstreamUrl) ||
        Boolean(ampcode?.upstreamApiKey) ||
        Boolean(ampcode?.upstreamApiKeys?.length) ||
        Boolean(ampcode?.modelMappings?.length);
      totalCount = hasAmpcodeConfig ? 1 : 0;
      if (hasAmpcodeConfig) {
        const meta: EnabledProviderSummaryItem['meta'] = [];
        appendIfPresent(meta, t('ai_providers.ampcode_upstream_url_label'), ampcode?.upstreamUrl);
        appendIfPresent(
          meta,
          t('ai_providers.ampcode_upstream_api_key_label'),
          ampcode?.upstreamApiKey
        );
        appendIfPresent(
          meta,
          t('ai_providers.ampcode_force_model_mappings_label'),
          ampcode?.forceModelMappings ? t('common.yes') : t('common.no')
        );
        appendIfPresent(
          meta,
          t('ai_providers.ampcode_model_mappings_count'),
          ampcode?.modelMappings?.length ?? 0
        );
        appendIfPresent(
          meta,
          t('ai_providers.ampcode_upstream_api_keys_count'),
          ampcode?.upstreamApiKeys?.length ?? 0
        );
        items = [
          {
            key: 'ampcode',
            title: providerLabel.ampcode,
            path: '/ai-providers/ampcode',
            meta,
          },
        ];
      }
    }

    return {
      providerLabel: providerLabel[activeProviderId],
      items,
      totalCount,
    };
  }, [
    activeProviderId,
    claudeConfigs,
    codexConfigs,
    config?.ampcode,
    geminiKeys,
    openaiProviders,
    t,
    vertexConfigs,
  ]);

  return (
    <div className={styles.container}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('ai_providers.title')}</h1>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setQuickImportOpen(true)}
          disabled={disableControls}
        >
          {t('ai_providers.quick_import_open_action')}
        </Button>
      </div>
      <div className={styles.content}>
        {error && <div className="error-box">{error}</div>}

        <div className={styles.providersLayout}>
          <div className={styles.providersMain}>
            <section className={styles.globalCooldownSection}>
              <div className={styles.globalCooldownHeader}>
                <div className={styles.globalCooldownTitleGroup}>
                  <h2>{t('ai_providers.cooldown_global_title')}</h2>
                </div>
                <Button
                  size="sm"
                  onClick={() => void saveProviderCooldown()}
                  loading={savingProviderCooldown}
                  disabled={
                    disableControls ||
                    savingProviderCooldown ||
                    loading ||
                    !isProviderCooldownDirty
                  }
                >
                  {t('ai_providers.cooldown_global_save')}
                </Button>
              </div>
              <ProviderCooldownFields
                scope="global"
                value={providerCooldown}
                onChange={(value) => setProviderCooldown(withProviderCooldownDefaults(value))}
                disabled={disableControls || savingProviderCooldown || loading}
              />
            </section>

            <div id="provider-gemini" className={styles.providerSection}>
              <GeminiSection
                configs={geminiKeys}
                keyStats={keyStats}
                usage={usage}
                usageDetailsBySource={usageDetailsBySource}
                usageDetailsByAuthIndex={usageDetailsByAuthIndex}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                onAdd={() => openEditor('/ai-providers/gemini/new')}
                onEdit={(index) => openEditor(`/ai-providers/gemini/${index}`)}
                onDelete={deleteGemini}
                onToggle={(index, enabled) => void setConfigEnabled('gemini', index, enabled)}
              />
            </div>

            <div id="provider-codex" className={styles.providerSection}>
              <CodexSection
                configs={codexConfigs}
                keyStats={keyStats}
                usage={usage}
                usageDetailsBySource={usageDetailsBySource}
                usageDetailsByAuthIndex={usageDetailsByAuthIndex}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                testingKey={testingProviderKey}
                onAdd={() => openEditor('/ai-providers/codex/new')}
                onEdit={(index) => openEditor(`/ai-providers/codex/${index}`)}
                onDelete={(index) => void deleteProviderEntry('codex', index)}
                onToggle={(index, enabled) => void setConfigEnabled('codex', index, enabled)}
                onTest={(index) => void runProviderTest('codex', index)}
              />
            </div>

            <div id="provider-claude" className={styles.providerSection}>
              <ClaudeSection
                configs={claudeConfigs}
                keyStats={keyStats}
                usage={usage}
                usageDetailsBySource={usageDetailsBySource}
                usageDetailsByAuthIndex={usageDetailsByAuthIndex}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                testingKey={testingProviderKey}
                onAdd={() => openEditor('/ai-providers/claude/new')}
                onEdit={(index) => openEditor(`/ai-providers/claude/${index}`)}
                onDelete={(index) => void deleteProviderEntry('claude', index)}
                onToggle={(index, enabled) => void setConfigEnabled('claude', index, enabled)}
                onTest={(index) => void runProviderTest('claude', index)}
              />
            </div>

            <div id="provider-vertex" className={styles.providerSection}>
              <VertexSection
                configs={vertexConfigs}
                keyStats={keyStats}
                usage={usage}
                usageDetailsBySource={usageDetailsBySource}
                usageDetailsByAuthIndex={usageDetailsByAuthIndex}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                onAdd={() => openEditor('/ai-providers/vertex/new')}
                onEdit={(index) => openEditor(`/ai-providers/vertex/${index}`)}
                onDelete={deleteVertex}
                onToggle={(index, enabled) => void setConfigEnabled('vertex', index, enabled)}
              />
            </div>

            <div id="provider-ampcode" className={styles.providerSection}>
              <AmpcodeSection
                config={config?.ampcode}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                onEdit={() => openEditor('/ai-providers/ampcode')}
              />
            </div>

            <div id="provider-openai" className={styles.providerSection}>
              <OpenAISection
                configs={openaiProviders}
                keyStats={keyStats}
                usage={usage}
                usageDetailsBySource={usageDetailsBySource}
                usageDetailsByAuthIndex={usageDetailsByAuthIndex}
                loading={loading}
                disableControls={disableControls}
                isSwitching={isSwitching}
                resolvedTheme={resolvedTheme}
                onAdd={() => openEditor('/ai-providers/openai/new')}
                onEdit={(index) => openEditor(`/ai-providers/openai/${index}`)}
                onDelete={deleteOpenai}
              />
            </div>
          </div>

          <aside className={styles.enabledAside} aria-label={t('ai_providers.config_toggle_label')}>
            <div className={styles.enabledPanel} key={activeProviderId}>
              <div className={styles.enabledHeader}>
                <div>
                  <div className={styles.enabledEyebrow}>
                    {t('ai_providers.config_toggle_label')}
                  </div>
                  <h2>{enabledSummary.providerLabel}</h2>
                </div>
                <span className={styles.enabledCount}>
                  {enabledSummary.items.length}/{enabledSummary.totalCount}
                </span>
              </div>

              {enabledSummary.items.length ? (
                <div className={styles.enabledList}>
                  {enabledSummary.items.map((item) => (
                    <button
                      key={item.key}
                      type="button"
                      className={styles.enabledItem}
                      onClick={() => openEditor(item.path)}
                    >
                      <span className={styles.enabledItemTitle}>{item.title}</span>
                      <span className={styles.enabledItemMeta}>
                        {item.meta.map((entry) => (
                          <span
                            key={`${entry.label}:${entry.value}`}
                            className={styles.enabledMetaRow}
                          >
                            <span>{entry.label}</span>
                            <strong>{entry.value}</strong>
                          </span>
                        ))}
                      </span>
                    </button>
                  ))}
                </div>
              ) : (
                <div className={styles.enabledEmpty}>{t('ai_providers.enabled_summary_empty')}</div>
              )}
            </div>
          </aside>
        </div>

        <Modal
          open={quickImportOpen}
          onClose={() => setQuickImportOpen(false)}
          closeDisabled={quickImportStatus === 'loading'}
          width={960}
          title={t('ai_providers.quick_import_title')}
          footer={
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setQuickImportOpen(false)}
              disabled={quickImportStatus === 'loading'}
            >
              {t('common.close')}
            </Button>
          }
        >
          <AiProviderQuickImportPanel
            disabled={disableControls || quickImportStatus === 'loading'}
            provider={quickImportProvider}
            baseUrl={quickImportBaseUrl}
            apiKey={quickImportApiKey}
            testState={quickImportTestState}
            onProviderChange={(provider) => {
              setQuickImportProvider(provider);
              setQuickImportModel('');
              setQuickImportStatus('idle');
              setQuickImportMessage('');
              setQuickImportResult('');
            }}
            onBaseUrlChange={(value) => {
              setQuickImportBaseUrl(value);
              setQuickImportStatus('idle');
              setQuickImportMessage('');
            }}
            onApiKeyChange={(value) => {
              setQuickImportApiKey(value);
              setQuickImportStatus('idle');
              setQuickImportMessage('');
            }}
            onModelChange={(value) => {
              setQuickImportModel(value);
              setQuickImportStatus('idle');
              setQuickImportMessage('');
            }}
            onCommandChange={setQuickImportCommand}
            onFillDefaultCommand={() => {
              setQuickImportCommand(generatedQuickImportCommand);
              setQuickImportStatus('idle');
              setQuickImportMessage('');
              setQuickImportResult('');
            }}
            onRunEditedCommand={() => void executeQuickImportTest('edited')}
            onRunDefaultCommand={() => void executeQuickImportTest('generated')}
            onAppend={appendQuickImportProvider}
          />
        </Modal>

        <Modal
          open={Boolean(activeProviderTest)}
          onClose={closeProviderTestDialog}
          closeDisabled={providerTestStatus === 'loading'}
          width={960}
          title={
            activeProviderTest
              ? activeProviderTest.provider === 'codex'
                ? t('ai_providers.codex_test_title')
                : t('ai_providers.claude_test_title')
              : ''
          }
          footer={
            <Button
              variant="secondary"
              size="sm"
              onClick={closeProviderTestDialog}
              disabled={providerTestStatus === 'loading'}
            >
              {t('common.close')}
            </Button>
          }
        >
          {activeProviderTest ? (
            <ProviderTestPanel
              title={
                activeProviderTest.provider === 'codex'
                  ? t('ai_providers.codex_test_title')
                  : t('ai_providers.claude_test_title')
              }
              hint={
                providerTestDefaultModel && providerTestAvailableModels.length === 0
                  ? t('ai_providers.provider_test_default_model_hint', {
                      model: providerTestDefaultModel,
                    })
                  : activeProviderTest.provider === 'codex'
                    ? t('ai_providers.codex_test_hint')
                    : t('ai_providers.claude_test_hint')
              }
              modelValue={providerTestModel}
              modelOptions={providerTestModelOptions}
              modelPlaceholder={
                providerTestDefaultModel && providerTestAvailableModels.length === 0
                  ? t('ai_providers.provider_test_use_default_model', {
                      model: providerTestDefaultModel,
                    })
                  : providerTestAvailableModels.length
                    ? activeProviderTest.provider === 'codex'
                      ? t('ai_providers.codex_test_select_placeholder')
                      : t('ai_providers.claude_test_select_placeholder')
                    : activeProviderTest.provider === 'codex'
                      ? t('ai_providers.codex_test_select_empty')
                      : t('ai_providers.claude_test_select_empty')
              }
              modelAriaLabel={
                activeProviderTest.provider === 'codex'
                  ? t('ai_providers.codex_test_title')
                  : t('ai_providers.claude_test_title')
              }
              commandValue={providerTestCommand}
              commandPlaceholder={generatedProviderTestCommand}
              result={providerTestResult}
              status={providerTestStatus}
              message={providerTestMessage}
              disabled={disableControls || Boolean(testingProviderKey)}
              onModelChange={(value) => {
                setProviderTestModel(value);
                setProviderTestStatus('idle');
                setProviderTestMessage('');
              }}
              onCommandChange={setProviderTestCommand}
              onFillDefaultCommand={() => {
                setProviderTestCommand(generatedProviderTestCommand);
                setProviderTestStatus('idle');
                setProviderTestMessage('');
                setProviderTestResult('');
              }}
              onRunEditedCommand={() => void executeProviderTest('edited')}
              onRunDefaultCommand={() => void executeProviderTest('generated')}
              fillDefaultLabel={t('ai_providers.provider_test_fill_default_command')}
              runEditedLabel={t('ai_providers.provider_test_run_edited_command')}
              runDefaultLabel={
                activeProviderTest.provider === 'codex'
                  ? t('ai_providers.codex_test_action')
                  : t('ai_providers.claude_test_action')
              }
              commandLabel={t('ai_providers.test_details_command')}
              resultLabel={t('ai_providers.test_details_result')}
            />
          ) : null}
        </Modal>
      </div>

      <ProviderNav />
    </div>
  );
}
