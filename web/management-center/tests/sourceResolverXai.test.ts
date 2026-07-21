import { describe, expect, test } from 'bun:test';
import { buildSourceInfoMap, resolveSourceDisplay } from '../src/utils/sourceResolver';

describe('xAI usage source resolution', () => {
  const authFiles = new Map<string, { name: string; type: string }>();
  const sourceMap = buildSourceInfoMap({
    xaiApiKeys: [
      {
        apiKey: 'xai-secret-value',
        prefix: 'team-xai',
        baseUrl: 'https://api.x.ai/v1',
        authIndex: 'xai:apikey:1',
      },
    ],
  });

  test('resolves xAI usage by auth index and links to the workbench editor', () => {
    expect(resolveSourceDisplay('', 'xai:apikey:1', sourceMap, authFiles)).toMatchObject({
      displayName: 'team-xai',
      type: 'xai',
      identityKey: 'xai:0',
      editPath: '/ai-providers?edit=xai:0',
      baseUrl: 'https://api.x.ai/v1',
    });
  });

  test('resolves xAI usage from provider and secret source forms', () => {
    expect(resolveSourceDisplay('xai#0', null, sourceMap, authFiles).identityKey).toBe('xai:0');
    expect(
      resolveSourceDisplay('xai-secret-value', null, sourceMap, authFiles).identityKey
    ).toBe('xai:0');
  });
});
