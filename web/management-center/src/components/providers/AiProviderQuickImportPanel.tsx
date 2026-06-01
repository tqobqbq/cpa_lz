import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ProviderTestPanel } from '@/components/providers/ProviderTestPanel';
import {
  buildQuickImportProviderConfig,
  extractQuickImportFields,
  normalizeQuickImportPriority,
  type QuickImportProvider,
} from '@/components/providers/quickImport';
import type { ProviderKeyConfig } from '@/types';
import styles from '@/pages/AiProvidersPage.module.scss';

type ProviderTestStatus = 'idle' | 'loading' | 'success' | 'error';

export type QuickImportTestState = {
  modelValue: string;
  modelOptions: Array<{ value: string; label: string }>;
  modelPlaceholder: string;
  commandValue: string;
  commandPlaceholder: string;
  result: string;
  status: ProviderTestStatus;
  message: string;
};

type AiProviderQuickImportPanelProps = {
  disabled?: boolean;
  provider: QuickImportProvider;
  baseUrl: string;
  apiKey: string;
  testState: QuickImportTestState;
  onProviderChange: (provider: QuickImportProvider) => void;
  onBaseUrlChange: (value: string) => void;
  onApiKeyChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onCommandChange: (value: string) => void;
  onFillDefaultCommand: () => void;
  onRunEditedCommand: () => void;
  onRunDefaultCommand: () => void;
  onAppend: (provider: QuickImportProvider, entry: ProviderKeyConfig) => Promise<void>;
};

export function AiProviderQuickImportPanel({
  disabled = false,
  provider,
  baseUrl,
  apiKey,
  testState,
  onProviderChange,
  onBaseUrlChange,
  onApiKeyChange,
  onModelChange,
  onCommandChange,
  onFillDefaultCommand,
  onRunEditedCommand,
  onRunDefaultCommand,
  onAppend,
}: AiProviderQuickImportPanelProps) {
  const { t } = useTranslation();
  const [sourceText, setSourceText] = useState('');
  const [priorityText, setPriorityText] = useState('10');
  const [prefix, setPrefix] = useState('');
  const [appendMessage, setAppendMessage] = useState('');
  const lastExtractedRef = useRef({ baseUrl: '', apiKey: '' });

  useEffect(() => {
    const extracted = extractQuickImportFields(sourceText);
    if (!baseUrl || baseUrl === lastExtractedRef.current.baseUrl) {
      onBaseUrlChange(extracted.baseUrl);
    }
    if (!apiKey || apiKey === lastExtractedRef.current.apiKey) {
      onApiKeyChange(extracted.apiKey);
    }
    lastExtractedRef.current = extracted;
  }, [apiKey, baseUrl, onApiKeyChange, onBaseUrlChange, sourceText]);

  const providerOptions = useMemo(
    () => [
      { value: 'codex', label: 'Codex' },
      { value: 'claude', label: 'Claude' },
    ],
    []
  );

  const handleProviderChange = (value: string) => {
    onProviderChange(value === 'claude' ? 'claude' : 'codex');
    setAppendMessage('');
  };

  const handleAppend = async () => {
    setAppendMessage('');
    if (!baseUrl.trim() || !apiKey.trim()) {
      setAppendMessage(t('ai_providers.quick_import_missing_fields'));
      return;
    }

    const priority = normalizeQuickImportPriority(priorityText);
    if (priority.error || priority.value === null) {
      setAppendMessage(t('ai_providers.quick_import_invalid_priority'));
      return;
    }

    await onAppend(
      provider,
      buildQuickImportProviderConfig({
        provider,
        apiKey,
        baseUrl,
        priority: priority.value,
        prefix,
      })
    );
  };

  return (
    <div className={styles.quickImportPanel}>
      <div className={styles.quickImportGrid}>
        <label className={styles.quickImportField}>
          <span>{t('ai_providers.quick_import_paste_label')}</span>
          <textarea
            className={`input ${styles.quickImportPaste}`}
            value={sourceText}
            onChange={(event) => setSourceText(event.target.value)}
            placeholder={t('ai_providers.quick_import_paste_placeholder')}
            disabled={disabled}
          />
        </label>

        <div className={styles.quickImportDetected}>
          <label className={styles.quickImportField}>
            <span>{t('common.base_url')}</span>
            <Input
              value={baseUrl}
              onChange={(event) => onBaseUrlChange(event.target.value)}
              disabled={disabled}
            />
          </label>

          <label className={styles.quickImportField}>
            <span>{t('common.api_key')}</span>
            <Input
              value={apiKey}
              onChange={(event) => onApiKeyChange(event.target.value)}
              disabled={disabled}
            />
          </label>

          <div className={styles.quickImportInline}>
            <label className={styles.quickImportField}>
              <span>{t('ai_providers.quick_import_provider_label')}</span>
              <Select
                value={provider}
                options={providerOptions}
                onChange={handleProviderChange}
                ariaLabel={t('ai_providers.quick_import_provider_label')}
                disabled={disabled}
              />
            </label>
            <label className={styles.quickImportField}>
              <span>{t('common.priority')}</span>
              <Input
                value={priorityText}
                onChange={(event) => setPriorityText(event.target.value)}
                disabled={disabled}
              />
            </label>
          </div>

          <label className={styles.quickImportField}>
            <span>{t('common.prefix')}</span>
            <Input
              value={prefix}
              onChange={(event) => setPrefix(event.target.value)}
              disabled={disabled}
            />
          </label>
        </div>
      </div>

      <ProviderTestPanel
        title={
          provider === 'codex'
            ? t('ai_providers.codex_test_title')
            : t('ai_providers.claude_test_title')
        }
        hint={t('ai_providers.quick_import_test_hint')}
        modelValue={testState.modelValue}
        modelOptions={testState.modelOptions}
        modelPlaceholder={testState.modelPlaceholder}
        modelAriaLabel={
          provider === 'codex'
            ? t('ai_providers.codex_test_title')
            : t('ai_providers.claude_test_title')
        }
        commandValue={testState.commandValue}
        commandPlaceholder={testState.commandPlaceholder}
        result={testState.result}
        status={testState.status}
        message={testState.message}
        disabled={disabled}
        onModelChange={onModelChange}
        onCommandChange={onCommandChange}
        onFillDefaultCommand={onFillDefaultCommand}
        onRunEditedCommand={onRunEditedCommand}
        onRunDefaultCommand={onRunDefaultCommand}
        fillDefaultLabel={t('ai_providers.provider_test_fill_default_command')}
        runEditedLabel={t('ai_providers.provider_test_run_edited_command')}
        runDefaultLabel={
          provider === 'codex'
            ? t('ai_providers.codex_test_action')
            : t('ai_providers.claude_test_action')
        }
        commandLabel={t('ai_providers.test_details_command')}
        resultLabel={t('ai_providers.test_details_result')}
      />

      {appendMessage ? <div className="status-badge error">{appendMessage}</div> : null}

      <div className={styles.quickImportFooter}>
        <Button variant="primary" size="sm" onClick={() => void handleAppend()} disabled={disabled}>
          {t('ai_providers.quick_import_append_action', {
            provider: provider === 'codex' ? 'codex-api-key' : 'claude-api-key',
          })}
        </Button>
      </div>
    </div>
  );
}
