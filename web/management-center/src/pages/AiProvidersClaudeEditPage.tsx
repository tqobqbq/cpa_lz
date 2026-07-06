import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useOutletContext } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { ProviderCooldownFields } from '@/components/providers';
import { ProviderTestPanel } from '@/components/providers/ProviderTestPanel';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import { buildHeaderObject } from '@/utils/headers';
import {
  buildProviderTestModelOptions,
  buildProviderTestModelPlaceholder,
  resolveProviderTestModel,
  resolveProviderTestExecutionCommand,
  syncEditableTestCommand,
} from '@/components/providers/providerTest';
import { buildClaudeProviderTestRequest } from '@/components/providers/providerTestRequest';
import { parseApiCallCommand } from '@/components/providers/testCommand';
import {
  isAiProviderEditModalState,
  requestAiProviderEditModalNavigate,
} from '@/utils/aiProviderEditModal';
import {
  buildApiCallCommandPreview,
  buildBackoffModeOptions,
  buildClaudeMessagesEndpoint,
  excludedModelsTextForEnabledState,
  formatApiCallResultPreview,
  hasDisableAllModelsRule,
  normalizeBackoffMode,
  normalizeRequestRetry,
  parseExcludedModels,
  parseTextList,
} from '@/components/providers/utils';
import type { ClaudeEditOutletContext } from './AiProvidersClaudeEditLayout';
import styles from './AiProvidersPage.module.scss';
import layoutStyles from './AiProvidersEditLayout.module.scss';

const CLAUDE_TEST_TIMEOUT_MS = 30_000;

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
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

export function AiProvidersClaudeEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { showNotification } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const {
    hasIndexParam,
    invalidIndexParam,
    invalidIndex,
    disableControls,
    loading,
    saving,
    form,
    setForm,
    testModel,
    setTestModel,
    testStatus,
    setTestStatus,
    testMessage,
    setTestMessage,
    availableModels,
    handleBack,
    handleSave,
    editorRootPath,
  } = useOutletContext<ClaudeEditOutletContext>();
  const location = useLocation();

  const title = hasIndexParam
    ? t('ai_providers.claude_edit_modal_title')
    : t('ai_providers.claude_add_modal_title');

  const swipeRef = useEdgeSwipeBack({ onBack: handleBack });
  const [isTesting, setIsTesting] = useState(false);
  const [testCommand, setTestCommand] = useState('');
  const [testResult, setTestResult] = useState('');
  const previousGeneratedTestCommandRef = useRef('');
  const lastCloakConfigRef = useRef<typeof form.cloak>(null);
  const backoffModeOptions = useMemo(() => buildBackoffModeOptions(t), [t]);
  const authModeOptions = useMemo(
    () => [
      { value: 'auto', label: t('ai_providers.claude_auth_mode_auto') },
      { value: 'api-key', label: t('ai_providers.claude_auth_mode_api_key') },
      { value: 'bearer', label: t('ai_providers.claude_auth_mode_bearer') },
    ],
    [t]
  );

  useEffect(() => {
    void fetchConfig('default-test-models').catch(() => {});
  }, [fetchConfig]);

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
    if (!form.cloak) return;
    lastCloakConfigRef.current = form.cloak;
  }, [form.cloak]);

  const canSave =
    !disableControls && !loading && !saving && !invalidIndexParam && !invalidIndex && !isTesting;
  const normalizedExcludedModels = useMemo(
    () => parseExcludedModels(form.excludedText ?? ''),
    [form.excludedText]
  );
  const isConfigEnabled = useMemo(
    () => !hasDisableAllModelsRule(normalizedExcludedModels),
    [normalizedExcludedModels]
  );
  const handleConfigEnabledChange = useCallback(
    (enabled: boolean) => {
      setForm((prev) => ({
        ...prev,
        excludedText: excludedModelsTextForEnabledState(prev.excludedText ?? '', enabled),
      }));
    },
    [setForm]
  );
  const defaultTestModel = useMemo(
    () => String(config?.defaultTestModels?.claude ?? '').trim(),
    [config?.defaultTestModels?.claude]
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

  const cloakModeOptions = useMemo(
    () => [
      { value: 'auto', label: t('ai_providers.claude_cloak_mode_auto') },
      { value: 'always', label: t('ai_providers.claude_cloak_mode_always') },
      { value: 'never', label: t('ai_providers.claude_cloak_mode_never') },
    ],
    [t]
  );

  const resolvedCloakMode = useMemo(() => {
    const mode = (form.cloak?.mode ?? '').trim().toLowerCase();
    if (!mode) return 'auto';
    if (mode === 'provider') return 'auto';
    if (mode === 'auto' || mode === 'always' || mode === 'never') return mode;
    return 'auto';
  }, [form.cloak?.mode]);
  const generatedTestCommand = useMemo(() => {
    const request = buildClaudeProviderTestRequest({
      apiKey: form.apiKey.trim() || '<api-key>',
      baseUrl: form.baseUrl ?? '',
      headers: buildHeaderObject(form.headers),
      authMode: form.authMode ?? 'auto',
      model: resolvedTestModel || buildProviderTestModelPlaceholder(defaultTestModel),
      proxyUrl: form.proxyUrl?.trim() || undefined,
    });
    return buildApiCallCommandPreview(request);
  }, [defaultTestModel, form.apiKey, form.authMode, form.baseUrl, form.headers, form.proxyUrl, resolvedTestModel]);

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
      form.authMode ?? 'auto',
      testModel.trim(),
      headersSignature,
      modelsSignature,
    ].join('||');
  }, [form.apiKey, form.authMode, form.baseUrl, form.headers, form.modelEntries, testModel]);

  useEffect(() => {
    setTestStatus('idle');
    setTestMessage('');
    setTestResult('');
  }, [connectivityConfigSignature, setTestMessage, setTestStatus]);

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

  const openClaudeModelDiscovery = () => {
    if (isAiProviderEditModalState(location.state)) {
      requestAiProviderEditModalNavigate(`${editorRootPath}/models`);
      return;
    }
    navigate('models');
  };

  const runClaudeConnectivityTest = useCallback(async (mode: 'generated' | 'edited') => {
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
        const message = t('ai_providers.claude_test_model_required');
        setTestStatus('error');
        setTestMessage(message);
        showNotification(message, 'error');
        return;
      }

      const customHeaders = buildHeaderObject(form.headers);
      const apiKey = form.apiKey.trim();
      const authMode = form.authMode ?? 'auto';
      const hasApiKeyHeader = hasHeader(customHeaders, 'x-api-key');
      const hasAuthorizationHeader = hasHeader(customHeaders, 'authorization');
      const apiKeyFromAuthorization = resolveBearerTokenFromAuthorization(customHeaders);
      const resolvedApiKey = apiKey || apiKeyFromAuthorization;

      if (!resolvedApiKey && !hasApiKeyHeader && !hasAuthorizationHeader) {
        const message = t('ai_providers.claude_test_key_required');
        setTestStatus('error');
        setTestMessage(message);
        showNotification(message, 'error');
        return;
      }

      const endpoint = buildClaudeMessagesEndpoint(form.baseUrl ?? '');
      if (!endpoint) {
        const message = t('ai_providers.claude_test_endpoint_invalid');
        setTestStatus('error');
        setTestMessage(message);
        showNotification(message, 'error');
        return;
      }

      requestPayload = buildClaudeProviderTestRequest({
        apiKey: resolvedApiKey,
        authMode,
        baseUrl: endpoint,
        headers: customHeaders,
        model: modelName,
        proxyUrl: form.proxyUrl?.trim() || undefined,
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
    setTestMessage(t('ai_providers.claude_test_running'));
    setTestCommand(usingGeneratedCommand ? generatedTestCommand : activeCommand);
    setTestResult('');
    let resultPreview = '';

    try {
      const result = await apiCallApi.request(requestPayload, {
        timeout: CLAUDE_TEST_TIMEOUT_MS,
      });
      resultPreview = formatApiCallResultPreview(result);
      setTestResult(resultPreview);

      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }

      const message = t('ai_providers.claude_test_success');
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
        ? t('ai_providers.claude_test_timeout', { seconds: CLAUDE_TEST_TIMEOUT_MS / 1000 })
        : `${t('ai_providers.claude_test_failed')}: ${message || t('common.unknown_error')}`;
      setTestStatus('error');
      setTestMessage(resolvedMessage);
      showNotification(resolvedMessage, 'error');
    } finally {
      setIsTesting(false);
    }
  }, [
    availableModels,
    defaultTestModel,
    form.apiKey,
    form.authMode,
    form.baseUrl,
    form.headers,
    generatedTestCommand,
    isTesting,
    setTestMessage,
    setTestStatus,
    showNotification,
    t,
    testCommand,
    testModel,
    form.proxyUrl,
  ]);

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
            onClick={() => void handleSave()}
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
        {invalidIndexParam || invalidIndex ? (
          <div className={styles.sectionHint}>{t('common.invalid_provider_index')}</div>
        ) : (
          <div className={styles.openaiEditForm}>
            <Input
              label={t('ai_providers.claude_add_modal_key_label')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={saving || disableControls || isTesting}
            />
            <div className="form-group">
              <label>{t('ai_providers.config_toggle_label')}</label>
              <ToggleSwitch
                checked={isConfigEnabled}
                onChange={handleConfigEnabledChange}
                disabled={saving || disableControls || isTesting}
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
              disabled={saving || disableControls || isTesting}
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
                disabled={saving || disableControls || isTesting}
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
                disabled={saving || disableControls || isTesting}
              />
            )}
            <ProviderCooldownFields
              value={form.cooldown}
              onChange={(cooldown) => setForm((prev) => ({ ...prev, cooldown }))}
              disabled={saving || disableControls || isTesting}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={saving || disableControls || isTesting}
            />
            <Input
              label={t('ai_providers.claude_add_modal_url_label')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={saving || disableControls || isTesting}
            />
            <div className="form-group">
              <label>{t('ai_providers.claude_auth_mode_label')}</label>
              <Select
                value={form.authMode ?? 'auto'}
                options={authModeOptions}
                onChange={(value) =>
                  setForm((prev) => ({
                    ...prev,
                    authMode:
                      value === 'api-key' || value === 'bearer' || value === 'auto'
                        ? value
                        : 'auto',
                  }))
                }
                ariaLabel={t('ai_providers.claude_auth_mode_label')}
                disabled={saving || disableControls || isTesting}
              />
              <div className="hint">{t('ai_providers.claude_auth_mode_hint')}</div>
            </div>
            <Input
              label={t('ai_providers.claude_add_modal_proxy_label')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={saving || disableControls || isTesting}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={saving || disableControls || isTesting}
            />

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>{t('ai_providers.claude_models_label')}</label>
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
                    disabled={saving || disableControls || isTesting}
                  >
                    {t('ai_providers.claude_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={openClaudeModelDiscovery}
                    disabled={saving || disableControls || isTesting}
                  >
                    {t('ai_providers.claude_models_fetch_button')}
                  </Button>
                </div>
              </div>

              <div className={styles.sectionHint}>{t('ai_providers.claude_models_hint')}</div>

              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={saving || disableControls || isTesting}
                hideAddButton
                className={styles.modelInputList}
                rowClassName={styles.modelInputRow}
                inputClassName={styles.modelInputField}
                removeButtonClassName={styles.modelRowRemoveButton}
                removeButtonTitle={t('common.delete')}
                removeButtonAriaLabel={t('common.delete')}
              />

              <ProviderTestPanel
                title={t('ai_providers.claude_test_title')}
                hint={
                  defaultTestModel && availableModels.length === 0
                    ? t('ai_providers.provider_test_default_model_hint', {
                        model: defaultTestModel,
                      })
                    : t('ai_providers.claude_test_hint')
                }
                modelValue={testModel}
                modelOptions={modelSelectOptions}
                modelPlaceholder={
                  defaultTestModel && availableModels.length === 0
                    ? t('ai_providers.provider_test_use_default_model', {
                        model: defaultTestModel,
                      })
                    : availableModels.length
                      ? t('ai_providers.claude_test_select_placeholder')
                      : t('ai_providers.claude_test_select_empty')
                }
                modelAriaLabel={t('ai_providers.claude_test_title')}
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
                onRunEditedCommand={() => void runClaudeConnectivityTest('edited')}
                onRunDefaultCommand={() => void runClaudeConnectivityTest('generated')}
                fillDefaultLabel={t('ai_providers.provider_test_fill_default_command')}
                runEditedLabel={t('ai_providers.provider_test_run_edited_command')}
                runDefaultLabel={t('ai_providers.claude_test_action')}
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
                disabled={saving || disableControls || isTesting}
              />
              <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
            </div>

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>{t('ai_providers.claude_cloak_title')}</label>
                <div className={styles.modelConfigToolbar}>
                  <ToggleSwitch
                    checked={Boolean(form.cloak)}
                    onChange={(enabled) =>
                      setForm((prev) => {
                        if (!enabled) {
                          if (prev.cloak) {
                            lastCloakConfigRef.current = prev.cloak;
                          }
                          return { ...prev, cloak: undefined };
                        }

                        const restored = prev.cloak
                          ?? lastCloakConfigRef.current
                          ?? { mode: 'auto', strictMode: false, sensitiveWords: [] };
                        const mode = String(restored.mode ?? 'auto').trim() || 'auto';
                        return {
                          ...prev,
                          cloak: {
                            mode,
                            strictMode: restored.strictMode ?? false,
                            sensitiveWords: restored.sensitiveWords ?? [],
                          },
                        };
                      })
                    }
                    disabled={saving || disableControls || isTesting}
                    ariaLabel={t('ai_providers.claude_cloak_toggle_aria')}
                    label={t('ai_providers.claude_cloak_toggle_label')}
                  />
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.claude_cloak_hint')}</div>

              {form.cloak ? (
                <>
                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_mode_label')}</label>
                    <Select
                      value={resolvedCloakMode}
                      options={cloakModeOptions}
                      onChange={(value) =>
                        setForm((prev) => ({
                          ...prev,
                          cloak: {
                            ...(prev.cloak ?? {}),
                            mode: value,
                          },
                        }))
                      }
                      ariaLabel={t('ai_providers.claude_cloak_mode_label')}
                      disabled={saving || disableControls || isTesting}
                    />
                    <div className="hint">{t('ai_providers.claude_cloak_mode_hint')}</div>
                  </div>

                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_strict_label')}</label>
                    <ToggleSwitch
                      checked={Boolean(form.cloak.strictMode)}
                      onChange={(value) =>
                        setForm((prev) => ({
                          ...prev,
                          cloak: {
                            ...(prev.cloak ?? {}),
                            strictMode: value,
                          },
                        }))
                      }
                      disabled={saving || disableControls || isTesting}
                      ariaLabel={t('ai_providers.claude_cloak_strict_label')}
                    />
                    <div className="hint">{t('ai_providers.claude_cloak_strict_hint')}</div>
                  </div>

                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_sensitive_words_label')}</label>
                    <textarea
                      className="input"
                      placeholder={t('ai_providers.claude_cloak_sensitive_words_placeholder')}
                      value={(form.cloak.sensitiveWords ?? []).join('\n')}
                      onChange={(e) => {
                        const nextWords = parseTextList(e.target.value);
                        setForm((prev) => ({
                          ...prev,
                          cloak: {
                            ...(prev.cloak ?? {}),
                            sensitiveWords: nextWords.length ? nextWords : undefined,
                          },
                        }));
                      }}
                      rows={3}
                      disabled={saving || disableControls || isTesting}
                    />
                    <div className="hint">{t('ai_providers.claude_cloak_sensitive_words_hint')}</div>
                  </div>
                </>
              ) : null}
            </div>
          </div>
        )}
      </Card>
    </SecondaryScreenShell>
  );
}
