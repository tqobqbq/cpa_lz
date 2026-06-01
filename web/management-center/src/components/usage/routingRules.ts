export type RoutingUserAgentRuleValue = {
  match?: 'equals' | 'contains';
  value?: string;
};

export type RoutingInputCharsRuleValue = {
  operator?: 'gt' | 'lt';
  value?: number;
};

export type RoutingRuleValue = {
  provider: string;
  userAgent?: RoutingUserAgentRuleValue;
  inputChars?: RoutingInputCharsRuleValue;
};

export function createEmptyRoutingRule(): RoutingRuleValue {
  return {
    provider: '',
  };
}

export function normalizeSeenUserAgents(values: unknown): string[] {
  if (!Array.isArray(values)) return [];
  const unique = new Set<string>();
  values.forEach((value) => {
    if (typeof value !== 'string') return;
    const trimmed = value.trim();
    if (!trimmed) return;
    unique.add(trimmed);
  });
  return Array.from(unique).sort((left, right) => left.localeCompare(right));
}

export function sanitizeRoutingRules(values: RoutingRuleValue[]): RoutingRuleValue[] {
  if (!Array.isArray(values)) return [];

  return values.reduce<RoutingRuleValue[]>((acc, item) => {
    const provider = String(item?.provider ?? '')
      .trim()
      .toLowerCase();
    if (!provider) {
      return acc;
    }

    const userAgentMatch = item?.userAgent?.match === 'equals' ? 'equals' : item?.userAgent?.match === 'contains' ? 'contains' : undefined;
    const userAgentValue = String(item?.userAgent?.value ?? '').trim();
    const inputOperator = item?.inputChars?.operator === 'lt' ? 'lt' : item?.inputChars?.operator === 'gt' ? 'gt' : undefined;
    const rawValue = Number(item?.inputChars?.value);
    const inputCharsValue = Number.isFinite(rawValue) ? Math.max(0, Math.trunc(rawValue)) : undefined;

    const nextRule: RoutingRuleValue = { provider };
    if (userAgentMatch && userAgentValue) {
      nextRule.userAgent = {
        match: userAgentMatch,
        value: userAgentValue,
      };
    }
    if (inputOperator && inputCharsValue !== undefined) {
      nextRule.inputChars = {
        operator: inputOperator,
        value: inputCharsValue,
      };
    }

    acc.push(nextRule);
    return acc;
  }, []);
}
