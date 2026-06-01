export type OutputFilterTestInput = {
  enabled: boolean;
  maxLength: string;
  keywordsText: string;
  sampleText: string;
};

export type OutputFilterTestResult =
  | { status: 'disabled' }
  | { status: 'empty-sample' }
  | { status: 'invalid-max-length' }
  | { status: 'too-long'; maxLength: number; actualLength: number }
  | { status: 'matched'; pattern: string }
  | { status: 'no-match'; invalidPatterns: string[] }
  | { status: 'invalid-patterns'; invalidPatterns: string[] };

export function splitOutputFilterKeywords(keywordsText: string): string[] {
  return keywordsText
    .split('\n')
    .map((keyword) => keyword.trim())
    .filter(Boolean);
}

const getUtf8ByteLength = (value: string): number => new TextEncoder().encode(value).length;

const asRecord = (value: unknown): Record<string, unknown> | null => {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
};

const asArray = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);

const addText = (parts: string[], value: unknown) => {
  if (typeof value === 'string' && value) {
    parts.push(value);
  }
};

const collectOpenAIChatText = (root: Record<string, unknown>, parts: string[]) => {
  for (const choiceRaw of asArray(root.choices)) {
    const choice = asRecord(choiceRaw);
    if (!choice) continue;
    const message = asRecord(choice.message);
    if (message) {
      addText(parts, message.content);
      addText(parts, message.reasoning_content);
      addText(parts, message.reasoning);
    }
    const delta = asRecord(choice.delta);
    if (delta) {
      addText(parts, delta.content);
      addText(parts, delta.reasoning_content);
      addText(parts, delta.reasoning);
    }
    addText(parts, choice.text);
  }
};

const collectOpenAIResponseItem = (item: Record<string, unknown> | null, parts: string[]) => {
  if (!item) return;
  for (const contentRaw of asArray(item.content)) {
    const content = asRecord(contentRaw);
    addText(parts, content?.text);
  }
  for (const summaryRaw of asArray(item.summary)) {
    const summary = asRecord(summaryRaw);
    addText(parts, summary?.text);
  }
};

const collectOpenAIResponsesText = (root: Record<string, unknown>, parts: string[]) => {
  const response = asRecord(root.response);
  if (response) {
    collectOpenAIResponsesText(response, parts);
  }

  if (root.type === 'response.output_text.delta') {
    addText(parts, root.delta);
  }
  if (root.type === 'response.output_text.done') {
    addText(parts, root.text);
  }

  addText(parts, root.output_text);
  for (const itemRaw of asArray(root.output)) {
    collectOpenAIResponseItem(asRecord(itemRaw), parts);
  }
  collectOpenAIResponseItem(asRecord(root.item), parts);
  addText(parts, asRecord(root.part)?.text);
};

const collectClaudeText = (root: Record<string, unknown>, parts: string[]) => {
  for (const blockRaw of asArray(root.content)) {
    const block = asRecord(blockRaw);
    addText(parts, block?.text);
    addText(parts, block?.thinking);
  }
  const contentBlock = asRecord(root.content_block);
  addText(parts, contentBlock?.text);
  addText(parts, contentBlock?.thinking);
  const delta = asRecord(root.delta);
  addText(parts, delta?.text);
  addText(parts, delta?.thinking);
};

const collectGeminiText = (root: Record<string, unknown>, parts: string[]) => {
  const response = asRecord(root.response);
  if (response) {
    collectGeminiText(response, parts);
  }
  for (const candidateRaw of asArray(root.candidates)) {
    const candidate = asRecord(candidateRaw);
    const content = asRecord(candidate?.content);
    for (const partRaw of asArray(content?.parts)) {
      const part = asRecord(partRaw);
      addText(parts, part?.text);
    }
  }
};

const extractDownstreamText = (sampleText: string): string | null => {
  let parsed: unknown;
  try {
    parsed = JSON.parse(sampleText);
  } catch {
    return null;
  }
  const root = asRecord(parsed);
  if (!root) return null;

  const parts: string[] = [];
  collectOpenAIChatText(root, parts);
  collectOpenAIResponsesText(root, parts);
  collectClaudeText(root, parts);
  collectGeminiText(root, parts);
  return parts.length > 0 ? parts.join('') : null;
};

export function evaluateOutputFilterTest(input: OutputFilterTestInput): OutputFilterTestResult {
  if (!input.enabled) return { status: 'disabled' };

  const trimmedSample = input.sampleText.trim();
  if (!trimmedSample) return { status: 'empty-sample' };

  const maxLengthText = input.maxLength.trim();
  if (!/^\d+$/.test(maxLengthText)) return { status: 'invalid-max-length' };

  const maxLength = Number(maxLengthText);
  if (!Number.isFinite(maxLength) || maxLength <= 0) return { status: 'invalid-max-length' };

  const downstreamText = extractDownstreamText(trimmedSample);
  if (!downstreamText) {
    return { status: 'no-match', invalidPatterns: [] };
  }

  const downstreamTextLength = getUtf8ByteLength(downstreamText);
  if (downstreamTextLength >= maxLength) {
    return { status: 'too-long', maxLength, actualLength: downstreamTextLength };
  }

  const invalidPatterns: string[] = [];
  for (const pattern of splitOutputFilterKeywords(input.keywordsText)) {
    let expression: RegExp;
    try {
      expression = new RegExp(pattern, 'i');
    } catch {
      invalidPatterns.push(pattern);
      continue;
    }
    if (expression.test(downstreamText)) {
      return { status: 'matched', pattern };
    }
  }

  if (invalidPatterns.length > 0) return { status: 'invalid-patterns', invalidPatterns };
  return { status: 'no-match', invalidPatterns };
}
