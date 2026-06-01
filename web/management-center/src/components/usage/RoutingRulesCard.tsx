import { useEffect, useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { configApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import type { RoutingRuleConfig } from '@/types';
import type { UsagePayload } from './hooks/useUsageData';
import {
  createEmptyRoutingRule,
  normalizeSeenUserAgents,
  sanitizeRoutingRules,
} from './routingRules';
import styles from '@/pages/UsagePage.module.scss';

export interface RoutingRulesCardProps {
  usage: UsagePayload | null;
  loading: boolean;
}

const PROVIDER_OPTIONS = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'gemini-cli', label: 'Gemini CLI' },
  { value: 'vertex', label: 'Vertex' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'aistudio', label: 'AI Studio' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'kimi', label: 'Kimi' },
] as const;

export function RoutingRulesCard({ usage, loading }: RoutingRulesCardProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const [rules, setRules] = useState<RoutingRuleConfig[]>(() => config?.routingRules ?? []);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const userAgentListId = useId();

  useEffect(() => {
    if (config) return;
    void fetchConfig().catch(() => {});
  }, [config, fetchConfig]);

  useEffect(() => {
    setRules(config?.routingRules ?? []);
  }, [config?.routingRules]);

  const seenUserAgents = useMemo(
    () => normalizeSeenUserAgents(usage?.user_agents),
    [usage?.user_agents]
  );
  const userAgentMatchOptions = useMemo(
    () => [
      { value: '', label: t('usage_stats.routing_rule_user_agent_any') },
      { value: 'contains', label: 'contains' },
      { value: 'equals', label: 'equals' },
    ],
    [t]
  );
  const inputCharsOperatorOptions = useMemo(
    () => [
      { value: '', label: t('usage_stats.routing_rule_input_chars_any') },
      { value: 'gt', label: '>' },
      { value: 'lt', label: '<' },
    ],
    [t]
  );

  const handleRuleChange = (index: number, nextRule: RoutingRuleConfig) => {
    setRules((current) => current.map((rule, currentIndex) => (currentIndex === index ? nextRule : rule)));
  };

  const handleAddRule = () => {
    setRules((current) => [...current, createEmptyRoutingRule()]);
    setError('');
  };

  const handleRemoveRule = (index: number) => {
    setRules((current) => current.filter((_, currentIndex) => currentIndex !== index));
    setError('');
  };

  const handleResetRules = () => {
    setRules(config?.routingRules ?? []);
    setError('');
  };

  const handleSaveRules = async () => {
    const invalidRule = rules.find((rule) => {
      const provider = String(rule.provider ?? '').trim();
      if (!provider) return true;

      const userAgentMatch = String(rule.userAgent?.match ?? '').trim();
      const userAgentValue = String(rule.userAgent?.value ?? '').trim();
      if (userAgentMatch && !userAgentValue) {
        return true;
      }

      const inputCharsOperator = String(rule.inputChars?.operator ?? '').trim();
      if (inputCharsOperator) {
        const parsedValue = Number(rule.inputChars?.value);
        if (!Number.isFinite(parsedValue) || parsedValue < 0) {
          return true;
        }
      }

      return false;
    });
    if (invalidRule) {
      const message = t('usage_stats.routing_rules_invalid');
      setError(message);
      showNotification(message, 'error');
      return;
    }

    const nextRules = sanitizeRoutingRules(rules) as RoutingRuleConfig[];
    setSaving(true);
    setError('');
    try {
      await configApi.updateRoutingRules(nextRules);
      updateConfigValue('routing/rules', nextRules);
      setRules(nextRules);
      showNotification(t('usage_stats.routing_rules_saved'), 'success');
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('notification.update_failed');
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card
      title={t('usage_stats.routing_rules_title')}
      className={styles.routingRulesCard}
      extra={
        <div className={styles.requestEventsActions}>
          <Button variant="ghost" size="sm" onClick={handleResetRules} disabled={saving}>
            {t('common.refresh')}
          </Button>
          <Button variant="secondary" size="sm" onClick={handleAddRule} disabled={saving}>
            {t('usage_stats.routing_rules_add')}
          </Button>
          <Button size="sm" onClick={() => void handleSaveRules()} loading={saving}>
            {t('common.save')}
          </Button>
        </div>
      }
    >
      <div className={styles.routingRulesIntro}>
        <div className={styles.routingRulesHint}>{t('usage_stats.routing_rules_hint')}</div>
        <div className={styles.routingRulesHint}>
          {t('usage_stats.routing_rules_seen_user_agents', { count: seenUserAgents.length })}
        </div>
      </div>

      {error ? <div className="error-box">{error}</div> : null}

      {loading && !config ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : rules.length === 0 ? (
        <div className={styles.hint}>{t('usage_stats.routing_rules_empty')}</div>
      ) : (
        <div className={styles.routingRulesList}>
          {rules.map((rule, index) => (
            <div key={`routing-rule-${index}`} className={styles.routingRuleRow}>
              <div className={styles.routingRuleFields}>
                <Select
                  value={rule.provider}
                  options={PROVIDER_OPTIONS}
                  onChange={(value) =>
                    handleRuleChange(index, {
                      ...rule,
                      provider: value,
                    })
                  }
                  placeholder={t('usage_stats.routing_rule_provider')}
                  ariaLabel={t('usage_stats.routing_rule_provider')}
                />
                <Select
                  value={rule.userAgent?.match ?? ''}
                  options={userAgentMatchOptions}
                  onChange={(value) =>
                    handleRuleChange(index, {
                      ...rule,
                      userAgent: value
                        ? {
                            ...(rule.userAgent ?? {}),
                            match: value as NonNullable<RoutingRuleConfig['userAgent']>['match'],
                            value: rule.userAgent?.value ?? '',
                          }
                        : undefined,
                    })
                  }
                  ariaLabel={t('usage_stats.routing_rule_user_agent_match')}
                />
                <Input
                  value={rule.userAgent?.value ?? ''}
                  onChange={(event) =>
                    handleRuleChange(index, {
                      ...rule,
                      userAgent: {
                        ...(rule.userAgent ?? { match: 'contains' }),
                        value: event.target.value,
                      },
                    })
                  }
                  placeholder={t('usage_stats.routing_rule_user_agent_value')}
                  aria-label={t('usage_stats.routing_rule_user_agent_value')}
                  list={seenUserAgents.length > 0 ? userAgentListId : undefined}
                  disabled={!rule.userAgent?.match || saving}
                />
                <Select
                  value={rule.inputChars?.operator ?? ''}
                  options={inputCharsOperatorOptions}
                  onChange={(value) =>
                    handleRuleChange(index, {
                      ...rule,
                      inputChars: value
                        ? {
                            ...(rule.inputChars ?? {}),
                            operator: value as NonNullable<RoutingRuleConfig['inputChars']>['operator'],
                            value: rule.inputChars?.value ?? 0,
                          }
                        : undefined,
                    })
                  }
                  ariaLabel={t('usage_stats.routing_rule_input_chars_operator')}
                />
                <Input
                  type="number"
                  min={0}
                  step={1}
                  value={rule.inputChars?.value ?? ''}
                  onChange={(event) => {
                    const parsed = event.target.value.trim() === '' ? undefined : Number(event.target.value);
                    handleRuleChange(index, {
                      ...rule,
                      inputChars: {
                        ...(rule.inputChars ?? { operator: 'gt' }),
                        value: parsed !== undefined && Number.isFinite(parsed) ? parsed : undefined,
                      },
                    });
                  }}
                  placeholder="0"
                  aria-label={t('usage_stats.routing_rule_input_chars_value')}
                  disabled={!rule.inputChars?.operator || saving}
                />
              </div>
              <Button
                variant="danger"
                size="sm"
                onClick={() => handleRemoveRule(index)}
                disabled={saving}
              >
                {t('common.delete')}
              </Button>
            </div>
          ))}
        </div>
      )}

      {seenUserAgents.length > 0 ? (
        <datalist id={userAgentListId}>
          {seenUserAgents.map((userAgent) => (
            <option key={userAgent} value={userAgent} />
          ))}
        </datalist>
      ) : null}
    </Card>
  );
}
