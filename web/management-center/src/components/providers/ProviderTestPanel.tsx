import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import styles from '@/pages/AiProvidersPage.module.scss';

type ProviderTestStatus = 'idle' | 'loading' | 'success' | 'error';

interface ProviderTestPanelProps {
  title: string;
  hint: string;
  modelValue: string;
  modelOptions: Array<{ value: string; label: string }>;
  modelPlaceholder: string;
  modelAriaLabel: string;
  commandValue: string;
  commandPlaceholder: string;
  result: string;
  status: ProviderTestStatus;
  message: string;
  disabled?: boolean;
  onModelChange: (value: string) => void;
  onCommandChange: (value: string) => void;
  onFillDefaultCommand: () => void;
  onRunEditedCommand: () => void;
  onRunDefaultCommand: () => void;
  fillDefaultLabel: string;
  runEditedLabel: string;
  runDefaultLabel: string;
  commandLabel: string;
  resultLabel: string;
}

export function ProviderTestPanel({
  title,
  hint,
  modelValue,
  modelOptions,
  modelPlaceholder,
  modelAriaLabel,
  commandValue,
  commandPlaceholder,
  result,
  status,
  message,
  disabled = false,
  onModelChange,
  onCommandChange,
  onFillDefaultCommand,
  onRunEditedCommand,
  onRunDefaultCommand,
  fillDefaultLabel,
  runEditedLabel,
  runDefaultLabel,
  commandLabel,
  resultLabel,
}: ProviderTestPanelProps) {
  const controlsDisabled = disabled || status === 'loading';

  return (
    <>
      <div className={styles.modelTestPanel}>
        <div className={styles.modelTestMeta}>
          <label className={styles.modelTestLabel}>{title}</label>
          <span className={styles.modelTestHint}>{hint}</span>
        </div>
        <div className={styles.modelTestControls}>
          <Select
            value={modelValue}
            options={modelOptions}
            onChange={onModelChange}
            placeholder={modelPlaceholder}
            className={styles.openaiTestSelect}
            ariaLabel={modelAriaLabel}
            disabled={controlsDisabled || modelOptions.length === 0}
          />
          <div className={styles.modelTestActionGroup}>
            <Button
              variant="secondary"
              size="sm"
              onClick={onFillDefaultCommand}
              disabled={controlsDisabled}
            >
              {fillDefaultLabel}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={onRunEditedCommand}
              disabled={controlsDisabled || !commandValue.trim()}
            >
              {runEditedLabel}
            </Button>
            <Button
              variant={status === 'error' ? 'danger' : 'secondary'}
              size="sm"
              onClick={onRunDefaultCommand}
              loading={status === 'loading'}
              disabled={controlsDisabled}
              className={styles.modelTestAllButton}
            >
              {runDefaultLabel}
            </Button>
          </div>
        </div>
      </div>

      <div className={styles.modelTestDetails}>
        <div className={styles.modelTestDetailsBlock}>
          <div className={styles.modelTestDetailsTitle}>{commandLabel}</div>
          <textarea
            className={`input ${styles.modelTestCommandEditor}`}
            rows={8}
            value={commandValue}
            onChange={(event) => onCommandChange(event.target.value)}
            placeholder={commandPlaceholder}
            disabled={controlsDisabled}
          />
        </div>
        {result ? (
          <div className={styles.modelTestDetailsBlock}>
            <div className={styles.modelTestDetailsTitle}>{resultLabel}</div>
            <pre className={styles.modelTestDetailsPre}>{result}</pre>
          </div>
        ) : null}
      </div>

      {message ? (
        <div
          className={`status-badge ${
            status === 'error' ? 'error' : status === 'success' ? 'success' : 'muted'
          }`}
        >
          {message}
        </div>
      ) : null}
    </>
  );
}
