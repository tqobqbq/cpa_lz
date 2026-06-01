import type { ModelPrice } from '../usage';

export function normalizeModelPriceKey(modelName: string): string {
  const trimmed = modelName.trim();
  const suffixStart = trimmed.lastIndexOf('(');
  if (suffixStart > 0 && trimmed.endsWith(')')) {
    return trimmed.slice(0, suffixStart).trim().toLowerCase();
  }
  return trimmed.toLowerCase();
}

export function resolveModelPrice(
  modelName: string,
  modelPrices: Record<string, ModelPrice>
): ModelPrice | undefined {
  if (!modelName) {
    return undefined;
  }

  if (modelPrices[modelName]) {
    return modelPrices[modelName];
  }

  const trimmedModel = modelName.trim();
  if (trimmedModel && modelPrices[trimmedModel]) {
    return modelPrices[trimmedModel];
  }

  const normalizedModel = normalizeModelPriceKey(modelName);
  if (!normalizedModel) {
    return undefined;
  }

  return Object.entries(modelPrices).find(
    ([candidate]) => normalizeModelPriceKey(candidate) === normalizedModel
  )?.[1];
}
