import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { Modal } from '@/components/ui/Modal';
import { ProviderTestPanel } from '@/components/providers/ProviderTestPanel';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { apiCallApi, getApiCallErrorMessage, modelsApi, providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { ProviderCooldownConfig, ProviderKeyConfig } from '@/types';
import {
  buildHeaderObject,
  hasHeader,
  headersToEntries,
  normalizeHeaderEntries,
} from '@/utils/headers';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import { areProviderCooldownConfigsEqual, normalizeProviderCooldown } from '@/utils/providerCooldown';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import {
  buildApiCallCommandPreview,
  buildBackoffModeOptions,
  buildOpenAIResponsesEndpoint,
  excludedModelsTextForEnabledState,
  excludedModelsToText,
  formatApiCallResultPreview,
  hasDisableAllModelsRule,
  normalizeBackoffMode,
  normalizeRequestRetry,
  parseExcludedModels,
} from '@/components/providers/utils';
import {
  buildProviderTestModelOptions,
  buildProviderTestModelPlaceholder,
  resolveProviderTestModel,
  resolveProviderTestExecutionCommand,
  syncEditableTestCommand,
} from '@/components/providers/providerTest';
import { buildCodexProviderTestRequest } from '@/components/providers/providerTestRequest';
import { parseApiCallCommand } from '@/components/providers/testCommand';
import {
  isAiProviderEditModalState,
  requestAiProviderEditModalClose,
} from '@/utils/aiProviderEditModal';
import { ProviderCooldownFields, type ProviderFormState } from '@/components/providers';
import type { ModelInfo } from '@/utils/models';
import layoutStyles from './AiProvidersEditLayout.module.scss';
import styles from './AiProvidersPage.module.scss';

type LocationState = { fromAiProviders?: boolean; fromUsage?: boolean } | null;

const buildEmptyForm = (): ProviderFormState => ({
  apiKey: '',
  priority: 10,
  backoffMode: 'default',
  requestRetry: 2,
  cooldown: undefined,
  prefix: '',
  baseUrl: '',
  useV1: true,
  websockets: false,
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
});

const parseIndexParam = (value: string | undefined) => {
  if (!value) return null;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : null;
};

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

const CODEX_TEST_TIMEOUT_MS = 30_000;

const normalizeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && alias === name) {
      alias = '';
    }
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

type CodexFormBaseline = {
  apiKey: string;
  priority: number | null;
  backoffMode: 'default' | 'off' | 'custom';
  requestRetry: number | null;
  cooldown?: ProviderCooldownConfig;
  prefix: string;
  baseUrl: string;
  useV1: boolean;
  websockets: boolean;
  proxyUrl: string;
  headers: ReturnType<typeof normalizeHeaderEntries>;
  models: ReturnType<typeof normalizeModelEntries>;
  excludedModels: string[];
};

const buildCodexBaseline = (form: ProviderFormState): CodexFormBaseline => ({
  apiKey: String(form.apiKey ?? '').trim(),
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  backoffMode: normalizeBackoffMode(form.backoffMode),
  requestRetry:
    form.requestRetry !== undefined && Number.isFinite(form.requestRetry)
      ? Math.trunc(form.requestRetry)
      : null,
  cooldown: normalizeProviderCooldown(form.cooldown),
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  useV1: form.useV1 !== false,
  websockets: Boolean(form.websockets),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  headers: normalizeHeaderEntries(form.headers),
  models: normalizeModelEntries(form.modelEntries),
  excludedModels: parseExcludedModels(form.excludedText ?? ''),
});

export function AiProvidersCodexEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const params = useParams<{ index?: string }>();

  const { showNotification } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<ProviderFormState>(() => buildEmptyForm());
  const [baseline, setBaseline] = useState(() => buildCodexBaseline(buildEmptyForm()));

  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryEndpoint, setModelDiscoveryEndpoint] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());
  const autoFetchSignatureRef = useRef<string>('');
  const modelDiscoveryRequestIdRef = useRef(0);
  const [isTesting, setIsTesting] = useState(false);
  const [testModel, setTestModel] = useState('');
  const [testStatus, setTestStatus] = useState<'idle' | 'loading' | 'success' | 'error'>('idle');
  const [testMessage, setTestMessage] = useState('');
  const [testCommand, setTestCommand] = useState('');
  const [testResult, setTestResult] = useState('');
  const previousGeneratedTestCommandRef = useRef('');

  const hasIndexParam = typeof params.index === 'string';
  const editIndex = useMemo(() => parseIndexParam(params.index), [params.index]);
  const invalidIndexParam = hasIndexParam && editIndex === null;

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);

  const invalidIndex = editIndex !== null && !initialData;

  const title =
    editIndex !== null
      ? t('ai_providers.codex_edit_modal_title')
      : t('ai_providers.codex_add_modal_title');

  const handleBack = useCallback(() => {
    const state = location.state as LocationState;
    if (isAiProviderEditModalState(state)) {
      requestAiProviderEditModalClose();
      return;
    }
    if (state?.fromAiProviders || state?.fromUsage) {
      navigate(-1);
      return;
    }
    navigate('/ai-providers', { replace: true });
  }, [location.state, navigate]);

  const swipeRef = useEdgeSwipeBack({ onBack: handleBack });

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        handleBack();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleBack]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');

    Promise.all([
      fetchConfig('codex-api-key'),
      fetchConfig('default-test-models').catch(() => undefined),
    ])
      .then(([value]) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as ProviderKeyConfig[]) : []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = err instanceof Error ? err.message : '';
        setError(message || t('notification.refresh_failed'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [fetchConfig, t]);

  useEffect(() => {
    if (loading) return;

    if (initialData) {
      const nextForm: ProviderFormState = {
        ...initialData,
        priority: initialData.priority ?? 10,
        backoffMode: normalizeBackoffMode(initialData.backoffMode),
        requestRetry: normalizeRequestRetry(initialData.requestRetry),
        useV1: initialData.useV1 !== false,
        websockets: Boolean(initialData.websockets),
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(nextForm);
      setBaseline(buildCodexBaseline(nextForm));
      return;
    }
    const nextForm = buildEmptyForm();
    setForm(nextForm);
    setBaseline(buildCodexBaseline(nextForm));
  }, [initialData, loading]);

  const normalizedHeaders = useMemo(() => normalizeHeaderEntries(form.headers), [form.headers]);
  const normalizedModels = useMemo(
    () => normalizeModelEntries(form.modelEntries),
    [form.modelEntries]
  );
  const normalizedExcludedModels = useMemo(
    () => parseExcludedModels(form.excludedText ?? ''),
    [form.excludedText]
  );
  const isConfigEnabled = useMemo(
    () => !hasDisableAllModelsRule(normalizedExcludedModels),
    [normalizedExcludedModels]
  );
  const handleConfigEnabledChange = useCallback((enabled: boolean) => {
    setForm((prev) => ({
      ...prev,
      excludedText: excludedModelsTextForEnabledState(prev.excludedText ?? '', enabled),
    }));
  }, []);
  const normalizedPriority = useMemo(() => {
    return form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null;
  }, [form.priority]);
  const normalizedBackoffMode = useMemo(
    () => normalizeBackoffMode(form.backoffMode),
    [form.backoffMode]
  );
  const normalizedRequestRetry = useMemo(() => {
    return form.requestRetry !== undefined && Number.isFinite(form.requestRetry)
      ? Math.trunc(form.requestRetry)
      : null;
  }, [form.requestRetry]);
  const normalizedCooldown = useMemo(
    () => normalizeProviderCooldown(form.cooldown),
    [form.cooldown]
  );
  const isHeadersDirty = useMemo(
    () => !areKeyValueEntriesEqual(baseline.headers, normalizedHeaders),
    [baseline.headers, normalizedHeaders]
  );
  const isModelsDirty = useMemo(
    () => !areModelEntriesEqual(baseline.models, normalizedModels),
    [baseline.models, normalizedModels]
  );
  const isExcludedModelsDirty = useMemo(
    () => !areStringArraysEqual(baseline.excludedModels, normalizedExcludedModels),
    [baseline.excludedModels, normalizedExcludedModels]
  );
  const isDirty =
    baseline.apiKey !== form.apiKey.trim() ||
    baseline.priority !== normalizedPriority ||
    baseline.backoffMode !== normalizedBackoffMode ||
    baseline.requestRetry !== normalizedRequestRetry ||
    !areProviderCooldownConfigsEqual(baseline.cooldown, normalizedCooldown) ||
    baseline.prefix !== String(form.prefix ?? '').trim() ||
    baseline.baseUrl !== String(form.baseUrl ?? '').trim() ||
    baseline.useV1 !== (form.useV1 !== false) ||
    baseline.websockets !== Boolean(form.websockets) ||
    baseline.proxyUrl !== String(form.proxyUrl ?? '').trim() ||
    isHeadersDirty ||
    isModelsDirty ||
    isExcludedModelsDirty;
  const canGuard = !loading && !saving && !invalidIndexParam && !invalidIndex;

  const { allowNextNavigation } = useUnsavedChangesGuard({
    enabled: canGuard,
    shouldBlock: ({ currentLocation, nextLocation }) =>
      isDirty && currentLocation.pathname !== nextLocation.pathname,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });

  const canSave = !disableControls && !saving && !loading && !invalidIndexParam && !invalidIndex;
  const availableModels = useMemo(
    () => form.modelEntries.map((entry) => entry.name.trim()).filter(Boolean),
    [form.modelEntries]
  );
  const defaultTestModel = useMemo(
    () => String(config?.defaultTestModels?.codex ?? '').trim(),
    [config?.defaultTestModels?.codex]
  );
  const resolvedTestModel = useMemo(
    () => resolveProviderTestModel(testModel, defaultTestModel, availableModels),
    [availableModels, defaultTestModel, testModel]
  );
  const modelSelectOptions = useMemo(() => {
    return buildProviderTestModelOptions(form.modelEntries, defaultTestModel, {
      defaultModel: (model) =>
        t('ai_providers.provider_test_use_default_model', {
          model,
        }),
      firstAvailable: t('ai_providers.provider_test_use_first_model'),
    });
  }, [defaultTestModel, form.modelEntries, t]);
  const backoffModeOptions = useMemo(() => buildBackoffModeOptions(t), [t]);
  const generatedTestCommand = useMemo(() => {
    const request = buildCodexProviderTestRequest({
      apiKey: form.apiKey.trim() || '<api-key>',
      baseUrl: form.baseUrl ?? '',
      useV1: form.useV1 !== false,
      headers: buildHeaderObject(form.headers),
      model: resolvedTestModel || buildProviderTestModelPlaceholder(defaultTestModel),
      proxyUrl: form.proxyUrl?.trim() || undefined,
    });
    return buildApiCallCommandPreview(request);
  }, [
    defaultTestModel,
    form.apiKey,
    form.baseUrl,
    form.headers,
    form.proxyUrl,
    form.useV1,
    resolvedTestModel,
  ]);

  const discoveredModelsFiltered = useMemo(() => {
    const filter = modelDiscoverySearch.trim().toLowerCase();
    if (!filter) return discoveredModels;
    return discoveredModels.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const alias = (model.alias || '').toLowerCase();
      const description = (model.description || '').toLowerCase();
      return name.includes(filter) || alias.includes(filter) || description.includes(filter);
    });
  }, [discoveredModels, modelDiscoverySearch]);
  const visibleDiscoveredModelNames = useMemo(
    () => discoveredModelsFiltered.map((model) => model.name),
    [discoveredModelsFiltered]
  );
  const allVisibleDiscoveredSelected = useMemo(
    () =>
      visibleDiscoveredModelNames.length > 0 &&
      visibleDiscoveredModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleDiscoveredModelNames]
  );

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;

      let addedCount = 0;
      setForm((prev) => {
        const mergedMap = new Map<string, { name: string; alias: string }>();
        prev.modelEntries.forEach((entry) => {
          const name = entry.name.trim();
          if (!name) return;
          mergedMap.set(name.toLowerCase(), { name, alias: entry.alias?.trim() || '' });
        });

        selectedModels.forEach((model) => {
          const name = String(model.name ?? '').trim();
          if (!name) return;
          const key = name.toLowerCase();
          if (mergedMap.has(key)) return;
          mergedMap.set(key, { name, alias: model.alias ?? '' });
          addedCount += 1;
        });

        const mergedEntries = Array.from(mergedMap.values());
        return {
          ...prev,
          modelEntries: mergedEntries.length ? mergedEntries : [{ name: '', alias: '' }],
        };
      });

      if (addedCount > 0) {
        showNotification(
          t('ai_providers.codex_models_fetch_added', { count: addedCount }),
          'success'
        );
      }
    },
    [setForm, showNotification, t]
  );

  const fetchCodexModelDiscovery = useCallback(async () => {
    const requestId = (modelDiscoveryRequestIdRef.current += 1);
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');

    try {
      const headerObject = buildHeaderObject(form.headers);
      const hasCustomAuthorization = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'authorization'
      );
      const apiKey = form.apiKey.trim() || undefined;
      const list = await modelsApi.fetchV1ModelsViaApiCall(
        form.baseUrl ?? '',
        hasCustomAuthorization ? undefined : apiKey,
        headerObject
      );
      if (modelDiscoveryRequestIdRef.current !== requestId) return;
      setDiscoveredModels(list);
    } catch (err: unknown) {
      if (modelDiscoveryRequestIdRef.current !== requestId) return;
      setDiscoveredModels([]);
      const message = getErrorMessage(err);
      setModelDiscoveryError(`${t('ai_providers.codex_models_fetch_error')}: ${message}`);
    } finally {
      if (modelDiscoveryRequestIdRef.current === requestId) {
        setModelDiscoveryFetching(false);
      }
    }
  }, [form.apiKey, form.baseUrl, form.headers, t]);

  useEffect(() => {
    if (!modelDiscoveryOpen) {
      autoFetchSignatureRef.current = '';
      modelDiscoveryRequestIdRef.current += 1;
      setModelDiscoveryFetching(false);
      return;
    }

    const nextEndpoint = modelsApi.buildV1ModelsEndpoint(form.baseUrl ?? '');
    setModelDiscoveryEndpoint(nextEndpoint);
    setDiscoveredModels([]);
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');

    if (!nextEndpoint) return;

    const headerObject = buildHeaderObject(form.headers);
    const hasCustomAuthorization = Object.keys(headerObject).some(
      (key) => key.toLowerCase() === 'authorization'
    );
    const hasApiKeyField = Boolean(form.apiKey.trim());
    const canAutoFetch = hasApiKeyField || hasCustomAuthorization;

    if (!canAutoFetch) return;

    const headerSignature = Object.entries(headerObject)
      .sort(([a], [b]) => a.toLowerCase().localeCompare(b.toLowerCase()))
      .map(([key, value]) => `${key}:${value}`)
      .join('|');
    const signature = `${nextEndpoint}||${form.apiKey.trim()}||${headerSignature}`;
    if (autoFetchSignatureRef.current === signature) return;
    autoFetchSignatureRef.current = signature;

    void fetchCodexModelDiscovery();
  }, [fetchCodexModelDiscovery, form.apiKey, form.baseUrl, form.headers, modelDiscoveryOpen]);

  useEffect(() => {
    const availableNames = new Set(discoveredModels.map((model) => model.name));
    setModelDiscoverySelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((name) => {
        if (availableNames.has(name)) {
          next.add(name);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [discoveredModels]);

  const toggleModelDiscoverySelection = (name: string) => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });
  };

  const handleSelectVisibleDiscoveredModels = useCallback(() => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      visibleDiscoveredModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleDiscoveredModelNames]);

  const handleClearDiscoveredModelSelection = useCallback(() => {
    setModelDiscoverySelected(new Set());
  }, []);

  const handleApplyDiscoveredModels = () => {
    const selectedModels = discoveredModels.filter((model) =>
      modelDiscoverySelected.has(model.name)
    );
    if (selectedModels.length) {
      mergeDiscoveredModels(selectedModels);
    }
    setModelDiscoveryOpen(false);
  };

  const handleSave = useCallback(async () => {
    if (!canSave) return;

    const trimmedBaseUrl = (form.baseUrl ?? '').trim();
    const baseUrl = trimmedBaseUrl || undefined;
    if (!baseUrl) {
      showNotification(t('notification.codex_base_url_required'), 'error');
      return;
    }

    setSaving(true);
    setError('');
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        backoffMode: normalizedBackoffMode,
        requestRetry:
          normalizedBackoffMode === 'custom' ? normalizeRequestRetry(form.requestRetry) : undefined,
        cooldown: normalizedCooldown,
        prefix: form.prefix?.trim() || undefined,
        baseUrl,
        useV1: form.useV1 !== false,
        websockets: Boolean(form.websockets),
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: entriesToModels(form.modelEntries),
        excludedModels: parseExcludedModels(form.excludedText),
      };

      const nextList =
        editIndex !== null
          ? configs.map((item, idx) => (idx === editIndex ? payload : item))
          : [...configs, payload];

      await providersApi.saveCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
      showNotification(
        editIndex !== null
          ? t('notification.codex_config_updated')
          : t('notification.codex_config_added'),
        'success'
      );
      allowNextNavigation();
      setBaseline(buildCodexBaseline(form));
      handleBack();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    allowNextNavigation,
    canSave,
    clearCache,
    configs,
    editIndex,
    form,
    handleBack,
    normalizedBackoffMode,
    normalizedCooldown,
    showNotification,
    t,
    updateConfigValue,
  ]);

  const canOpenModelDiscovery =
    !disableControls &&
    !saving &&
    !loading &&
    !invalidIndexParam &&
    !invalidIndex &&
    Boolean((form.baseUrl ?? '').trim());
  const canApplyModelDiscovery =
    !disableControls && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;
  const connectivityConfigSignature = useMemo(() => {
    const headersSignature = form.headers
      .map((entry) => `${entry.key.trim()}:${entry.value.trim()}`)
      .join('|');
    const modelsSignature = form.modelEntries
      .map((entry) => `${entry.name.trim()}:${entry.alias.trim()}`)
      .join('|');
    return [
      form.apiKey.trim(),
      form.baseUrl?.trim() ?? '',
      form.useV1 !== false ? 'v1' : 'no-v1',
      testModel.trim(),
      headersSignature,
      modelsSignature,
    ].join('||');
  }, [form.apiKey, form.baseUrl, form.headers, form.modelEntries, form.useV1, testModel]);

  useEffect(() => {
    if (!testModel) return;
    if (!availableModels.includes(testModel)) {
      setTestModel('');
    }
  }, [availableModels, testModel]);

  useEffect(() => {
    setTestCommand((current) => {
      const next = syncEditableTestCommand(
        current,
        previousGeneratedTestCommandRef.current,
        generatedTestCommand
      );
      previousGeneratedTestCommandRef.current = generatedTestCommand;
      return next;
    });
  }, [generatedTestCommand]);

  useEffect(() => {
    setTestStatus('idle');
    setTestMessage('');
    setTestResult('');
  }, [connectivityConfigSignature]);

  const runCodexConnectivityTest = useCallback(
    async (mode: 'generated' | 'edited') => {
      if (isTesting) return;
      const activeCommand = resolveProviderTestExecutionCommand(
        testCommand,
        generatedTestCommand,
        mode
      );
      const usingGeneratedCommand = mode === 'generated';
      let requestPayload;

      if (usingGeneratedCommand) {
        const modelName = resolveProviderTestModel(testModel, defaultTestModel, availableModels);
        if (!modelName) {
          const message = t('ai_providers.codex_test_model_required');
          setTestStatus('error');
          setTestMessage(message);
          showNotification(message, 'error');
          return;
        }

        const endpoint = buildOpenAIResponsesEndpoint(form.baseUrl ?? '', form.useV1 !== false);
        if (!endpoint) {
          const message = t('ai_providers.codex_test_endpoint_invalid');
          setTestStatus('error');
          setTestMessage(message);
          showNotification(message, 'error');
          return;
        }

        const headers = {
          'Content-Type': 'application/json',
          ...buildHeaderObject(form.headers),
        } as Record<string, string>;
        const apiKey = form.apiKey.trim();
        if (!apiKey && !hasHeader(headers, 'authorization')) {
          const message = t('ai_providers.codex_test_key_required');
          setTestStatus('error');
          setTestMessage(message);
          showNotification(message, 'error');
          return;
        }
        if (apiKey && !hasHeader(headers, 'authorization')) {
          headers.Authorization = `Bearer ${apiKey}`;
        }

        requestPayload = buildCodexProviderTestRequest({
          apiKey,
          baseUrl: endpoint,
          headers,
          model: modelName,
          proxyUrl: form.proxyUrl?.trim() || undefined,
          useV1: form.useV1 !== false,
        });
      } else {
        if (!activeCommand) {
          const message = t('ai_providers.provider_test_command_invalid');
          setTestStatus('error');
          setTestMessage(message);
          showNotification(message, 'error');
          return;
        }
        try {
          requestPayload = {
            ...parseApiCallCommand(activeCommand),
            proxyUrl: form.proxyUrl?.trim() || undefined,
          };
        } catch {
          const message = t('ai_providers.provider_test_command_invalid');
          setTestStatus('error');
          setTestMessage(message);
          showNotification(message, 'error');
          return;
        }
      }

      setIsTesting(true);
      setTestStatus('loading');
      setTestMessage(t('ai_providers.codex_test_running'));
      setTestCommand(usingGeneratedCommand ? generatedTestCommand : activeCommand);
      setTestResult('');
      let resultPreview = '';

      try {
        const result = await apiCallApi.request(requestPayload, {
          timeout: CODEX_TEST_TIMEOUT_MS,
        });
        resultPreview = formatApiCallResultPreview(result);
        setTestResult(resultPreview);

        if (result.statusCode < 200 || result.statusCode >= 300) {
          throw new Error(getApiCallErrorMessage(result));
        }

        const message = t('ai_providers.codex_test_success');
        setTestStatus('success');
        setTestMessage(message);
        showNotification(message, 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        const errorCode =
          typeof err === 'object' && err !== null && 'code' in err
            ? String((err as { code?: string }).code)
            : '';
        const isTimeout = errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
        if (!resultPreview) {
          setTestResult(message || t('common.unknown_error'));
        }
        const resolvedMessage = isTimeout
          ? t('ai_providers.codex_test_timeout', { seconds: CODEX_TEST_TIMEOUT_MS / 1000 })
          : `${t('ai_providers.codex_test_failed')}: ${message || t('common.unknown_error')}`;
        setTestStatus('error');
        setTestMessage(resolvedMessage);
        showNotification(resolvedMessage, 'error');
      } finally {
        setIsTesting(false);
      }
    },
    [
      availableModels,
      defaultTestModel,
      form.apiKey,
      form.baseUrl,
      form.headers,
      form.useV1,
      generatedTestCommand,
      isTesting,
      showNotification,
      t,
      testCommand,
      testModel,
      form.proxyUrl,
    ]
  );

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      hideTopBarBackButton
      hideTopBarRightAction
      floatingAction={
        <div className={layoutStyles.floatingActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleBack}
            className={layoutStyles.floatingBackButton}
          >
            {t('common.back')}
          </Button>
          <Button
            size="sm"
            onClick={handleSave}
            loading={saving}
            disabled={!canSave}
            className={layoutStyles.floatingSaveButton}
          >
            {t('common.save')}
          </Button>
        </div>
      }
      isLoading={loading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        {error && <div className="error-box">{error}</div>}
        {invalidIndexParam || invalidIndex ? (
          <div className="hint">{t('common.invalid_provider_index')}</div>
        ) : (
          <>
            <Input
              label={t('ai_providers.codex_add_modal_key_label')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disableControls || saving}
            />
            <div className="form-group">
              <label>{t('ai_providers.config_toggle_label')}</label>
              <ToggleSwitch
                checked={isConfigEnabled}
                onChange={handleConfigEnabledChange}
                disabled={disableControls || saving || isTesting}
                ariaLabel={t('ai_providers.config_toggle_label')}
              />
              <div className="hint">{t('ai_providers.config_enabled_hint')}</div>
            </div>
            <Input
              label={t('ai_providers.priority_label')}
              hint={t('ai_providers.priority_hint')}
              type="number"
              step={1}
              value={form.priority ?? ''}
              onChange={(e) => {
                const raw = e.target.value;
                const parsed = raw.trim() === '' ? undefined : Number(raw);
                setForm((prev) => ({
                  ...prev,
                  priority: parsed !== undefined && Number.isFinite(parsed) ? parsed : undefined,
                }));
              }}
              disabled={disableControls || saving || isTesting}
            />
            <div className="form-group">
              <label>{t('ai_providers.backoff_mode_label')}</label>
              <Select
                value={form.backoffMode ?? 'default'}
                options={backoffModeOptions}
                onChange={(value) =>
                  setForm((prev) => ({
                    ...prev,
                    backoffMode: normalizeBackoffMode(value),
                    requestRetry:
                      normalizeBackoffMode(value) === 'custom'
                        ? normalizeRequestRetry(prev.requestRetry)
                        : prev.requestRetry,
                  }))
                }
                ariaLabel={t('ai_providers.backoff_mode_label')}
                disabled={disableControls || saving || isTesting}
              />
              <div className="hint">{t('ai_providers.backoff_mode_hint')}</div>
            </div>
            {normalizeBackoffMode(form.backoffMode) === 'custom' && (
              <Input
                label={t('ai_providers.request_retry_label')}
                hint={t('ai_providers.request_retry_hint')}
                type="number"
                min={0}
                step={1}
                value={form.requestRetry ?? 2}
                onChange={(e) => {
                  const raw = e.target.value;
                  const parsed = raw.trim() === '' ? 2 : Number(raw);
                  setForm((prev) => ({
                    ...prev,
                    requestRetry: Number.isFinite(parsed) ? parsed : 2,
                  }));
                }}
                disabled={disableControls || saving || isTesting}
              />
            )}
            <ProviderCooldownFields
              value={form.cooldown}
              onChange={(cooldown) => setForm((prev) => ({ ...prev, cooldown }))}
              disabled={disableControls || saving || isTesting}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={disableControls || saving || isTesting}
            />
            <Input
              label={t('ai_providers.codex_add_modal_url_label')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disableControls || saving || isTesting}
            />
            <div className="form-group">
              <label>{t('ai_providers.codex_use_v1_label')}</label>
              <ToggleSwitch
                checked={form.useV1 !== false}
                onChange={(value) => setForm((prev) => ({ ...prev, useV1: value }))}
                disabled={disableControls || saving || isTesting}
                ariaLabel={t('ai_providers.codex_use_v1_label')}
              />
              <div className="hint">{t('ai_providers.codex_use_v1_hint')}</div>
            </div>
            <div className="form-group">
              <label>{t('ai_providers.codex_websockets_label')}</label>
              <ToggleSwitch
                checked={Boolean(form.websockets)}
                onChange={(value) => setForm((prev) => ({ ...prev, websockets: value }))}
                disabled={disableControls || saving || isTesting}
                ariaLabel={t('ai_providers.codex_websockets_label')}
              />
              <div className="hint">{t('ai_providers.codex_websockets_hint')}</div>
            </div>
            <Input
              label={t('ai_providers.codex_add_modal_proxy_label')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={disableControls || saving || isTesting}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={disableControls || saving || isTesting}
            />

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {t('ai_providers.codex_models_label')}
                </label>
                <div className={styles.modelConfigToolbar}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() =>
                      setForm((prev) => ({
                        ...prev,
                        modelEntries: [...prev.modelEntries, { name: '', alias: '' }],
                      }))
                    }
                    disabled={disableControls || saving || isTesting}
                  >
                    {t('ai_providers.codex_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(true)}
                    disabled={!canOpenModelDiscovery || isTesting}
                  >
                    {t('ai_providers.codex_models_fetch_button')}
                  </Button>
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.codex_models_hint')}</div>

              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={disableControls || saving || isTesting}
                hideAddButton
                className={styles.modelInputList}
                rowClassName={styles.modelInputRow}
                inputClassName={styles.modelInputField}
                removeButtonClassName={styles.modelRowRemoveButton}
                removeButtonTitle={t('common.delete')}
                removeButtonAriaLabel={t('common.delete')}
              />

              <ProviderTestPanel
                title={t('ai_providers.codex_test_title')}
                hint={
                  defaultTestModel && availableModels.length === 0
                    ? t('ai_providers.provider_test_default_model_hint', {
                        model: defaultTestModel,
                      })
                    : t('ai_providers.codex_test_hint')
                }
                modelValue={testModel}
                modelOptions={modelSelectOptions}
                modelPlaceholder={
                  defaultTestModel && availableModels.length === 0
                    ? t('ai_providers.provider_test_use_default_model', {
                        model: defaultTestModel,
                      })
                    : availableModels.length
                      ? t('ai_providers.codex_test_select_placeholder')
                      : t('ai_providers.codex_test_select_empty')
                }
                modelAriaLabel={t('ai_providers.codex_test_title')}
                commandValue={testCommand}
                commandPlaceholder={generatedTestCommand}
                result={testResult}
                status={testStatus}
                message={testMessage}
                disabled={saving || disableControls || isTesting}
                onModelChange={(value) => {
                  setTestModel(value);
                  setTestStatus('idle');
                  setTestMessage('');
                }}
                onCommandChange={setTestCommand}
                onFillDefaultCommand={() => {
                  setTestCommand(generatedTestCommand);
                  setTestStatus('idle');
                  setTestMessage('');
                  setTestResult('');
                }}
                onRunEditedCommand={() => void runCodexConnectivityTest('edited')}
                onRunDefaultCommand={() => void runCodexConnectivityTest('generated')}
                fillDefaultLabel={t('ai_providers.provider_test_fill_default_command')}
                runEditedLabel={t('ai_providers.provider_test_run_edited_command')}
                runDefaultLabel={t('ai_providers.codex_test_action')}
                commandLabel={t('ai_providers.test_details_command')}
                resultLabel={t('ai_providers.test_details_result')}
              />
            </div>
            <div className="form-group">
              <label>{t('ai_providers.excluded_models_label')}</label>
              <textarea
                className="input"
                placeholder={t('ai_providers.excluded_models_placeholder')}
                value={form.excludedText}
                onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
                rows={4}
                disabled={disableControls || saving || isTesting}
              />
              <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
            </div>

            <Modal
              open={modelDiscoveryOpen}
              title={t('ai_providers.codex_models_fetch_title')}
              onClose={() => setModelDiscoveryOpen(false)}
              width={720}
              footer={
                <>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(false)}
                    disabled={modelDiscoveryFetching}
                  >
                    {t('common.cancel')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={handleApplyDiscoveredModels}
                    disabled={!canApplyModelDiscovery}
                  >
                    {t('ai_providers.codex_models_fetch_apply')}
                  </Button>
                </>
              }
            >
              <div className={styles.openaiModelsContent}>
                <div className={styles.sectionHint}>
                  {t('ai_providers.codex_models_fetch_hint')}
                </div>
                <div className={styles.openaiModelsEndpointSection}>
                  <label className={styles.openaiModelsEndpointLabel}>
                    {t('ai_providers.codex_models_fetch_url_label')}
                  </label>
                  <div className={styles.openaiModelsEndpointControls}>
                    <input
                      className={`input ${styles.openaiModelsEndpointInput}`}
                      readOnly
                      value={modelDiscoveryEndpoint}
                    />
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => void fetchCodexModelDiscovery()}
                      loading={modelDiscoveryFetching}
                      disabled={disableControls || saving}
                    >
                      {t('ai_providers.codex_models_fetch_refresh')}
                    </Button>
                  </div>
                </div>
                <Input
                  label={t('ai_providers.codex_models_search_label')}
                  placeholder={t('ai_providers.codex_models_search_placeholder')}
                  value={modelDiscoverySearch}
                  onChange={(e) => setModelDiscoverySearch(e.target.value)}
                  disabled={modelDiscoveryFetching}
                />
                {discoveredModels.length > 0 && (
                  <div className={styles.modelDiscoveryToolbar}>
                    <div className={styles.modelDiscoveryToolbarActions}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={handleSelectVisibleDiscoveredModels}
                        disabled={
                          disableControls ||
                          saving ||
                          modelDiscoveryFetching ||
                          discoveredModelsFiltered.length === 0 ||
                          allVisibleDiscoveredSelected
                        }
                      >
                        {t('ai_providers.model_discovery_select_visible')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={handleClearDiscoveredModelSelection}
                        disabled={
                          disableControls ||
                          saving ||
                          modelDiscoveryFetching ||
                          modelDiscoverySelected.size === 0
                        }
                      >
                        {t('ai_providers.model_discovery_clear_selection')}
                      </Button>
                    </div>
                    <div className={styles.modelDiscoverySelectionSummary}>
                      {t('ai_providers.model_discovery_selected_count', {
                        count: modelDiscoverySelected.size,
                      })}
                    </div>
                  </div>
                )}
                {modelDiscoveryError && <div className="error-box">{modelDiscoveryError}</div>}
                {modelDiscoveryFetching ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.codex_models_fetch_loading')}
                  </div>
                ) : discoveredModels.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.codex_models_fetch_empty')}
                  </div>
                ) : discoveredModelsFiltered.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.codex_models_search_empty')}
                  </div>
                ) : (
                  <div className={styles.modelDiscoveryList}>
                    {discoveredModelsFiltered.map((model) => {
                      const checked = modelDiscoverySelected.has(model.name);
                      return (
                        <SelectionCheckbox
                          key={model.name}
                          checked={checked}
                          onChange={() => toggleModelDiscoverySelection(model.name)}
                          disabled={disableControls || saving || modelDiscoveryFetching}
                          ariaLabel={model.name}
                          className={`${styles.modelDiscoveryRow} ${
                            checked ? styles.modelDiscoveryRowSelected : ''
                          }`}
                          labelClassName={styles.modelDiscoverySelectionLabel}
                          label={
                            <div className={styles.modelDiscoveryMeta}>
                              <div className={styles.modelDiscoveryName}>
                                {model.name}
                                {model.alias && (
                                  <span className={styles.modelDiscoveryAlias}>{model.alias}</span>
                                )}
                              </div>
                              {model.description && (
                                <div className={styles.modelDiscoveryDesc}>{model.description}</div>
                              )}
                            </div>
                          }
                        />
                      );
                    })}
                  </div>
                )}
              </div>
            </Modal>
          </>
        )}
      </Card>
    </SecondaryScreenShell>
  );
}
