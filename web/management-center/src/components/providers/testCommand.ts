export type ParsedApiCallCommand = {
  method: string;
  url: string;
  header: Record<string, string>;
  data?: string;
};

function tokenizeShellCommand(command: string): string[] {
  const tokens: string[] = [];
  let current = '';
  let quote: "'" | '"' | '' = '';

  for (let index = 0; index < command.length; index += 1) {
    const char = command[index];
    const next = command[index + 1];

    if (char === '\\' && next === '\n') {
      index += 1;
      continue;
    }

    if (!quote && /\s/.test(char)) {
      if (current) {
        tokens.push(current);
        current = '';
      }
      continue;
    }

    if (!quote && (char === "'" || char === '"')) {
      quote = char;
      continue;
    }

    if (quote && char === quote) {
      quote = '';
      continue;
    }

    if (char === '\\' && quote !== "'") {
      if (next !== undefined) {
        current += next;
        index += 1;
        continue;
      }
    }

    current += char;
  }

  if (current) {
    tokens.push(current);
  }

  return tokens;
}

export function parseApiCallCommand(command: string): ParsedApiCallCommand {
  const tokens = tokenizeShellCommand(command.trim());
  if (tokens.length === 0 || tokens[0] !== 'curl') {
    throw new Error('Invalid curl command');
  }

  let method = '';
  let url = '';
  let data = '';
  const header: Record<string, string> = {};

  for (let index = 1; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === '-X' || token === '--request') {
      method = String(tokens[index + 1] ?? '').toUpperCase();
      index += 1;
      continue;
    }
    if (token === '-H' || token === '--header') {
      const rawHeader = String(tokens[index + 1] ?? '');
      index += 1;
      const separatorIndex = rawHeader.indexOf(':');
      if (separatorIndex <= 0) {
        continue;
      }
      const key = rawHeader.slice(0, separatorIndex).trim();
      const value = rawHeader.slice(separatorIndex + 1).trim();
      if (key) {
        header[key] = value;
      }
      continue;
    }
    if (
      token === '-d' ||
      token === '--data' ||
      token === '--data-raw' ||
      token === '--data-binary'
    ) {
      data = String(tokens[index + 1] ?? '');
      index += 1;
      continue;
    }
    if (!token.startsWith('-') && !url) {
      url = token;
    }
  }

  if (!method) {
    method = data ? 'POST' : 'GET';
  }
  if (!url) {
    throw new Error('Missing request URL');
  }

  return {
    method,
    url,
    header,
    ...(data ? { data } : {}),
  };
}
