export const AI_PROVIDER_EDIT_MODAL_CLOSE_EVENT = 'ai-provider-edit-modal-close';
export const AI_PROVIDER_EDIT_MODAL_NAVIGATE_EVENT = 'ai-provider-edit-modal-navigate';
export const AI_PROVIDER_EDIT_MODAL_OPEN_EVENT = 'ai-provider-edit-modal-open';
export const AI_PROVIDER_EDIT_MODAL_ACTION_ROOT_ID = 'ai-provider-edit-modal-action-root';

export type AiProviderEditModalState = {
  providerEditModal?: boolean;
};

export const isAiProviderEditModalState = (state: unknown): boolean =>
  Boolean(state && typeof state === 'object' && (state as AiProviderEditModalState).providerEditModal);

export const requestAiProviderEditModalClose = () => {
  window.dispatchEvent(new CustomEvent(AI_PROVIDER_EDIT_MODAL_CLOSE_EVENT));
};

export const requestAiProviderEditModalOpen = (path: string) => {
  window.dispatchEvent(new CustomEvent<string>(AI_PROVIDER_EDIT_MODAL_OPEN_EVENT, { detail: path }));
};

export const requestAiProviderEditModalNavigate = (path: string) => {
  window.dispatchEvent(
    new CustomEvent<string>(AI_PROVIDER_EDIT_MODAL_NAVIGATE_EVENT, { detail: path })
  );
};
