export const ERROR_TEXT_PREVIEW_LENGTH = 120;

export function formatRequestEventStatusCode(statusCode: number | null | undefined): string {
  if (typeof statusCode !== 'number' || !Number.isFinite(statusCode) || statusCode <= 0) {
    return '';
  }
  return String(statusCode);
}

export function formatRequestEventCost(cost: number | null | undefined): string {
  if (typeof cost !== 'number' || !Number.isFinite(cost) || cost <= 0) {
    return '--';
  }

  if (cost < 0.01) {
    return `$${cost.toFixed(6)}`;
  }

  return `$${cost.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;
}

export function getRequestEventErrorDisplay(
  errorText: string,
  expanded: boolean
): { text: string; needsToggle: boolean } {
  const text = errorText.trim();
  if (text.length <= ERROR_TEXT_PREVIEW_LENGTH) {
    return { text, needsToggle: false };
  }
  if (expanded) {
    return { text, needsToggle: true };
  }
  return {
    text: `${text.slice(0, ERROR_TEXT_PREVIEW_LENGTH)}...`,
    needsToggle: true,
  };
}
