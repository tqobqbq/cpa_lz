import { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import {
  LATENCY_SOURCE_FIELD,
  formatCompactNumber,
  formatDurationMs,
  formatUsd,
  type ApiStats,
} from '@/utils/usage';
import styles from '@/pages/UsagePage.module.scss';

export interface ApiDetailsCardProps {
  apiStats: ApiStats[];
  loading: boolean;
  hasPrices: boolean;
}

type ApiSortKey =
  | 'endpoint'
  | 'requests'
  | 'tokens'
  | 'inputTokens'
  | 'outputTokens'
  | 'reasoningTokens'
  | 'cachedTokens'
  | 'cacheCreationTokens'
  | 'cacheReadTokens'
  | 'averageLatencyMs'
  | 'totalLatencyMs'
  | 'cost';
type SortDir = 'asc' | 'desc';

export function ApiDetailsCard({ apiStats, loading, hasPrices }: ApiDetailsCardProps) {
  const { t } = useTranslation();
  const [sortKey, setSortKey] = useState<ApiSortKey>('requests');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  const latencyHint = t('usage_stats.latency_unit_hint', {
    field: LATENCY_SOURCE_FIELD,
    unit: t('usage_stats.duration_unit_ms'),
  });

  const handleSort = (key: ApiSortKey) => {
    if (sortKey === key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
    } else {
      setSortKey(key);
      setSortDir(key === 'endpoint' ? 'asc' : 'desc');
    }
  };

  const sorted = useMemo(() => {
    const list = apiStats.map((api) => ({
      ...api,
      successRate: api.totalRequests > 0 ? (api.successCount / api.totalRequests) * 100 : 100,
    }));
    const dir = sortDir === 'asc' ? 1 : -1;
    list.sort((a, b) => {
      switch (sortKey) {
        case 'endpoint': return dir * a.endpoint.localeCompare(b.endpoint);
        case 'requests': return dir * (a.totalRequests - b.totalRequests);
        case 'tokens': return dir * (a.totalTokens - b.totalTokens);
        case 'cost': return dir * (a.totalCost - b.totalCost);
        default: {
          const left = a[sortKey];
          const right = b[sortKey];
          const leftValue = typeof left === 'number' && Number.isFinite(left) ? left : -1;
          const rightValue = typeof right === 'number' && Number.isFinite(right) ? right : -1;
          return dir * (leftValue - rightValue);
        }
      }
    });
    return list;
  }, [apiStats, sortKey, sortDir]);

  const arrow = (key: ApiSortKey) =>
    sortKey === key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : '';
  const ariaSort = (key: ApiSortKey): 'none' | 'ascending' | 'descending' =>
    sortKey === key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none';

  return (
    <Card title={t('usage_stats.api_details')} className={styles.detailsFixedCard}>
      {loading ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : sorted.length > 0 ? (
        <div className={styles.detailsScroll}>
          <div className={styles.tableWrapper}>
            <table className={styles.table}>
              <thead>
                <tr>
                  {(
                    [
                      ['endpoint', 'usage_stats.api_endpoint'],
                      ['requests', 'usage_stats.requests_count'],
                      ['tokens', 'usage_stats.tokens_count'],
                      ['inputTokens', 'usage_stats.input_tokens'],
                      ['outputTokens', 'usage_stats.output_tokens'],
                      ['reasoningTokens', 'usage_stats.reasoning_tokens'],
                      ['cachedTokens', 'usage_stats.cached_tokens'],
                      ['cacheCreationTokens', 'usage_stats.cache_creation_tokens'],
                      ['cacheReadTokens', 'usage_stats.cache_read_tokens'],
                      ['averageLatencyMs', 'usage_stats.avg_time'],
                      ['totalLatencyMs', 'usage_stats.total_time'],
                    ] as [ApiSortKey, string][]
                  ).map(([key, labelKey]) => (
                    <th key={key} className={styles.sortableHeader} aria-sort={ariaSort(key)}>
                      <button
                        type="button"
                        className={styles.sortHeaderButton}
                        onClick={() => handleSort(key)}
                        title={
                          key === 'averageLatencyMs' || key === 'totalLatencyMs'
                            ? latencyHint
                            : undefined
                        }
                      >
                        {t(labelKey)}
                        {arrow(key)}
                      </button>
                    </th>
                  ))}
                  <th>{t('usage_stats.success_rate')}</th>
                  {hasPrices && (
                    <th className={styles.sortableHeader} aria-sort={ariaSort('cost')}>
                      <button
                        type="button"
                        className={styles.sortHeaderButton}
                        onClick={() => handleSort('cost')}
                      >
                        {t('usage_stats.total_cost')}
                        {arrow('cost')}
                      </button>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody>
                {sorted.map((api) => (
                  <tr key={api.endpoint}>
                    <td className={styles.modelCell}>{api.endpoint}</td>
                    <td>
                      <span className={styles.requestCountCell}>
                        <span>{api.totalRequests.toLocaleString()}</span>
                        <span className={styles.requestBreakdown}>
                          (<span className={styles.statSuccess}>{api.successCount.toLocaleString()}</span>{' '}
                          <span className={styles.statFailure}>{api.failureCount.toLocaleString()}</span>)
                        </span>
                      </span>
                    </td>
                    <td>{formatCompactNumber(api.totalTokens)}</td>
                    <td>{api.inputTokens.toLocaleString()}</td>
                    <td>{api.outputTokens.toLocaleString()}</td>
                    <td>{api.reasoningTokens.toLocaleString()}</td>
                    <td>{api.cachedTokens.toLocaleString()}</td>
                    <td>{api.cacheCreationTokens.toLocaleString()}</td>
                    <td>{api.cacheReadTokens.toLocaleString()}</td>
                    <td className={styles.durationCell}>{formatDurationMs(api.averageLatencyMs)}</td>
                    <td className={styles.durationCell}>{formatDurationMs(api.totalLatencyMs)}</td>
                    <td>
                      <span
                        className={
                          api.successRate >= 95
                            ? styles.statSuccess
                            : api.successRate >= 80
                              ? styles.statNeutral
                              : styles.statFailure
                        }
                      >
                        {api.successRate.toFixed(1)}%
                      </span>
                    </td>
                    {hasPrices && <td>{api.totalCost > 0 ? formatUsd(api.totalCost) : '--'}</td>}
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
