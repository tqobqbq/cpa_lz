import type { ProviderCooldownConfig } from '@/types';

type ProviderCooldownInput = Partial<Record<keyof ProviderCooldownConfig, unknown>>;

export const DEFAULT_PROVIDER_COOLDOWN: Required<ProviderCooldownConfig> = {
  start: 1,
  exponent: 1.2,
  max: 10,
};

const toFiniteNumber = (value: unknown): number | undefined => {
  if (value === undefined || value === null || String(value).trim() === '') return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
};

export const normalizeProviderCooldown = (
  value?: ProviderCooldownInput | null
): ProviderCooldownConfig | undefined => {
  if (!value || typeof value !== 'object') return undefined;

  const startRaw = toFiniteNumber(value.start);
  const exponentRaw = toFiniteNumber(value.exponent);
  const maxRaw = toFiniteNumber(value.max);
  const normalized: ProviderCooldownConfig = {};

  if (startRaw !== undefined) normalized.start = Math.max(1, Math.trunc(startRaw));
  if (exponentRaw !== undefined) normalized.exponent = exponentRaw > 0 ? exponentRaw : 1.2;
  if (maxRaw !== undefined) normalized.max = Math.max(1, Math.trunc(maxRaw));

  return Object.keys(normalized).length ? normalized : undefined;
};

export const withProviderCooldownDefaults = (
  value?: ProviderCooldownConfig | null
): Required<ProviderCooldownConfig> => {
  const normalized = normalizeProviderCooldown(value);
  return {
    start: normalized?.start ?? DEFAULT_PROVIDER_COOLDOWN.start,
    exponent: normalized?.exponent ?? DEFAULT_PROVIDER_COOLDOWN.exponent,
    max: normalized?.max ?? DEFAULT_PROVIDER_COOLDOWN.max,
  };
};

export const areProviderCooldownConfigsEqual = (
  left?: ProviderCooldownConfig | null,
  right?: ProviderCooldownConfig | null
) => {
  const a = normalizeProviderCooldown(left);
  const b = normalizeProviderCooldown(right);
  return (
    (a?.start ?? null) === (b?.start ?? null) &&
    (a?.exponent ?? null) === (b?.exponent ?? null) &&
    (a?.max ?? null) === (b?.max ?? null)
  );
};
