export function resolveProviderTestModel(
  selectedModel: string,
  defaultModel: string | undefined,
  availableModels: string[]
): string {
  const selected = selectedModel.trim();
  if (selected) return selected;

  const configuredModel = String(availableModels[0] ?? '').trim();
  if (configuredModel) return configuredModel;

  return String(defaultModel ?? '').trim();
}

export function buildProviderTestModelPlaceholder(defaultModel: string | undefined): string {
  const fallback = String(defaultModel ?? '').trim();
  return fallback || '<test-model>';
}

export type ProviderTestModelOption = {
  value: string;
  label: string;
};

export function buildProviderTestModelOptions(
  entries: Array<{ name?: string; alias?: string }>,
  defaultModel: string | undefined,
  labels: {
    defaultModel: (model: string) => string;
    firstAvailable: string;
  }
): ProviderTestModelOption[] {
  const seen = new Set<string>();
  const options = entries.reduce<ProviderTestModelOption[]>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    if (!name || seen.has(name)) return acc;
    seen.add(name);
    const alias = String(entry?.alias ?? '').trim();
    acc.push({
      value: name,
      label: alias && alias !== name ? `${name} (${alias})` : name,
    });
    return acc;
  }, []);

  if (options.length > 0) {
    return [{ value: '', label: labels.firstAvailable }, ...options];
  }
  const fallback = String(defaultModel ?? '').trim();
  if (fallback) {
    return [{ value: '', label: labels.defaultModel(fallback) }];
  }
  return options;
}

export function syncEditableTestCommand(
  currentCommand: string,
  previousGeneratedCommand: string,
  nextGeneratedCommand: string
): string {
  const current = currentCommand.trim();
  const previousGenerated = previousGeneratedCommand.trim();
  if (!current || current === previousGenerated) {
    return nextGeneratedCommand;
  }
  return currentCommand;
}

export function resolveProviderTestExecutionCommand(
  currentCommand: string,
  generatedCommand: string,
  mode: 'generated' | 'edited'
): string {
  if (mode === 'generated') {
    return generatedCommand.trim();
  }
  return currentCommand.trim();
}
