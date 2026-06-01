import { apiClient } from './client';
import type { AuthFilesGroupConfig } from '@/types';

const normalizeAuthFilesGroup = (payload: unknown): AuthFilesGroupConfig => {
  const source =
    payload && typeof payload === 'object' && !Array.isArray(payload)
      ? (payload as Record<string, unknown>)
      : {};

  const priorityRaw = source.priority ?? 10;
  const priority =
    typeof priorityRaw === 'number' && Number.isFinite(priorityRaw)
      ? priorityRaw
      : typeof priorityRaw === 'string' && priorityRaw.trim() !== ''
        ? Number(priorityRaw)
        : 10;

  return {
    enabled: source.enabled !== false,
    priority: Number.isFinite(priority) ? priority : 10,
    proxyUrl: String(source['proxy-url'] ?? source.proxyUrl ?? source['proxy_url'] ?? '').trim(),
  };
};

export const authFilesGroupApi = {
  async get(): Promise<AuthFilesGroupConfig> {
    return normalizeAuthFilesGroup(await apiClient.get('/auth-files-group'));
  },

  update: (value: AuthFilesGroupConfig) =>
    apiClient.patch('/auth-files-group', {
      enabled: value.enabled,
      priority: value.priority,
      'proxy-url': value.proxyUrl,
    }),
};
