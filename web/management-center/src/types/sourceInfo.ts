export type SourceInfo = {
  displayName: string;
  type: string;
  identityKey?: string;
  editPath?: string;
  baseUrl?: string;
  priority?: number;
  enabled?: boolean;
  proxyUrl?: string;
  apiKeyCount?: number;
  modelCount?: number;
  headerCount?: number;
};

export type CredentialInfo = {
  name: string;
  type: string;
};
