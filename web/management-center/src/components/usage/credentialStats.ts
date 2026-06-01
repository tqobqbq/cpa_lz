export type CredentialStatsRecord = {
  identityKey: string;
  displayName: string;
  type: string;
  editPath?: string;
  failed: boolean;
  success?: number;
  failure?: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  totalLatencyMs?: number;
  latencySampleCount?: number;
  cost: number;
};

export type CredentialStatsRow = {
  key: string;
  displayName: string;
  type: string;
  editPath?: string;
  success: number;
  failure: number;
  total: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheCreationTokens: number;
  cacheReadTokens: number;
  averageLatencyMs: number | null;
  totalLatencyMs: number | null;
  latencySampleCount: number;
  cost: number;
};

export function buildCredentialStatsRows(records: CredentialStatsRecord[]): CredentialStatsRow[] {
  const rowMap = new Map<string, CredentialStatsRow>();

  for (const record of records) {
    const key = record.identityKey || record.displayName;
    const row =
      rowMap.get(key) ??
      ({
        key,
        displayName: record.displayName,
        type: record.type,
        editPath: record.editPath,
        success: 0,
        failure: 0,
        total: 0,
        successRate: 100,
        inputTokens: 0,
        outputTokens: 0,
        reasoningTokens: 0,
        cachedTokens: 0,
        cacheCreationTokens: 0,
        cacheReadTokens: 0,
        averageLatencyMs: null,
        totalLatencyMs: null,
        latencySampleCount: 0,
        cost: 0,
      } satisfies CredentialStatsRow);

    if (!row.editPath && record.editPath) {
      row.editPath = record.editPath;
    }

    if (typeof record.success === 'number' || typeof record.failure === 'number') {
      row.success += Math.max(record.success ?? 0, 0);
      row.failure += Math.max(record.failure ?? 0, 0);
    } else if (record.failed) {
      row.failure += 1;
    } else {
      row.success += 1;
    }

    row.total = row.success + row.failure;
    row.successRate = row.total > 0 ? (row.success / row.total) * 100 : 100;
    row.inputTokens += record.inputTokens;
    row.outputTokens += record.outputTokens;
    row.reasoningTokens += record.reasoningTokens;
    row.cachedTokens += record.cachedTokens;
    row.cacheCreationTokens += record.cacheCreationTokens;
    row.cacheReadTokens += record.cacheReadTokens;
    if ((record.latencySampleCount ?? 0) > 0) {
      row.totalLatencyMs = (row.totalLatencyMs ?? 0) + (record.totalLatencyMs ?? 0);
      row.latencySampleCount += record.latencySampleCount ?? 0;
      row.averageLatencyMs =
        row.latencySampleCount > 0 ? (row.totalLatencyMs ?? 0) / row.latencySampleCount : null;
    }
    row.cost += record.cost;

    rowMap.set(key, row);
  }

  return Array.from(rowMap.values()).sort((left, right) => right.total - left.total);
}
