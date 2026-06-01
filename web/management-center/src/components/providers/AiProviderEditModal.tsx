import { useEffect } from 'react';
import { useRoutes } from 'react-router-dom';
import { Modal } from '@/components/ui/Modal';
import { AiProvidersAmpcodeEditPage } from '@/pages/AiProvidersAmpcodeEditPage';
import { AiProvidersClaudeEditLayout } from '@/pages/AiProvidersClaudeEditLayout';
import { AiProvidersClaudeEditPage } from '@/pages/AiProvidersClaudeEditPage';
import { AiProvidersClaudeModelsPage } from '@/pages/AiProvidersClaudeModelsPage';
import { AiProvidersCodexEditPage } from '@/pages/AiProvidersCodexEditPage';
import { AiProvidersGeminiEditPage } from '@/pages/AiProvidersGeminiEditPage';
import { AiProvidersOpenAIEditLayout } from '@/pages/AiProvidersOpenAIEditLayout';
import { AiProvidersOpenAIEditPage } from '@/pages/AiProvidersOpenAIEditPage';
import { AiProvidersOpenAIModelsPage } from '@/pages/AiProvidersOpenAIModelsPage';
import { AiProvidersVertexEditPage } from '@/pages/AiProvidersVertexEditPage';
import {
  AI_PROVIDER_EDIT_MODAL_CLOSE_EVENT,
  AI_PROVIDER_EDIT_MODAL_ACTION_ROOT_ID,
  AI_PROVIDER_EDIT_MODAL_NAVIGATE_EVENT,
} from '@/utils/aiProviderEditModal';
import styles from '@/pages/AiProvidersPage.module.scss';

const AI_PROVIDER_EDIT_ROUTES = [
  { path: '/ai-providers/gemini/new', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers/gemini/:index', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers/codex/new', element: <AiProvidersCodexEditPage /> },
  { path: '/ai-providers/codex/:index', element: <AiProvidersCodexEditPage /> },
  {
    path: '/ai-providers/claude/new',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  {
    path: '/ai-providers/claude/:index',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  { path: '/ai-providers/vertex/new', element: <AiProvidersVertexEditPage /> },
  { path: '/ai-providers/vertex/:index', element: <AiProvidersVertexEditPage /> },
  {
    path: '/ai-providers/openai/new',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  {
    path: '/ai-providers/openai/:index',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  { path: '/ai-providers/ampcode', element: <AiProvidersAmpcodeEditPage /> },
];

type AiProviderEditModalProps = {
  path: string | null;
  onClose: () => void;
  onNavigate: (path: string) => void;
};

export function AiProviderEditModal({ path, onClose, onNavigate }: AiProviderEditModalProps) {
  const modalContent = useRoutes(
    AI_PROVIDER_EDIT_ROUTES,
    path
      ? {
          pathname: path,
          search: '',
          hash: '',
          state: { providerEditModal: true },
          key: `ai-provider-edit:${path}`,
        }
      : undefined
  );

  useEffect(() => {
    const handleClose = () => onClose();
    const handleNavigate = (event: Event) => {
      const nextPath = (event as CustomEvent<string>).detail;
      if (nextPath) {
        onNavigate(nextPath);
      }
    };

    window.addEventListener(AI_PROVIDER_EDIT_MODAL_CLOSE_EVENT, handleClose);
    window.addEventListener(AI_PROVIDER_EDIT_MODAL_NAVIGATE_EVENT, handleNavigate);
    return () => {
      window.removeEventListener(AI_PROVIDER_EDIT_MODAL_CLOSE_EVENT, handleClose);
      window.removeEventListener(AI_PROVIDER_EDIT_MODAL_NAVIGATE_EVENT, handleNavigate);
    };
  }, [onClose, onNavigate]);

  return (
    <Modal
      open={Boolean(path)}
      onClose={onClose}
      width="min(1120px, calc(100vw - 32px))"
      className={styles.providerEditModal}
    >
      <div className={styles.providerEditModalBody}>{modalContent}</div>
      <div id={AI_PROVIDER_EDIT_MODAL_ACTION_ROOT_ID} className={styles.providerEditActionRoot} />
    </Modal>
  );
}
