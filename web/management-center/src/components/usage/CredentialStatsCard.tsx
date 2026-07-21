import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { authFilesApi } from '@/services/api/authFiles';
import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import {
  LATENCY_SOURCE_FIELD,
  calculateCost,
  calculateUsageBucketCost,
  collectUsage20mSummaryEntries,
  collectUsageDetails,
  formatCompactNumber,
  formatDurationMs,
  formatUsd,
  getUsageTokenBuckets,
  normalizeAuthIndex,
  usageBucketTokenBuckets,
  type ModelPrice,
} from '@/utils/usage';
import type { UsagePayload } from './hooks/useUsageData';
import { buildCredentialStatsRows } from './credentialStats';
import styles from '@/pages/UsagePage.module.scss';

export interface CredentialStatsCardProps {
  usage: UsagePayload | null;
  loading: boolean;
  geminiKeys: GeminiKeyConfig[];
  claudeConfigs: ProviderKeyConfig[];
  codexConfigs: ProviderKeyConfig[];
  xaiConfigs: ProviderKeyConfig[];
  vertexConfigs: ProviderKeyConfig[];
  openaiProviders: OpenAIProviderConfig[];
  modelPrices: Record<string, ModelPrice>;
  onEditSource?: (path: string) => void;
}

export function CredentialStatsCard({
  usage,
  loading,
  geminiKeys,
  claudeConfigs,
  codexConfigs,
  xaiConfigs,
  vertexConfigs,
  openaiProviders,
  modelPrices,
  onEditSource,
}: CredentialStatsCardProps) {
  const { t } = useTranslation();
  const [authFileMap, setAuthFileMap] = useState<Map<string, CredentialInfo>>(new Map());

  useEffect(() => {
    let cancelled = false;

    authFilesApi
      .list()
      .then((res) => {
        if (cancelled) return;

        const files = Array.isArray(res) ? res : (res as { files?: AuthFileItem[] })?.files;
        if (!Array.isArray(files)) return;

        const map = new Map<string, CredentialInfo>();
        files.forEach((file) => {
          const key = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
          if (!key) return;

          map.set(key, {
            name: file.name || key,
            type: (file.type || file.provider || '').toString(),
          });
        });
        setAuthFileMap(map);
      })
      .catch(() => {});

    return () => {
      cancelled = true;
    };
  }, []);

  const sourceInfoMap = useMemo(
    () =>
      buildSourceInfoMap({
        geminiApiKeys: geminiKeys,
        claudeApiKeys: claudeConfigs,
        codexApiKeys: codexConfigs,
        xaiApiKeys: xaiConfigs,
        vertexApiKeys: vertexConfigs,
        openaiCompatibility: openaiProviders,
      }),
    [claudeConfigs, codexConfigs, geminiKeys, openaiProviders, vertexConfigs, xaiConfigs]
  );

  // Usage tables can contain many rows; preserve this explicit cache across renders.
  // eslint-disable-next-line react-hooks/preserve-manual-memoization
  const rows = useMemo(() => {
    if (!usage) return [];
    const summaryEntries = collectUsage20mSummaryEntries(usage);
    if (summaryEntries.length > 0) {
      return buildCredentialStatsRows(
        summaryEntries.map((entry) => {
          const [kind, ...rest] = entry.identity.split(':');
          const identityValue = rest.join(':');
          const authIndex = kind === 'auth_index' ? identityValue : null;
          const sourceRaw = kind === 'source' ? identityValue : '';
          const sourceInfo = resolveSourceDisplay(
            sourceRaw,
            authIndex,
            sourceInfoMap,
            authFileMap
          );
          const buckets = usageBucketTokenBuckets(entry.stats);
          const latencySampleCount = Number(entry.stats.latency_samples) || 0;
          return {
            identityKey: sourceInfo.identityKey ?? entry.identity,
            displayName: sourceInfo.displayName || entry.identity,
            type: sourceInfo.type || entry.provider,
            editPath: sourceInfo.editPath,
            failed: false,
            success: Number(entry.stats.success_count) || 0,
            failure: Number(entry.stats.failure_count) || 0,
            inputTokens: buckets.inputTokens,
            outputTokens: buckets.outputTokens,
            reasoningTokens: buckets.reasoningTokens,
            cachedTokens: buckets.cachedInputTokens,
            cacheCreationTokens: buckets.cacheCreationTokens,
            cacheReadTokens: buckets.cacheReadTokens,
            totalLatencyMs: latencySampleCount > 0 ? Number(entry.stats.latency_total_ms) || 0 : 0,
            latencySampleCount,
            cost: calculateUsageBucketCost(entry.model, entry.stats, modelPrices),
          };
        })
      );
    }

    return buildCredentialStatsRows(
      collectUsageDetails(usage).map((detail) => {
        const sourceInfo = resolveSourceDisplay(
          detail.source ?? '',
          detail.auth_index,
          sourceInfoMap,
          authFileMap
        );
        const buckets = getUsageTokenBuckets(detail);
        const latencyMs = Number(detail.latency_ms) || 0;
        return {
          identityKey: sourceInfo.identityKey ?? sourceInfo.displayName,
          displayName: sourceInfo.displayName,
          type: sourceInfo.type,
          editPath: sourceInfo.editPath,
          failed: detail.failed === true,
          inputTokens: buckets.inputTokens,
          outputTokens: buckets.outputTokens,
          reasoningTokens: buckets.reasoningTokens,
          cachedTokens: buckets.cachedInputTokens,
          cacheCreationTokens: buckets.cacheCreationTokens,
          cacheReadTokens: buckets.cacheReadTokens,
          totalLatencyMs: latencyMs > 0 ? latencyMs : 0,
          latencySampleCount: latencyMs > 0 ? 1 : 0,
          cost: calculateCost(detail, modelPrices),
        };
      })
    );
  }, [authFileMap, modelPrices, sourceInfoMap, usage]);
  const hasLatencyData = rows.some((row) => row.latencySampleCount > 0);
  const latencyHint = t('usage_stats.latency_unit_hint', {
    field: LATENCY_SOURCE_FIELD,
    unit: t('usage_stats.duration_unit_ms'),
  });
  const handleCredentialClick = (path: string) => {
    onEditSource?.(path);
  };

  return (
    <Card title={t('usage_stats.credential_stats')} className={styles.detailsFixedCard}>
      {loading ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : rows.length > 0 ? (
        <div className={styles.detailsScroll}>
          {hasLatencyData && <div className={styles.detailsNote}>{latencyHint}</div>}
          <div className={styles.tableWrapper}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_stats.credential_name')}</th>
                  <th>{t('usage_stats.requests_count')}</th>
                  <th>{t('usage_stats.success_rate')}</th>
                  <th>{t('usage_stats.input_tokens')}</th>
                  <th>{t('usage_stats.output_tokens')}</th>
                  <th>{t('usage_stats.reasoning_tokens')}</th>
                  <th>{t('usage_stats.cached_tokens')}</th>
                  <th>{t('usage_stats.cache_creation_tokens')}</th>
                  <th>{t('usage_stats.cache_read_tokens')}</th>
                  <th title={latencyHint}>{t('usage_stats.avg_time')}</th>
                  <th title={latencyHint}>{t('usage_stats.total_time')}</th>
                  <th>{t('usage_stats.total_cost')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.key}>
                    <td className={styles.modelCell}>
                      {row.editPath && onEditSource ? (
                        <button
                          type="button"
                          className={styles.credentialLinkButton}
                          onClick={() => handleCredentialClick(row.editPath as string)}
                        >
                          {row.displayName}
                        </button>
                      ) : (
                        <span>{row.displayName}</span>
                      )}
                      {row.type && <span className={styles.credentialType}>{row.type}</span>}
                    </td>
                    <td>
                      <span className={styles.requestCountCell}>
                        <span>{formatCompactNumber(row.total)}</span>
                        <span className={styles.requestBreakdown}>
                          (
                          <span className={styles.statSuccess}>
                            {row.success.toLocaleString()}
                          </span>{' '}
                          <span className={styles.statFailure}>
                            {row.failure.toLocaleString()}
                          </span>
                          )
                        </span>
                      </span>
                    </td>
                    <td>
                      <span
                        className={
                          row.successRate >= 95
                            ? styles.statSuccess
                            : row.successRate >= 80
                              ? styles.statNeutral
                              : styles.statFailure
                        }
                      >
                        {row.successRate.toFixed(1)}%
                      </span>
                    </td>
                    <td>{row.inputTokens.toLocaleString()}</td>
                    <td>{row.outputTokens.toLocaleString()}</td>
                    <td>{row.reasoningTokens.toLocaleString()}</td>
                    <td>{row.cachedTokens.toLocaleString()}</td>
                    <td>{row.cacheCreationTokens.toLocaleString()}</td>
                    <td>{row.cacheReadTokens.toLocaleString()}</td>
                    <td className={styles.durationCell}>{formatDurationMs(row.averageLatencyMs)}</td>
                    <td className={styles.durationCell}>{formatDurationMs(row.totalLatencyMs)}</td>
                    <td>{row.cost > 0 ? formatUsd(row.cost) : '--'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : (
        <div className={styles.hint}>{t('usage_stats.no_data')}</div>
      )}
    </Card>
  );
}
