import { parseTimestampMs } from '../../utils/timestamp.ts';

type AuthFileLike = Record<string, unknown> & {
  name?: string;
  modified?: unknown;
  modtime?: unknown;
};

type AuthFilesSortMode = 'default' | 'az' | 'priority' | 'modified';
export type AuthFilesStatusAction = 'enable' | 'disable' | 'invert';
export type AuthFilesStatusUpdate = {
  name: string;
  enabled: boolean;
};

type PaginationOptions = {
  page: number;
  pageSize: number;
  showAll?: boolean;
};

type PaginationResult<T> = {
  items: T[];
  totalPages: number;
  currentPage: number;
};

function toTimestampMs(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value < 1_000_000_000_000 ? value * 1000 : value;
  }
  const parsed = parseTimestampMs(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function getAuthFilesModifiedTimestampMs(item: AuthFileLike): number {
  return toTimestampMs(
    item.modified ??
      item['modified'] ??
      item.modtime ??
      item['modtime'] ??
      item['lastModified'] ??
      item['last-modified']
  );
}

export function sortAuthFilesByMode<T extends AuthFileLike>(
  items: T[],
  sortMode: AuthFilesSortMode
): T[] {
  const sorted = [...items];
  switch (sortMode) {
    case 'az':
      sorted.sort((left, right) => String(left.name ?? '').localeCompare(String(right.name ?? '')));
      break;
    case 'priority':
      sorted.sort((left, right) => Number(right.priority ?? 0) - Number(left.priority ?? 0));
      break;
    case 'modified':
      sorted.sort((left, right) => {
        const timestampDiff =
          getAuthFilesModifiedTimestampMs(right) - getAuthFilesModifiedTimestampMs(left);
        if (timestampDiff !== 0) {
          return timestampDiff;
        }
        return String(left.name ?? '').localeCompare(String(right.name ?? ''));
      });
      break;
    default:
      break;
  }
  return sorted;
}

export function paginateAuthFiles<T>(items: T[], options: PaginationOptions): PaginationResult<T> {
  if (options.showAll) {
    return {
      items: [...items],
      totalPages: 1,
      currentPage: 1,
    };
  }

  const pageSize = Math.max(1, Math.trunc(options.pageSize));
  const totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  const currentPage = Math.min(Math.max(1, Math.trunc(options.page)), totalPages);
  const start = (currentPage - 1) * pageSize;

  return {
    items: items.slice(start, start + pageSize),
    totalPages,
    currentPage,
  };
}

function isRuntimeOnlyAuthFileLike(item: AuthFileLike): boolean {
  const value = item.runtimeOnly ?? item['runtime_only'];
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') return value.trim().toLowerCase() === 'true';
  return false;
}

export function buildAuthFilesStatusUpdates<T extends AuthFileLike>(
  items: T[],
  action: AuthFilesStatusAction
): AuthFilesStatusUpdate[] {
  const seen = new Set<string>();
  const updates: AuthFilesStatusUpdate[] = [];

  items.forEach((item) => {
    const name = String(item.name ?? '').trim();
    if (!name || seen.has(name) || isRuntimeOnlyAuthFileLike(item)) return;
    seen.add(name);

    updates.push({
      name,
      enabled: action === 'invert' ? item.disabled === true : action === 'enable',
    });
  });

  return updates;
}
