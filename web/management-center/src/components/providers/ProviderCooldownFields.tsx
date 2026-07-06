import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { ProviderCooldownConfig } from '@/types';
import { normalizeProviderCooldown, withProviderCooldownDefaults } from '@/utils/providerCooldown';
import styles from './ProviderCooldownFields.module.scss';

interface ProviderCooldownFieldsProps {
  value?: ProviderCooldownConfig;
  onChange: (value: ProviderCooldownConfig | undefined) => void;
  disabled?: boolean;
  scope?: 'global' | 'provider';
}

export function ProviderCooldownFields({
  value,
  onChange,
  disabled = false,
  scope = 'provider',
}: ProviderCooldownFieldsProps) {
  const { t } = useTranslation();
  const isGlobal = scope === 'global';
  const customEnabled = isGlobal || Boolean(normalizeProviderCooldown(value));
  const current = useMemo(() => withProviderCooldownDefaults(value), [value]);

  const updateField = (field: keyof ProviderCooldownConfig, nextValue: number) => {
    onChange(
      normalizeProviderCooldown({
        ...current,
        [field]: nextValue,
      })
    );
  };

  return (
    <div className={styles.root}>
      {!isGlobal && (
        <div className={styles.toggleRow}>
          <label>{t('ai_providers.cooldown_scope_label')}</label>
          <ToggleSwitch
            checked={customEnabled}
            onChange={(checked) => onChange(checked ? current : undefined)}
            disabled={disabled}
            ariaLabel={t('ai_providers.cooldown_scope_label')}
            label={
              customEnabled
                ? t('ai_providers.cooldown_scope_custom')
                : t('ai_providers.cooldown_scope_inherit')
            }
          />
          <div className="hint">
            {customEnabled
              ? t('ai_providers.cooldown_provider_hint')
              : t('ai_providers.cooldown_inherit_hint')}
          </div>
        </div>
      )}

      {customEnabled && (
        <div className={styles.fields}>
          <Input
            label={t('ai_providers.cooldown_start_label')}
            type="number"
            min={1}
            step={1}
            value={current.start}
            onChange={(event) => {
              const parsed = Number(event.target.value);
              updateField('start', Number.isFinite(parsed) ? parsed : 1);
            }}
            disabled={disabled}
          />
          <Input
            label={t('ai_providers.cooldown_exponent_label')}
            type="number"
            min={0.1}
            step={0.1}
            value={current.exponent}
            onChange={(event) => {
              const parsed = Number(event.target.value);
              updateField('exponent', Number.isFinite(parsed) ? parsed : 1.2);
            }}
            disabled={disabled}
          />
          <Input
            label={t('ai_providers.cooldown_max_label')}
            type="number"
            min={1}
            step={1}
            value={current.max}
            onChange={(event) => {
              const parsed = Number(event.target.value);
              updateField('max', Number.isFinite(parsed) ? parsed : 10);
            }}
            disabled={disabled}
          />
        </div>
      )}

      {isGlobal && <div className="hint">{t('ai_providers.cooldown_global_hint')}</div>}
    </div>
  );
}

