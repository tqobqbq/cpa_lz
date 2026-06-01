import { useEffect, useMemo, useState, type MouseEvent } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Select } from '@/components/ui/Select';
import { authFilesApi } from '@/services/api/authFiles';
import type { GeminiKeyConfig, ProviderKeyConfig, OpenAIProviderConfig } from '@/types';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialInfo, SourceInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import { parseTimestampMs } from '@/utils/timestamp';
import {
  calculateCost,
  collectUsageDetails,
  extractLatencyMs,
  extractTotalTokens,
  formatDurationMs,
  getUsageTokenBuckets,
  LATENCY_SOURCE_FIELD,
  normalizeAuthIndex,
  type ModelPrice,
} from '@/utils/usage';
import { downloadBlob } from '@/utils/download';
import styles from '@/pages/UsagePage.module.scss';
import {
  formatRequestEventCost,
  formatRequestEventStatusCode,
  getRequestEventErrorDisplay,
} from './requestEventsDisplay';

const ALL_FILTER = '__all__';
const MAX_RENDERED_EVENTS = 500;

type RequestEventRow = {
  id: string;
  timestamp: string;
  timestampMs: number;
  timestampLabel: string;
  model: string;
  sourceKey: string;
  sourceRaw: string;
  source: string;
  sourceType: string;
  sourceInfo: SourceInfo;
  authIndex: string;
  remoteIP: string;
  userAgent: string;
  inputChars: number;
  failed: boolean;
  statusCode: number | null;
  errorReason: string;
  errorMessage: string;
  latencyMs: number | null;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheWrite5mTokens: number;
  cacheWrite1hTokens: number;
  cacheReadTokens: number;
  totalTokens: number;
  cost: number;
};

type SourceTooltipState = {
  row: RequestEventRow;
  x: number;
  y: number;
};

export interface RequestEventsDetailsCardProps {
  usage: unknown;
  loading: boolean;
  geminiKeys: GeminiKeyConfig[];
  claudeConfigs: ProviderKeyConfig[];
  codexConfigs: ProviderKeyConfig[];
  vertexConfigs: ProviderKeyConfig[];
  openaiProviders: OpenAIProviderConfig[];
  modelPrices: Record<string, ModelPrice>;
  onEditSource?: (path: string) => void;
}

const toNumber = (value: unknown): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return 0;
  return parsed;
};

const encodeCsv = (value: string | number): string => {
  const text = String(value ?? '');
  const trimmedLeft = text.replace(/^\s+/, '');
  const safeText = trimmedLeft && /^[=+\-@]/.test(trimmedLeft) ? `'${text}` : text;
  return `"${safeText.replace(/"/g, '""')}"`;
};

export function RequestEventsDetailsCard({
  usage,
  loading,
  geminiKeys,
  claudeConfigs,
  codexConfigs,
  vertexConfigs,
  openaiProviders,
  modelPrices,
  onEditSource,
}: RequestEventsDetailsCardProps) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const latencyHint = t('usage_stats.latency_unit_hint', {
    field: LATENCY_SOURCE_FIELD,
    unit: t('usage_stats.duration_unit_ms'),
  });

  const [modelFilter, setModelFilter] = useState(ALL_FILTER);
  const [sourceFilter, setSourceFilter] = useState(ALL_FILTER);
  const [authIndexFilter, setAuthIndexFilter] = useState(ALL_FILTER);
  const [authFileMap, setAuthFileMap] = useState<Map<string, CredentialInfo>>(new Map());
  const [expandedErrorRows, setExpandedErrorRows] = useState<Set<string>>(() => new Set());
  const [sourceTooltip, setSourceTooltip] = useState<SourceTooltipState | null>(null);

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
        vertexApiKeys: vertexConfigs,
        openaiCompatibility: openaiProviders,
      }),
    [claudeConfigs, codexConfigs, geminiKeys, openaiProviders, vertexConfigs]
  );

  const rows = useMemo<RequestEventRow[]>(() => {
    const details = collectUsageDetails(usage);

    const baseRows = details
      .map((detail, index) => {
        const timestamp = detail.timestamp;
        const timestampMs =
          typeof detail.__timestampMs === 'number' && detail.__timestampMs > 0
            ? detail.__timestampMs
            : parseTimestampMs(timestamp);
        const date = Number.isNaN(timestampMs) ? null : new Date(timestampMs);
        const sourceRaw = String(detail.source ?? '').trim();
        const authIndexRaw = detail.auth_index as unknown;
        const authIndex =
          authIndexRaw === null || authIndexRaw === undefined || authIndexRaw === ''
            ? '-'
            : String(authIndexRaw);
        const remoteIP = String(detail.remote_ip ?? '').trim() || '-';
        const sourceInfo = resolveSourceDisplay(
          sourceRaw,
          authIndexRaw,
          sourceInfoMap,
          authFileMap
        );
        const source = sourceInfo.displayName;
        const sourceKey = sourceInfo.identityKey ?? `source:${sourceRaw || source}`;
        const sourceType = sourceInfo.type;
        const model = String(detail.__modelName ?? '').trim() || '-';
        const userAgent = String(detail.user_agent ?? '').trim() || '-';
        const inputChars = Math.max(toNumber(detail.input_chars), 0);
        const buckets = getUsageTokenBuckets(detail);
        const inputTokens = buckets.inputTokens;
        const outputTokens = buckets.outputTokens;
        const reasoningTokens = buckets.reasoningTokens;
        const cachedTokens = buckets.cachedInputTokens;
        const cacheCreationTokens = buckets.cacheCreationTokens;
        const cacheWrite5mTokens = buckets.cacheWrite5mTokens;
        const cacheWrite1hTokens = buckets.cacheWrite1hTokens;
        const cacheReadTokens = buckets.cacheReadTokens;
        const totalTokens = Math.max(
          toNumber(detail.tokens?.total_tokens),
          extractTotalTokens(detail)
        );
        const cost = calculateCost(detail, modelPrices);
        const latencyMs = extractLatencyMs(detail);
        const statusCode =
          typeof detail.status_code === 'number' &&
          Number.isFinite(detail.status_code) &&
          detail.status_code > 0
            ? detail.status_code
            : null;
        const errorReason = String(detail.error_reason ?? '').trim();
        const errorMessage = String(detail.error_message ?? '').trim();

        return {
          id: `${timestamp}-${model}-${sourceKey}-${authIndex}-${index}`,
          timestamp,
          timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
          timestampLabel: date ? date.toLocaleString(i18n.language) : timestamp || '-',
          model,
          sourceKey,
          sourceRaw: sourceRaw || '-',
          source,
          sourceType,
          sourceInfo,
          authIndex,
          remoteIP,
          userAgent,
          inputChars,
          failed: detail.failed === true,
          statusCode,
          errorReason,
          errorMessage,
          latencyMs,
          inputTokens,
          outputTokens,
          reasoningTokens,
          cachedTokens,
          cacheCreationTokens,
          cacheWrite5mTokens,
          cacheWrite1hTokens,
          cacheReadTokens,
          totalTokens,
          cost,
        };
      });

    const sourceLabelKeyMap = new Map<string, Set<string>>();
    baseRows.forEach((row) => {
      const keys = sourceLabelKeyMap.get(row.source) ?? new Set<string>();
      keys.add(row.sourceKey);
      sourceLabelKeyMap.set(row.source, keys);
    });

    const buildDisambiguatedSourceLabel = (row: RequestEventRow) => {
      const labelKeyCount = sourceLabelKeyMap.get(row.source)?.size ?? 0;
      if (labelKeyCount <= 1) {
        return row.source;
      }

      if (row.authIndex !== '-') {
        return `${row.source} · ${row.authIndex}`;
      }

      if (row.sourceRaw !== '-' && row.sourceRaw !== row.source) {
        return `${row.source} · ${row.sourceRaw}`;
      }

      if (row.sourceType) {
        return `${row.source} · ${row.sourceType}`;
      }

      return `${row.source} · ${row.sourceKey}`;
    };

    return baseRows
      .map((row) => ({
        ...row,
        source: buildDisambiguatedSourceLabel(row),
      }))
      .sort((a, b) => b.timestampMs - a.timestampMs);
  }, [authFileMap, i18n.language, modelPrices, sourceInfoMap, usage]);

  const hasLatencyData = useMemo(() => rows.some((row) => row.latencyMs !== null), [rows]);

  const modelOptions = useMemo(
    () => [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      ...Array.from(new Set(rows.map((row) => row.model))).map((model) => ({
        value: model,
        label: model,
      })),
    ],
    [rows, t]
  );

  const sourceOptions = useMemo(() => {
    const optionMap = new Map<string, string>();
    rows.forEach((row) => {
      if (!optionMap.has(row.sourceKey)) {
        optionMap.set(row.sourceKey, row.source);
      }
    });

    return [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      ...Array.from(optionMap.entries()).map(([value, label]) => ({
        value,
        label,
      })),
    ];
  }, [rows, t]);

  const authIndexOptions = useMemo(
    () => [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      ...Array.from(new Set(rows.map((row) => row.authIndex))).map((authIndex) => ({
        value: authIndex,
        label: authIndex,
      })),
    ],
    [rows, t]
  );

  const modelOptionSet = useMemo(
    () => new Set(modelOptions.map((option) => option.value)),
    [modelOptions]
  );
  const sourceOptionSet = useMemo(
    () => new Set(sourceOptions.map((option) => option.value)),
    [sourceOptions]
  );
  const authIndexOptionSet = useMemo(
    () => new Set(authIndexOptions.map((option) => option.value)),
    [authIndexOptions]
  );

  const effectiveModelFilter = modelOptionSet.has(modelFilter) ? modelFilter : ALL_FILTER;
  const effectiveSourceFilter = sourceOptionSet.has(sourceFilter) ? sourceFilter : ALL_FILTER;
  const effectiveAuthIndexFilter = authIndexOptionSet.has(authIndexFilter)
    ? authIndexFilter
    : ALL_FILTER;

  const filteredRows = useMemo(
    () =>
      rows.filter((row) => {
        const modelMatched =
          effectiveModelFilter === ALL_FILTER || row.model === effectiveModelFilter;
        const sourceMatched =
          effectiveSourceFilter === ALL_FILTER || row.sourceKey === effectiveSourceFilter;
        const authIndexMatched =
          effectiveAuthIndexFilter === ALL_FILTER || row.authIndex === effectiveAuthIndexFilter;
        return modelMatched && sourceMatched && authIndexMatched;
      }),
    [effectiveAuthIndexFilter, effectiveModelFilter, effectiveSourceFilter, rows]
  );

  const renderedRows = useMemo(() => filteredRows.slice(0, MAX_RENDERED_EVENTS), [filteredRows]);

  const hasActiveFilters =
    effectiveModelFilter !== ALL_FILTER ||
    effectiveSourceFilter !== ALL_FILTER ||
    effectiveAuthIndexFilter !== ALL_FILTER;

  const handleClearFilters = () => {
    setModelFilter(ALL_FILTER);
    setSourceFilter(ALL_FILTER);
    setAuthIndexFilter(ALL_FILTER);
  };

  const handleExportCsv = () => {
    if (!filteredRows.length) return;

    const csvHeader = [
      'timestamp',
      'model',
      'source',
      'source_raw',
      'auth_index',
      'remote_ip',
      'user_agent',
      'input_chars',
      'result',
      'status_code',
      'error_reason',
      'error_message',
      ...(hasLatencyData ? ['latency_ms'] : []),
      'cost',
      'input_tokens',
      'output_tokens',
      'reasoning_tokens',
      'cached_tokens',
      'cache_creation_input_tokens',
      'cache_creation_5m_input_tokens',
      'cache_creation_1h_input_tokens',
      'cache_read_input_tokens',
      'total_tokens',
    ];

    const csvRows = filteredRows.map((row) =>
      [
        row.timestamp,
        row.model,
        row.source,
        row.sourceRaw,
        row.authIndex,
        row.remoteIP,
        row.userAgent,
        row.inputChars,
        row.failed ? 'failed' : 'success',
        row.statusCode ?? '',
        row.errorReason,
        row.errorMessage,
        ...(hasLatencyData ? [row.latencyMs ?? ''] : []),
        row.cost,
        row.inputTokens,
        row.outputTokens,
        row.reasoningTokens,
        row.cachedTokens,
        row.cacheCreationTokens,
        row.cacheWrite5mTokens,
        row.cacheWrite1hTokens,
        row.cacheReadTokens,
        row.totalTokens,
      ]
        .map((value) => encodeCsv(value))
        .join(',')
    );

    const content = [csvHeader.join(','), ...csvRows].join('\n');
    const fileTime = new Date().toISOString().replace(/[:.]/g, '-');
    downloadBlob({
      filename: `usage-events-${fileTime}.csv`,
      blob: new Blob([content], { type: 'text/csv;charset=utf-8' }),
    });
  };

  const handleExportJson = () => {
    if (!filteredRows.length) return;

    const payload = filteredRows.map((row) => ({
      timestamp: row.timestamp,
      model: row.model,
      source: row.source,
      source_raw: row.sourceRaw,
      auth_index: row.authIndex,
      remote_ip: row.remoteIP,
      user_agent: row.userAgent,
      input_chars: row.inputChars,
      failed: row.failed,
      ...(row.statusCode !== null ? { status_code: row.statusCode } : {}),
      error_reason: row.errorReason,
      error_message: row.errorMessage,
      ...(hasLatencyData && row.latencyMs !== null ? { latency_ms: row.latencyMs } : {}),
      cost: row.cost,
      tokens: {
        input_tokens: row.inputTokens,
        output_tokens: row.outputTokens,
        reasoning_tokens: row.reasoningTokens,
        cached_tokens: row.cachedTokens,
        cache_creation_input_tokens: row.cacheCreationTokens,
        cache_creation_5m_input_tokens: row.cacheWrite5mTokens,
        cache_creation_1h_input_tokens: row.cacheWrite1hTokens,
        cache_read_input_tokens: row.cacheReadTokens,
        total_tokens: row.totalTokens,
      },
    }));

    const content = JSON.stringify(payload, null, 2);
    const fileTime = new Date().toISOString().replace(/[:.]/g, '-');
    downloadBlob({
      filename: `usage-events-${fileTime}.json`,
      blob: new Blob([content], { type: 'application/json;charset=utf-8' }),
    });
  };

  const buildSourceTooltipRows = (row: RequestEventRow) =>
    [
      [t('usage_stats.request_events_source'), row.source],
      row.sourceType ? [t('common.status'), row.sourceType] : null,
      row.sourceInfo.baseUrl ? [t('common.base_url'), row.sourceInfo.baseUrl] : null,
      row.sourceInfo.priority !== undefined
        ? [t('common.priority'), String(row.sourceInfo.priority)]
        : null,
      row.sourceInfo.enabled !== undefined
        ? [t('ai_providers.config_toggle_label'), row.sourceInfo.enabled ? t('common.yes') : t('common.no')]
        : null,
      row.sourceInfo.proxyUrl ? [t('common.proxy_url'), row.sourceInfo.proxyUrl] : null,
      row.sourceInfo.apiKeyCount !== undefined
        ? [t('ai_providers.openai_keys_count'), String(row.sourceInfo.apiKeyCount)]
        : null,
      row.sourceInfo.modelCount !== undefined
        ? [t('ai_providers.openai_models_count'), String(row.sourceInfo.modelCount)]
        : null,
      row.sourceInfo.headerCount !== undefined
        ? [t('common.custom_headers_label'), String(row.sourceInfo.headerCount)]
        : null,
    ].filter(Boolean) as Array<[string, string]>;

  const showSourceTooltip = (row: RequestEventRow, event: MouseEvent<HTMLElement>) => {
    setSourceTooltip({ row, x: event.clientX, y: event.clientY });
  };

  const moveSourceTooltip = (row: RequestEventRow, event: MouseEvent<HTMLElement>) => {
    setSourceTooltip((current) =>
      current?.row.id === row.id ? { row, x: event.clientX, y: event.clientY } : current
    );
  };

  const hideSourceTooltip = () => {
    setSourceTooltip(null);
  };

  const handleSourceClick = (row: RequestEventRow) => {
    if (!row.sourceInfo.editPath) return;
    if (onEditSource) {
      onEditSource(row.sourceInfo.editPath);
      return;
    }
    navigate(row.sourceInfo.editPath, { state: { fromUsage: true } });
  };

  return (
    <Card
      title={t('usage_stats.request_events_title')}
      extra={
        <div className={styles.requestEventsActions}>
          <Button
            variant="ghost"
            size="sm"
            onClick={handleClearFilters}
            disabled={!hasActiveFilters}
          >
            {t('usage_stats.clear_filters')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleExportCsv}
            disabled={filteredRows.length === 0}
          >
            {t('usage_stats.export_csv')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleExportJson}
            disabled={filteredRows.length === 0}
          >
            {t('usage_stats.export_json')}
          </Button>
        </div>
      }
    >
      <div className={styles.requestEventsToolbar}>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_model')}
          </span>
          <Select
            value={effectiveModelFilter}
            options={modelOptions}
            onChange={setModelFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_model')}
            fullWidth={false}
          />
        </div>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_source')}
          </span>
          <Select
            value={effectiveSourceFilter}
            options={sourceOptions}
            onChange={setSourceFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_source')}
            fullWidth={false}
          />
        </div>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_auth_index')}
          </span>
          <Select
            value={effectiveAuthIndexFilter}
            options={authIndexOptions}
            onChange={setAuthIndexFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_auth_index')}
            fullWidth={false}
          />
        </div>
      </div>

      {loading && rows.length === 0 ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <EmptyState
          title={t('usage_stats.request_events_empty_title')}
          description={t('usage_stats.request_events_empty_desc')}
        />
      ) : filteredRows.length === 0 ? (
        <EmptyState
          title={t('usage_stats.request_events_no_result_title')}
          description={t('usage_stats.request_events_no_result_desc')}
        />
      ) : (
        <>
          <div className={styles.requestEventsMeta}>
            <span>{t('usage_stats.request_events_count', { count: filteredRows.length })}</span>
            {hasLatencyData && <span className={styles.requestEventsLimitHint}>{latencyHint}</span>}
            {filteredRows.length > MAX_RENDERED_EVENTS && (
              <span className={styles.requestEventsLimitHint}>
                {t('usage_stats.request_events_limit_hint', {
                  shown: MAX_RENDERED_EVENTS,
                  total: filteredRows.length,
                })}
              </span>
            )}
          </div>

          <div className={styles.requestEventsTableWrapper}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_stats.request_events_timestamp')}</th>
                  <th>{t('usage_stats.model_name')}</th>
                  <th>{t('usage_stats.request_events_source')}</th>
                  <th>{t('usage_stats.request_events_auth_index')}</th>
                  <th>{t('usage_stats.request_events_remote_ip')}</th>
                  <th>{t('usage_stats.request_events_user_agent')}</th>
                  <th>{t('usage_stats.request_events_input_chars')}</th>
                  <th>{t('usage_stats.request_events_result')}</th>
                  <th>{t('logs.trace_status_code')}</th>
                  <th>{t('common.error')}</th>
                  {hasLatencyData && <th title={latencyHint}>{t('usage_stats.time')}</th>}
                  <th>{t('usage_stats.request_events_cost')}</th>
                  <th>{t('usage_stats.input_tokens')}</th>
                  <th>{t('usage_stats.output_tokens')}</th>
                  <th>{t('usage_stats.reasoning_tokens')}</th>
                  <th>{t('usage_stats.cached_tokens')}</th>
                  <th>{t('usage_stats.cache_creation_tokens')}</th>
                  <th>{t('usage_stats.cache_read_tokens')}</th>
                  <th>{t('usage_stats.total_tokens')}</th>
                </tr>
              </thead>
              <tbody>
                {renderedRows.map((row) => (
                  <tr key={row.id}>
                    <td title={row.timestamp} className={styles.requestEventsTimestamp}>
                      {row.timestampLabel}
                    </td>
                    <td className={styles.modelCell}>{row.model}</td>
                    <td className={styles.requestEventsSourceCell} title={row.source}>
                      {row.sourceInfo.editPath ? (
                        <button
                          type="button"
                          className={styles.requestEventsSourceButton}
                          onClick={() => handleSourceClick(row)}
                          onMouseEnter={(event) => showSourceTooltip(row, event)}
                          onMouseMove={(event) => moveSourceTooltip(row, event)}
                          onMouseLeave={hideSourceTooltip}
                        >
                          {row.source}
                        </button>
                      ) : (
                        <span
                          onMouseEnter={(event) => showSourceTooltip(row, event)}
                          onMouseMove={(event) => moveSourceTooltip(row, event)}
                          onMouseLeave={hideSourceTooltip}
                        >
                          {row.source}
                        </span>
                      )}
                      {row.sourceType && (
                        <span className={styles.credentialType}>{row.sourceType}</span>
                      )}
                    </td>
                    <td className={styles.requestEventsAuthIndex} title={row.authIndex}>
                      {row.authIndex}
                    </td>
                    <td className={styles.requestEventsAuthIndex} title={row.remoteIP}>
                      {row.remoteIP}
                    </td>
                    <td className={styles.requestEventsSourceCell} title={row.userAgent}>
                      {row.userAgent}
                    </td>
                    <td>{row.inputChars.toLocaleString()}</td>
                    <td>
                      <span
                        className={
                          row.failed
                            ? styles.requestEventsResultFailed
                            : styles.requestEventsResultSuccess
                        }
                      >
                        {row.failed ? t('stats.failure') : t('stats.success')}
                      </span>
                    </td>
                    <td>{formatRequestEventStatusCode(row.statusCode)}</td>
                    <td
                      className={styles.requestEventsErrorCell}
                      title={row.errorMessage || undefined}
                    >
                      {(() => {
                        const errorText = row.errorReason || row.errorMessage;
                        if (!errorText) {
                          return '';
                        }
                        const expanded = expandedErrorRows.has(row.id);
                        const display = getRequestEventErrorDisplay(errorText, expanded);
                        return (
                          <>
                            <span className={styles.requestEventsErrorText}>{display.text}</span>
                            {display.needsToggle && (
                              <button
                                type="button"
                                aria-expanded={expanded}
                                className={styles.requestEventsErrorToggle}
                                onClick={() => {
                                  setExpandedErrorRows((current) => {
                                    const next = new Set(current);
                                    if (next.has(row.id)) {
                                      next.delete(row.id);
                                    } else {
                                      next.add(row.id);
                                    }
                                    return next;
                                  });
                                }}
                              >
                                {expanded ? t('common.collapse') : t('common.expand')}
                              </button>
                            )}
                          </>
                        );
                      })()}
                    </td>
                    {hasLatencyData && (
                      <td className={styles.durationCell}>{formatDurationMs(row.latencyMs)}</td>
                    )}
                    <td>{formatRequestEventCost(row.cost)}</td>
                    <td>{row.inputTokens.toLocaleString()}</td>
                    <td>{row.outputTokens.toLocaleString()}</td>
                    <td>{row.reasoningTokens.toLocaleString()}</td>
                    <td>{row.cachedTokens.toLocaleString()}</td>
                    <td>{row.cacheCreationTokens.toLocaleString()}</td>
                    <td>{row.cacheReadTokens.toLocaleString()}</td>
                    <td>{row.totalTokens.toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {sourceTooltip && typeof document !== 'undefined'
            ? createPortal(
                <div
                  className={styles.requestEventsSourceTooltip}
                  style={{
                    left: Math.max(12, Math.min(sourceTooltip.x + 14, window.innerWidth - 300)),
                    top: Math.max(12, Math.min(sourceTooltip.y + 14, window.innerHeight - 220)),
                  }}
                >
                  {buildSourceTooltipRows(sourceTooltip.row).map(([label, value]) => (
                    <div key={label} className={styles.requestEventsSourceTooltipRow}>
                      <span>{label}</span>
                      <strong>{value}</strong>
                    </div>
                  ))}
                  {sourceTooltip.row.sourceInfo.editPath && (
                    <div className={styles.requestEventsSourceTooltipHint}>{t('common.edit')}</div>
                  )}
                </div>,
                document.body
              )
            : null}
        </>
      )}
    </Card>
  );
}
