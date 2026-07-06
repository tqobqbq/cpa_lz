import {
  useLayoutEffect,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconCode,
  IconDiamond,
  IconKey,
  IconSatellite,
  IconSettings,
  IconShield,
  IconTimer,
  IconTrendingUp,
  type IconProps,
} from '@/components/ui/icons';
import { ConfigSection } from '@/components/config/ConfigSection';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import type {
  PayloadFilterRule,
  PayloadParamValidationErrorCode,
  PayloadRule,
  ErrorControlPolicyField,
  ErrorControlPolicyValues,
  VisualConfigFieldPath,
  VisualConfigValidationErrorCode,
  VisualConfigValidationErrors,
  VisualConfigValues,
} from '@/types/visualConfig';
import { makeClientId } from '@/types/visualConfig';
import {
  ApiKeysCardEditor,
  PayloadFilterRulesEditor,
  PayloadRulesEditor,
} from './VisualConfigEditorBlocks';
import { evaluateOutputFilterTest } from './outputFilter';
import styles from './VisualConfigEditor.module.scss';

type VisualSectionId =
  | 'server'
  | 'tls'
  | 'remote'
  | 'auth'
  | 'system'
  | 'network'
  | 'quota'
  | 'streaming'
  | 'outputFilter'
  | 'payload';

type VisualSection = {
  id: VisualSectionId;
  title: string;
  description: string;
  icon: ComponentType<IconProps>;
  errorCount: number;
};

interface VisualConfigEditorProps {
  values: VisualConfigValues;
  validationErrors?: VisualConfigValidationErrors;
  hasPayloadValidationErrors?: boolean;
  disabled?: boolean;
  onChange: (values: Partial<VisualConfigValues>) => void;
}

function getValidationMessage(
  t: ReturnType<typeof useTranslation>['t'],
  errorCode?: VisualConfigValidationErrorCode | PayloadParamValidationErrorCode
) {
  if (!errorCode) return undefined;
  return t(`config_management.visual.validation.${errorCode}`);
}

type ToggleRowProps = {
  title: string;
  description?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
};

function ToggleRow({ title, description, checked, disabled, onChange }: ToggleRowProps) {
  return (
    <div className={styles.toggleRow}>
      <div className={styles.toggleCopy}>
        <div className={styles.toggleTitle}>{title}</div>
        {description ? <div className={styles.toggleDescription}>{description}</div> : null}
      </div>
      <ToggleSwitch checked={checked} onChange={onChange} disabled={disabled} ariaLabel={title} />
    </div>
  );
}

function SectionGrid({ children }: { children: ReactNode }) {
  return <div className={styles.sectionGrid}>{children}</div>;
}

function SectionStack({ children }: { children: ReactNode }) {
  return <div className={styles.sectionStack}>{children}</div>;
}

function Divider() {
  return <div className={styles.divider} />;
}

function createOutputFilterProviderRule() {
  return {
    id: makeClientId(),
    provider: '',
    enabled: true,
    maxLength: '200',
    keywordsText: '',
  };
}

function SectionSubsection({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.subsection}>
      <div className={styles.subsectionHeader}>
        <h3 className={styles.subsectionTitle}>{title}</h3>
        {description ? <p className={styles.subsectionDescription}>{description}</p> : null}
      </div>
      {children}
    </div>
  );
}

const ERROR_CONTROL_POLICY_FIELDS: Array<{
  field: ErrorControlPolicyField;
  labelKey: string;
  placeholder: string;
  type: 'number' | 'text';
}> = [
  {
    field: 'retryRounds',
    labelKey: 'retry_rounds',
    placeholder: '1',
    type: 'number',
  },
  {
    field: 'roundBackoffBase',
    labelKey: 'round_backoff_base',
    placeholder: '1.0',
    type: 'number',
  },
  {
    field: 'roundBackoffExponent',
    labelKey: 'round_backoff_exponent',
    placeholder: '2.0',
    type: 'number',
  },
  {
    field: 'roundBackoffMax',
    labelKey: 'round_backoff_max',
    placeholder: '60.0',
    type: 'number',
  },
];

function createEmptyErrorControlPolicy(): ErrorControlPolicyValues {
  return {
    retryRounds: '',
    roundBackoffBase: '',
    roundBackoffExponent: '',
    roundBackoffMax: '',
  };
}

function ErrorControlPolicyInputs({
  policy,
  disabled,
  validationErrors,
  pathPrefix,
  onChange,
}: {
  policy: ErrorControlPolicyValues;
  disabled?: boolean;
  validationErrors?: VisualConfigValidationErrors;
  pathPrefix: string;
  onChange: (field: ErrorControlPolicyField, value: string) => void;
}) {
  const { t } = useTranslation();

  return (
    <SectionGrid>
      {ERROR_CONTROL_POLICY_FIELDS.map((item) => {
        const fieldPath = `${pathPrefix}.${item.field}` as VisualConfigFieldPath;
        return (
          <Input
            key={item.field}
            label={t(`config_management.visual.sections.network.error_control.${item.labelKey}`)}
            type={item.type}
            placeholder={item.placeholder}
            value={policy[item.field]}
            onChange={(event) => onChange(item.field, event.target.value)}
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.[fieldPath])}
          />
        );
      })}
    </SectionGrid>
  );
}

function FieldShell({
  label,
  labelId,
  htmlFor,
  hint,
  hintId,
  error,
  errorId,
  children,
}: {
  label: string;
  labelId?: string;
  htmlFor?: string;
  hint?: string;
  hintId?: string;
  error?: string;
  errorId?: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.fieldShell}>
      <label id={labelId} htmlFor={htmlFor} className={styles.fieldLabel}>
        {label}
      </label>
      {children}
      {error ? (
        <div id={errorId} className="error-box">
          {error}
        </div>
      ) : null}
      {hint ? (
        <div id={hintId} className={styles.fieldHint}>
          {hint}
        </div>
      ) : null}
    </div>
  );
}

export function VisualConfigEditor({
  values,
  validationErrors,
  hasPayloadValidationErrors = false,
  disabled = false,
  onChange,
}: VisualConfigEditorProps) {
  const { t } = useTranslation();
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.isCurrentLayer : true;
  const isMobile = useMediaQuery('(max-width: 768px)');
  const isFloatingSidebar = useMediaQuery('(min-width: 1025px)');
  const shouldRenderFloatingSidebar = !isMobile && isFloatingSidebar && isCurrentLayer;
  const routingStrategyLabelId = useId();
  const routingStrategyHintId = `${routingStrategyLabelId}-hint`;
  const keepaliveInputId = useId();
  const keepaliveHintId = `${keepaliveInputId}-hint`;
  const keepaliveErrorId = `${keepaliveInputId}-error`;
  const nonstreamKeepaliveInputId = useId();
  const nonstreamKeepaliveHintId = `${nonstreamKeepaliveInputId}-hint`;
  const nonstreamKeepaliveErrorId = `${nonstreamKeepaliveInputId}-error`;
  const outputFilterKeywordsId = useId();
  const outputFilterSampleId = useId();
  const [outputFilterSampleText, setOutputFilterSampleText] = useState('');
  const [activeSectionId, setActiveSectionId] = useState<VisualSectionId>('server');
  const workspaceRef = useRef<HTMLDivElement | null>(null);
  const sidebarAnchorRef = useRef<HTMLElement | null>(null);
  const floatingSidebarRef = useRef<HTMLDivElement | null>(null);
  const sectionRefs = useRef<Partial<Record<VisualSectionId, HTMLElement | null>>>({});
  const mobileNavScrollerRef = useRef<HTMLDivElement | null>(null);
  const mobileNavButtonRefs = useRef<Partial<Record<VisualSectionId, HTMLButtonElement | null>>>(
    {}
  );

  const isKeepaliveDisabled =
    values.streaming.keepaliveSeconds === '' || values.streaming.keepaliveSeconds === '0';
  const isNonstreamKeepaliveDisabled =
    values.streaming.nonstreamKeepaliveInterval === '' ||
    values.streaming.nonstreamKeepaliveInterval === '0';

  const portError = getValidationMessage(t, validationErrors?.port);
  const logsMaxSizeError = getValidationMessage(t, validationErrors?.logsMaxTotalSizeMb);
  const requestRetryError = getValidationMessage(t, validationErrors?.requestRetry);
  const maxRetryIntervalError = getValidationMessage(t, validationErrors?.maxRetryInterval);
  const keepaliveError = getValidationMessage(t, validationErrors?.['streaming.keepaliveSeconds']);
  const bootstrapRetriesError = getValidationMessage(
    t,
    validationErrors?.['streaming.bootstrapRetries']
  );
  const nonstreamKeepaliveError = getValidationMessage(
    t,
    validationErrors?.['streaming.nonstreamKeepaliveInterval']
  );
  const outputFilterMaxLengthError = getValidationMessage(
    t,
    validationErrors?.['outputFilter.maxLength']
  );

  const outputFilterTestResult = useMemo(
    () =>
      evaluateOutputFilterTest({
        enabled: values.outputFilter.enabled,
        maxLength: values.outputFilter.maxLength,
        keywordsText: values.outputFilter.keywordsText,
        sampleText: outputFilterSampleText,
      }),
    [outputFilterSampleText, values.outputFilter]
  );

  const outputFilterResultText = useMemo(() => {
    switch (outputFilterTestResult.status) {
      case 'disabled':
        return t('config_management.visual.sections.output_filter.test_disabled');
      case 'empty-sample':
        return t('config_management.visual.sections.output_filter.test_empty');
      case 'invalid-max-length':
        return t('config_management.visual.sections.output_filter.test_invalid_max_length');
      case 'too-long':
        return t('config_management.visual.sections.output_filter.test_too_long', {
          actual: outputFilterTestResult.actualLength,
          max: outputFilterTestResult.maxLength,
        });
      case 'matched':
        return t('config_management.visual.sections.output_filter.test_matched', {
          pattern: outputFilterTestResult.pattern,
        });
      case 'invalid-patterns':
        return t('config_management.visual.sections.output_filter.test_invalid_patterns', {
          patterns: outputFilterTestResult.invalidPatterns.join(', '),
        });
      case 'no-match':
      default:
        return t('config_management.visual.sections.output_filter.test_no_match');
    }
  }, [outputFilterTestResult, t]);

  const handleApiKeysTextChange = useCallback(
    (apiKeysText: string) => onChange({ apiKeysText }),
    [onChange]
  );
  const handlePayloadDefaultRulesChange = useCallback(
    (payloadDefaultRules: PayloadRule[]) => onChange({ payloadDefaultRules }),
    [onChange]
  );
  const handlePayloadDefaultRawRulesChange = useCallback(
    (payloadDefaultRawRules: PayloadRule[]) => onChange({ payloadDefaultRawRules }),
    [onChange]
  );
  const handlePayloadOverrideRulesChange = useCallback(
    (payloadOverrideRules: PayloadRule[]) => onChange({ payloadOverrideRules }),
    [onChange]
  );
  const handlePayloadOverrideRawRulesChange = useCallback(
    (payloadOverrideRawRules: PayloadRule[]) => onChange({ payloadOverrideRawRules }),
    [onChange]
  );
  const handlePayloadFilterRulesChange = useCallback(
    (payloadFilterRules: PayloadFilterRule[]) => onChange({ payloadFilterRules }),
    [onChange]
  );
  const handleOutputFilterChange = useCallback(
    (outputFilter: Partial<typeof values.outputFilter>) =>
      onChange({
        outputFilter: {
          ...values.outputFilter,
          ...outputFilter,
        },
      }),
    [onChange, values.outputFilter]
  );
  const handleOutputFilterProviderChange = useCallback(
    (providerId: string, patch: Partial<(typeof values.outputFilter.providers)[number]>) =>
      onChange({
        outputFilter: {
          ...values.outputFilter,
          providers: values.outputFilter.providers.map((entry) =>
            entry.id === providerId ? { ...entry, ...patch } : entry
          ),
        },
      }),
    [onChange, values.outputFilter]
  );
  const handleAddOutputFilterProvider = useCallback(
    () =>
      onChange({
        outputFilter: {
          ...values.outputFilter,
          providers: [...values.outputFilter.providers, createOutputFilterProviderRule()],
        },
      }),
    [onChange, values.outputFilter]
  );
  const handleRemoveOutputFilterProvider = useCallback(
    (providerId: string) =>
      onChange({
        outputFilter: {
          ...values.outputFilter,
          providers: values.outputFilter.providers.filter((entry) => entry.id !== providerId),
        },
      }),
    [onChange, values.outputFilter]
  );
  const handleDefaultErrorControlChange = useCallback(
    (field: ErrorControlPolicyField, value: string) =>
      onChange({
        errorControl: {
          ...values.errorControl,
          default: {
            ...values.errorControl.default,
            [field]: value,
          },
        },
      }),
    [onChange, values.errorControl]
  );
  const handleProviderErrorControlChange = useCallback(
    (providerId: string, field: ErrorControlPolicyField, value: string) =>
      onChange({
        errorControl: {
          ...values.errorControl,
          providers: values.errorControl.providers.map((entry) =>
            entry.id === providerId
              ? {
                  ...entry,
                  policy: {
                    ...entry.policy,
                    [field]: value,
                  },
                }
              : entry
          ),
        },
      }),
    [onChange, values.errorControl]
  );
  const handleProviderNameChange = useCallback(
    (providerId: string, provider: string) =>
      onChange({
        errorControl: {
          ...values.errorControl,
          providers: values.errorControl.providers.map((entry) =>
            entry.id === providerId ? { ...entry, provider } : entry
          ),
        },
      }),
    [onChange, values.errorControl]
  );
  const handleAddErrorControlProvider = useCallback(
    () =>
      onChange({
        errorControl: {
          ...values.errorControl,
          providers: [
            ...values.errorControl.providers,
            {
              id: makeClientId(),
              provider: '',
              policy: createEmptyErrorControlPolicy(),
            },
          ],
        },
      }),
    [onChange, values.errorControl]
  );
  const handleRemoveErrorControlProvider = useCallback(
    (providerId: string) =>
      onChange({
        errorControl: {
          ...values.errorControl,
          providers: values.errorControl.providers.filter((entry) => entry.id !== providerId),
        },
      }),
    [onChange, values.errorControl]
  );

  const countErrors = useCallback(
    (fields: VisualConfigFieldPath[]) =>
      fields.reduce((total, field) => total + (validationErrors?.[field] ? 1 : 0), 0),
    [validationErrors]
  );
  const errorControlErrorCount = useMemo(
    () =>
      Object.entries(validationErrors ?? {}).filter(([field, error]) =>
        Boolean(error && field.startsWith('errorControl.'))
      ).length,
    [validationErrors]
  );
  const outputFilterErrorCount = useMemo(
    () =>
      Object.entries(validationErrors ?? {}).filter(([field, error]) =>
        Boolean(error && field.startsWith('outputFilter.'))
      ).length,
    [validationErrors]
  );

  const sections = useMemo<VisualSection[]>(
    () => [
      {
        id: 'server',
        title: t('config_management.visual.sections.server.title'),
        description: t('config_management.visual.sections.server.description'),
        icon: IconSettings,
        errorCount: countErrors(['port']),
      },
      {
        id: 'tls',
        title: t('config_management.visual.sections.tls.title'),
        description: t('config_management.visual.sections.tls.description'),
        icon: IconShield,
        errorCount: 0,
      },
      {
        id: 'remote',
        title: t('config_management.visual.sections.remote.title'),
        description: t('config_management.visual.sections.remote.description'),
        icon: IconSatellite,
        errorCount: 0,
      },
      {
        id: 'auth',
        title: t('config_management.visual.sections.auth.title'),
        description: t('config_management.visual.sections.auth.description'),
        icon: IconKey,
        errorCount: 0,
      },
      {
        id: 'system',
        title: t('config_management.visual.sections.system.title'),
        description: t('config_management.visual.sections.system.description'),
        icon: IconDiamond,
        errorCount: countErrors(['logsMaxTotalSizeMb']),
      },
      {
        id: 'network',
        title: t('config_management.visual.sections.network.title'),
        description: t('config_management.visual.sections.network.description'),
        icon: IconTrendingUp,
        errorCount: countErrors(['requestRetry', 'maxRetryInterval']) + errorControlErrorCount,
      },
      {
        id: 'quota',
        title: t('config_management.visual.sections.quota.title'),
        description: t('config_management.visual.sections.quota.description'),
        icon: IconTimer,
        errorCount: 0,
      },
      {
        id: 'streaming',
        title: t('config_management.visual.sections.streaming.title'),
        description: t('config_management.visual.sections.streaming.description'),
        icon: IconSatellite,
        errorCount: countErrors([
          'streaming.keepaliveSeconds',
          'streaming.bootstrapRetries',
          'streaming.nonstreamKeepaliveInterval',
        ]),
      },
      {
        id: 'outputFilter',
        title: t('config_management.visual.sections.output_filter.title'),
        description: t('config_management.visual.sections.output_filter.description'),
        icon: IconShield,
        errorCount: outputFilterErrorCount,
      },
      {
        id: 'payload',
        title: t('config_management.visual.sections.payload.title'),
        description: t('config_management.visual.sections.payload.description'),
        icon: IconCode,
        errorCount: hasPayloadValidationErrors ? 1 : 0,
      },
    ],
    [countErrors, errorControlErrorCount, hasPayloadValidationErrors, outputFilterErrorCount, t]
  );

  const hasValidationIssues =
    sections.some((section) => section.errorCount > 0) || hasPayloadValidationErrors;
  const focusSections = useMemo(
    () =>
      sections.filter((section) =>
        ['server', 'network', 'outputFilter', 'payload'].includes(section.id)
      ),
    [sections]
  );

  useEffect(() => {
    if (!isCurrentLayer) return undefined;
    if (typeof IntersectionObserver === 'undefined') return undefined;

    const observer = new IntersectionObserver(
      (entries) => {
        const visibleEntries = entries
          .filter((entry) => entry.isIntersecting)
          .sort((left, right) => right.intersectionRatio - left.intersectionRatio);

        if (visibleEntries.length === 0) return;
        setActiveSectionId(visibleEntries[0].target.id as VisualSectionId);
      },
      {
        rootMargin: '-18% 0px -58% 0px',
        threshold: [0.12, 0.3, 0.55],
      }
    );

    for (const section of sections) {
      const element = sectionRefs.current[section.id];
      if (element) observer.observe(element);
    }

    return () => observer.disconnect();
  }, [isCurrentLayer, sections]);

  useEffect(() => {
    if (!isCurrentLayer || !isMobile) return;
    const scroller = mobileNavScrollerRef.current;
    const button = mobileNavButtonRefs.current[activeSectionId];
    if (!scroller || !button) return;

    const scrollerRect = scroller.getBoundingClientRect();
    const buttonRect = button.getBoundingClientRect();
    const centeredLeft =
      scroller.scrollLeft +
      (buttonRect.left - scrollerRect.left) -
      (scroller.clientWidth - buttonRect.width) / 2;
    const maxScrollLeft = Math.max(scroller.scrollWidth - scroller.clientWidth, 0);
    const targetLeft = Math.min(Math.max(centeredLeft, 0), maxScrollLeft);

    scroller.scrollTo({
      left: targetLeft,
      behavior: 'smooth',
    });
  }, [activeSectionId, isCurrentLayer, isMobile]);

  const handleSectionJump = useCallback((sectionId: VisualSectionId) => {
    setActiveSectionId(sectionId);
    sectionRefs.current[sectionId]?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, []);

  useLayoutEffect(() => {
    const floatingElement = floatingSidebarRef.current;
    const anchorElement = sidebarAnchorRef.current;
    const workspaceElement = workspaceRef.current;
    if (!floatingElement) return undefined;

    const clearFloatingStyles = () => {
      floatingElement.style.removeProperty('transform');
      floatingElement.style.removeProperty('width');
      floatingElement.style.removeProperty('max-height');
      floatingElement.style.removeProperty('opacity');
      floatingElement.style.removeProperty('pointer-events');
    };

    if (!shouldRenderFloatingSidebar || !anchorElement || !workspaceElement) {
      clearFloatingStyles();
      return undefined;
    }

    /* ---- Cache header height – recomputed only on resize ---- */
    const computeHeaderHeight = () => {
      const header = document.querySelector('.main-header') as HTMLElement | null;
      if (header) return header.getBoundingClientRect().height;

      const raw = getComputedStyle(document.documentElement).getPropertyValue('--header-height');
      const parsed = Number.parseFloat(raw);
      return Number.isFinite(parsed) ? parsed : 64;
    };
    let headerHeight = computeHeaderHeight();

    /* ---- Cache content scroller – resolved once ---- */
    const contentScroller = document.querySelector('.content') as HTMLElement | null;

    /* ---- Cache floating height from previous frame ---- */
    let cachedFloatingHeight = floatingElement.getBoundingClientRect().height || 200;

    let frameId = 0;

    const updateFloatingPosition = () => {
      frameId = 0;

      const anchorRect = anchorElement.getBoundingClientRect();
      const workspaceRect = workspaceElement.getBoundingClientRect();
      const stickyTop = headerHeight + 20;
      const viewportPadding = 16;
      const maxTop = workspaceRect.bottom - cachedFloatingHeight;
      const unclampedTop = Math.min(Math.max(anchorRect.top, stickyTop), maxTop);
      const top = Math.max(unclampedTop, viewportPadding);
      const left = Math.max(anchorRect.left, viewportPadding);
      const width = Math.max(
        Math.min(anchorRect.width, window.innerWidth - left - viewportPadding),
        220
      );
      const maxHeight = Math.max(window.innerHeight - top - viewportPadding, 160);
      const isVisible =
        workspaceRect.bottom > stickyTop + 24 && anchorRect.top < window.innerHeight;

      floatingElement.style.transform = `translate3d(${left}px, ${top}px, 0)`;
      floatingElement.style.width = `${width}px`;
      floatingElement.style.maxHeight = `${maxHeight}px`;
      floatingElement.style.opacity = isVisible ? '1' : '0';
      floatingElement.style.pointerEvents = isVisible ? 'auto' : 'none';
    };

    const requestPositionUpdate = () => {
      if (frameId) cancelAnimationFrame(frameId);
      frameId = requestAnimationFrame(updateFloatingPosition);
    };

    const handleResize = () => {
      headerHeight = computeHeaderHeight();
      cachedFloatingHeight = floatingElement.getBoundingClientRect().height || cachedFloatingHeight;
      requestPositionUpdate();
    };

    requestPositionUpdate();

    window.addEventListener('resize', handleResize);
    window.addEventListener('scroll', requestPositionUpdate, { passive: true });
    contentScroller?.addEventListener('scroll', requestPositionUpdate, { passive: true });

    const resizeObserver =
      typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(requestPositionUpdate);
    resizeObserver?.observe(anchorElement);
    resizeObserver?.observe(workspaceElement);

    return () => {
      if (frameId) cancelAnimationFrame(frameId);
      resizeObserver?.disconnect();
      window.removeEventListener('resize', handleResize);
      window.removeEventListener('scroll', requestPositionUpdate);
      contentScroller?.removeEventListener('scroll', requestPositionUpdate);
      clearFloatingStyles();
    };
  }, [shouldRenderFloatingSidebar]);

  const navContent = (
    <div className={styles.navList}>
      {sections.map((section, index) => {
        const Icon = section.icon;

        return (
          <button
            key={section.id}
            type="button"
            className={`${styles.navButton} ${
              activeSectionId === section.id ? styles.navButtonActive : ''
            }`}
            onClick={() => handleSectionJump(section.id)}
          >
            <span className={styles.navIndex}>{String(index + 1).padStart(2, '0')}</span>
            <span className={styles.navMain}>
              <span className={styles.navHeadingRow}>
                <span className={styles.navLabelWrap}>
                  <span className={styles.navIcon}>
                    <Icon size={14} />
                  </span>
                  <span className={styles.navLabel}>{section.title}</span>
                </span>
                {section.errorCount > 0 ? (
                  <span className={styles.navBadge} aria-hidden="true">
                    {section.errorCount}
                  </span>
                ) : null}
              </span>
              <span className={styles.navDescription}>{section.description}</span>
            </span>
          </button>
        );
      })}
    </div>
  );

  return (
    <div className={styles.visualEditor}>
      <div className={styles.overview}>
        <div className={styles.overviewHeader}>
          <div className={styles.overviewMeta}>
            <span className={styles.overviewPill}>
              {t('config_management.visual.quick_jump', { defaultValue: '快速跳转' })}
            </span>
            {hasValidationIssues ? (
              <span className={`${styles.overviewPill} ${styles.overviewPillWarning}`}>
                {t('config_management.visual.validation.validation_blocked')}
              </span>
            ) : null}
          </div>
        </div>

        <div className={styles.overviewFocusList}>
          {focusSections.map((section) => {
            const Icon = section.icon;

            return (
              <button
                key={section.id}
                type="button"
                className={`${styles.overviewFocusLink} ${
                  activeSectionId === section.id ? styles.overviewFocusLinkActive : ''
                }`}
                onClick={() => handleSectionJump(section.id)}
              >
                <span className={styles.focusIcon}>
                  <Icon size={16} />
                </span>
                <span className={styles.focusCopy}>
                  <span className={styles.focusTitle}>{section.title}</span>
                  <span className={styles.focusDescription}>{section.description}</span>
                </span>
                {section.errorCount > 0 ? (
                  <span className={styles.navBadge} aria-hidden="true">
                    {section.errorCount}
                  </span>
                ) : null}
              </button>
            );
          })}
        </div>
      </div>

      <div ref={workspaceRef} className={styles.workspace}>
        {isMobile ? (
          <div className={styles.mobileSectionNav}>
            <div
              ref={mobileNavScrollerRef}
              className={styles.mobileSectionNavScroller}
              aria-label={t('config_management.visual.quick_jump', { defaultValue: '快速跳转' })}
            >
              {sections.map((section, index) => (
                <button
                  key={section.id}
                  ref={(node) => {
                    mobileNavButtonRefs.current[section.id] = node;
                  }}
                  type="button"
                  className={`${styles.mobileSectionNavButton} ${
                    activeSectionId === section.id ? styles.mobileSectionNavButtonActive : ''
                  }`}
                  onClick={() => handleSectionJump(section.id)}
                >
                  <span className={styles.mobileSectionNavIndex}>
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  <span className={styles.mobileSectionNavLabel}>{section.title}</span>
                  {section.errorCount > 0 ? (
                    <span className={styles.mobileSectionNavBadge} aria-hidden="true">
                      {section.errorCount}
                    </span>
                  ) : null}
                </button>
              ))}
            </div>
          </div>
        ) : null}

        <aside ref={sidebarAnchorRef} className={styles.sidebar}>
          {isFloatingSidebar ? (
            <div className={styles.sidebarPlaceholder} aria-hidden="true" />
          ) : (
            <div className={styles.sidebarRail}>{navContent}</div>
          )}
        </aside>

        <div className={styles.sections}>
          <ConfigSection
            id="server"
            ref={(node) => {
              sectionRefs.current.server = node;
            }}
            indexLabel="01"
            icon={<IconSettings size={16} />}
            title={t('config_management.visual.sections.server.title')}
            description={t('config_management.visual.sections.server.description')}
          >
            <SectionGrid>
              <Input
                label={t('config_management.visual.sections.server.host')}
                placeholder="0.0.0.0"
                value={values.host}
                onChange={(e) => onChange({ host: e.target.value })}
                disabled={disabled}
              />
              <Input
                label={t('config_management.visual.sections.server.port')}
                type="number"
                placeholder="8317"
                value={values.port}
                onChange={(e) => onChange({ port: e.target.value })}
                disabled={disabled}
                error={portError}
              />
            </SectionGrid>
          </ConfigSection>

          <ConfigSection
            id="tls"
            ref={(node) => {
              sectionRefs.current.tls = node;
            }}
            indexLabel="02"
            icon={<IconShield size={16} />}
            title={t('config_management.visual.sections.tls.title')}
            description={t('config_management.visual.sections.tls.description')}
          >
            <SectionStack>
              <ToggleRow
                title={t('config_management.visual.sections.tls.enable')}
                description={t('config_management.visual.sections.tls.enable_desc')}
                checked={values.tlsEnable}
                disabled={disabled}
                onChange={(tlsEnable) => onChange({ tlsEnable })}
              />

              {values.tlsEnable ? (
                <>
                  <Divider />
                  <SectionGrid>
                    <Input
                      label={t('config_management.visual.sections.tls.cert')}
                      placeholder="/path/to/cert.pem"
                      value={values.tlsCert}
                      onChange={(e) => onChange({ tlsCert: e.target.value })}
                      disabled={disabled}
                    />
                    <Input
                      label={t('config_management.visual.sections.tls.key')}
                      placeholder="/path/to/key.pem"
                      value={values.tlsKey}
                      onChange={(e) => onChange({ tlsKey: e.target.value })}
                      disabled={disabled}
                    />
                  </SectionGrid>
                </>
              ) : null}
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="remote"
            ref={(node) => {
              sectionRefs.current.remote = node;
            }}
            indexLabel="03"
            icon={<IconSatellite size={16} />}
            title={t('config_management.visual.sections.remote.title')}
            description={t('config_management.visual.sections.remote.description')}
          >
            <SectionStack>
              <ToggleRow
                title={t('config_management.visual.sections.remote.allow_remote')}
                description={t('config_management.visual.sections.remote.allow_remote_desc')}
                checked={values.rmAllowRemote}
                disabled={disabled}
                onChange={(rmAllowRemote) => onChange({ rmAllowRemote })}
              />
              <ToggleRow
                title={t('config_management.visual.sections.remote.disable_panel')}
                description={t('config_management.visual.sections.remote.disable_panel_desc')}
                checked={values.rmDisableControlPanel}
                disabled={disabled}
                onChange={(rmDisableControlPanel) => onChange({ rmDisableControlPanel })}
              />
              <SectionGrid>
                <Input
                  label={t('config_management.visual.sections.remote.secret_key')}
                  type="password"
                  placeholder={t('config_management.visual.sections.remote.secret_key_placeholder')}
                  value={values.rmSecretKey}
                  onChange={(e) => onChange({ rmSecretKey: e.target.value })}
                  disabled={disabled}
                />
                <Input
                  label={t('config_management.visual.sections.remote.panel_repo')}
                  placeholder="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
                  value={values.rmPanelRepo}
                  onChange={(e) => onChange({ rmPanelRepo: e.target.value })}
                  disabled={disabled}
                />
              </SectionGrid>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="auth"
            ref={(node) => {
              sectionRefs.current.auth = node;
            }}
            indexLabel="04"
            icon={<IconKey size={16} />}
            title={t('config_management.visual.sections.auth.title')}
            description={t('config_management.visual.sections.auth.description')}
          >
            <SectionStack>
              <Input
                label={t('config_management.visual.sections.auth.auth_dir')}
                placeholder="~/.cli-proxy-api"
                value={values.authDir}
                onChange={(e) => onChange({ authDir: e.target.value })}
                disabled={disabled}
                hint={t('config_management.visual.sections.auth.auth_dir_hint')}
              />
              <div className={styles.subsection}>
                <ApiKeysCardEditor
                  value={values.apiKeysText}
                  disabled={disabled}
                  onChange={handleApiKeysTextChange}
                />
              </div>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="system"
            ref={(node) => {
              sectionRefs.current.system = node;
            }}
            indexLabel="05"
            icon={<IconDiamond size={16} />}
            title={t('config_management.visual.sections.system.title')}
            description={t('config_management.visual.sections.system.description')}
          >
            <SectionStack>
              <SectionGrid>
                <ToggleRow
                  title={t('config_management.visual.sections.system.debug')}
                  description={t('config_management.visual.sections.system.debug_desc')}
                  checked={values.debug}
                  disabled={disabled}
                  onChange={(debug) => onChange({ debug })}
                />
                <ToggleRow
                  title={t('config_management.visual.sections.system.commercial_mode')}
                  description={t('config_management.visual.sections.system.commercial_mode_desc')}
                  checked={values.commercialMode}
                  disabled={disabled}
                  onChange={(commercialMode) => onChange({ commercialMode })}
                />
                <ToggleRow
                  title={t('config_management.visual.sections.system.logging_to_file')}
                  description={t('config_management.visual.sections.system.logging_to_file_desc')}
                  checked={values.loggingToFile}
                  disabled={disabled}
                  onChange={(loggingToFile) => onChange({ loggingToFile })}
                />
                <ToggleRow
                  title={t('config_management.visual.sections.system.usage_statistics')}
                  description={t('config_management.visual.sections.system.usage_statistics_desc')}
                  checked={values.usageStatisticsEnabled}
                  disabled={disabled}
                  onChange={(usageStatisticsEnabled) => onChange({ usageStatisticsEnabled })}
                />
              </SectionGrid>

              <SectionGrid>
                <Input
                  label={t('config_management.visual.sections.system.logs_max_size')}
                  type="number"
                  placeholder="0"
                  value={values.logsMaxTotalSizeMb}
                  onChange={(e) => onChange({ logsMaxTotalSizeMb: e.target.value })}
                  disabled={disabled}
                  error={logsMaxSizeError}
                />
              </SectionGrid>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="network"
            ref={(node) => {
              sectionRefs.current.network = node;
            }}
            indexLabel="06"
            icon={<IconTrendingUp size={16} />}
            title={t('config_management.visual.sections.network.title')}
            description={t('config_management.visual.sections.network.description')}
          >
            <SectionStack>
              <SectionGrid>
                <Input
                  label={t('config_management.visual.sections.network.proxy_url')}
                  placeholder="socks5://user:pass@127.0.0.1:1080/"
                  value={values.proxyUrl}
                  onChange={(e) => onChange({ proxyUrl: e.target.value })}
                  disabled={disabled}
                />
                <Input
                  label={t('config_management.visual.sections.network.request_retry')}
                  type="number"
                  placeholder="3"
                  value={values.requestRetry}
                  onChange={(e) => onChange({ requestRetry: e.target.value })}
                  disabled={disabled}
                  error={requestRetryError}
                />
                <Input
                  label={t('config_management.visual.sections.network.max_retry_interval')}
                  type="number"
                  placeholder="30"
                  value={values.maxRetryInterval}
                  onChange={(e) => onChange({ maxRetryInterval: e.target.value })}
                  disabled={disabled}
                  error={maxRetryIntervalError}
                />
                <Input
                  label={t('config_management.visual.sections.network.default_test_model_codex')}
                  placeholder="gpt-5-codex"
                  value={values.defaultTestModelCodex}
                  onChange={(e) => onChange({ defaultTestModelCodex: e.target.value })}
                  disabled={disabled}
                />
                <Input
                  label={t('config_management.visual.sections.network.default_test_model_claude')}
                  placeholder="claude-sonnet-4"
                  value={values.defaultTestModelClaude}
                  onChange={(e) => onChange({ defaultTestModelClaude: e.target.value })}
                  disabled={disabled}
                />
                <FieldShell
                  label={t('config_management.visual.sections.network.routing_strategy')}
                  labelId={routingStrategyLabelId}
                  hint={t('config_management.visual.sections.network.routing_strategy_hint')}
                  hintId={routingStrategyHintId}
                >
                  <Select
                    value={values.routingStrategy}
                    options={[
                      {
                        value: 'random',
                        label: t('config_management.visual.sections.network.strategy_random'),
                      },
                      {
                        value: 'last-success',
                        label: t(
                          'config_management.visual.sections.network.strategy_last_success'
                        ),
                      },
                    ]}
                    id={`${routingStrategyLabelId}-select`}
                    disabled={disabled}
                    ariaLabelledBy={routingStrategyLabelId}
                    ariaDescribedBy={routingStrategyHintId}
                    onChange={(nextValue) =>
                      onChange({
                        routingStrategy: nextValue as VisualConfigValues['routingStrategy'],
                      })
                    }
                  />
                </FieldShell>
                <Input
                  label={t(
                    'config_management.visual.sections.network.parallel_requests_min_round'
                  )}
                  type="number"
                  placeholder="1"
                  value={values.routingParallelRequestsMinRound}
                  onChange={(e) =>
                    onChange({ routingParallelRequestsMinRound: e.target.value })
                  }
                  disabled={disabled}
                  error={getValidationMessage(
                    t,
                    validationErrors?.routingParallelRequestsMinRound
                  )}
                />
                <Input
                  label={t(
                    'config_management.visual.sections.network.parallel_requests_min_failures'
                  )}
                  type="number"
                  placeholder="1"
                  value={values.routingParallelRequestsMinFailures}
                  onChange={(e) =>
                    onChange({ routingParallelRequestsMinFailures: e.target.value })
                  }
                  disabled={disabled}
                  error={getValidationMessage(
                    t,
                    validationErrors?.routingParallelRequestsMinFailures
                  )}
                />
                <Input
                  label={t('config_management.visual.sections.network.session_affinity_ttl')}
                  placeholder="1h"
                  value={values.routingSessionAffinityTTL}
                  onChange={(e) => onChange({ routingSessionAffinityTTL: e.target.value })}
                  disabled={disabled}
                />
              </SectionGrid>

              <SectionGrid>
                <ToggleRow
                  title={t(
                    'config_management.visual.sections.network.parallel_requests_enabled'
                  )}
                  description={t(
                    'config_management.visual.sections.network.parallel_requests_enabled_desc'
                  )}
                  checked={values.routingParallelRequestsEnabled}
                  disabled={disabled}
                  onChange={(routingParallelRequestsEnabled) =>
                    onChange({ routingParallelRequestsEnabled })
                  }
                />
                <ToggleRow
                  title={t('config_management.visual.sections.network.force_model_prefix')}
                  description={t(
                    'config_management.visual.sections.network.force_model_prefix_desc'
                  )}
                  checked={values.forceModelPrefix}
                  disabled={disabled}
                  onChange={(forceModelPrefix) => onChange({ forceModelPrefix })}
                />
                <ToggleRow
                  title={t(
                    'config_management.visual.sections.network.codex_remove_empty_input_name'
                  )}
                  description={t(
                    'config_management.visual.sections.network.codex_remove_empty_input_name_desc'
                  )}
                  checked={values.codexRemoveEmptyInputName}
                  disabled={disabled}
                  onChange={(codexRemoveEmptyInputName) => onChange({ codexRemoveEmptyInputName })}
                />
                <ToggleRow
                  title={t('config_management.visual.sections.network.session_affinity')}
                  checked={values.routingSessionAffinity}
                  disabled={disabled}
                  onChange={(routingSessionAffinity) => onChange({ routingSessionAffinity })}
                />
                <ToggleRow
                  title={t('config_management.visual.sections.network.ws_auth')}
                  description={t('config_management.visual.sections.network.ws_auth_desc')}
                  checked={values.wsAuth}
                  disabled={disabled}
                  onChange={(wsAuth) => onChange({ wsAuth })}
                />
              </SectionGrid>

              <SectionSubsection
                title={t('config_management.visual.sections.network.error_control.title')}
                description={t(
                  'config_management.visual.sections.network.error_control.description'
                )}
              >
                <SectionSubsection
                  title={t('config_management.visual.sections.network.error_control.default_title')}
                  description={t(
                    'config_management.visual.sections.network.error_control.default_description'
                  )}
                >
                  <ErrorControlPolicyInputs
                    policy={values.errorControl.default}
                    disabled={disabled}
                    validationErrors={validationErrors}
                    pathPrefix="errorControl.default"
                    onChange={handleDefaultErrorControlChange}
                  />
                </SectionSubsection>

                <SectionSubsection
                  title={t(
                    'config_management.visual.sections.network.error_control.providers_title'
                  )}
                  description={t(
                    'config_management.visual.sections.network.error_control.providers_description'
                  )}
                >
                  {values.errorControl.providers.length === 0 ? (
                    <div className={styles.emptyState}>
                      {t('config_management.visual.sections.network.error_control.providers_empty')}
                    </div>
                  ) : (
                    <div className={styles.blockStack}>
                      {values.errorControl.providers.map((entry, index) => (
                        <div key={entry.id} className={styles.ruleCard}>
                          <div className={styles.ruleCardHeader}>
                            <div className={styles.ruleCardTitle}>
                              {entry.provider.trim() ||
                                t(
                                  'config_management.visual.sections.network.error_control.provider_fallback',
                                  { index: index + 1 }
                                )}
                            </div>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => handleRemoveErrorControlProvider(entry.id)}
                              disabled={disabled}
                            >
                              {t('config_management.visual.common.delete')}
                            </Button>
                          </div>
                          <SectionGrid>
                            <Input
                              label={t(
                                'config_management.visual.sections.network.error_control.provider_name'
                              )}
                              placeholder="claude"
                              value={entry.provider}
                              onChange={(event) =>
                                handleProviderNameChange(entry.id, event.target.value)
                              }
                              disabled={disabled}
                            />
                          </SectionGrid>
                          <ErrorControlPolicyInputs
                            policy={entry.policy}
                            disabled={disabled}
                            validationErrors={validationErrors}
                            pathPrefix={`errorControl.providers.${entry.id}`}
                            onChange={(field, value) =>
                              handleProviderErrorControlChange(entry.id, field, value)
                            }
                          />
                        </div>
                      ))}
                    </div>
                  )}
                  <div className={styles.actionRow}>
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      onClick={handleAddErrorControlProvider}
                      disabled={disabled}
                    >
                      {t('config_management.visual.sections.network.error_control.add_provider')}
                    </Button>
                  </div>
                </SectionSubsection>
              </SectionSubsection>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="quota"
            ref={(node) => {
              sectionRefs.current.quota = node;
            }}
            indexLabel="07"
            icon={<IconTimer size={16} />}
            title={t('config_management.visual.sections.quota.title')}
            description={t('config_management.visual.sections.quota.description')}
          >
            <SectionGrid>
              <ToggleRow
                title={t('config_management.visual.sections.quota.switch_project')}
                description={t('config_management.visual.sections.quota.switch_project_desc')}
                checked={values.quotaSwitchProject}
                disabled={disabled}
                onChange={(quotaSwitchProject) => onChange({ quotaSwitchProject })}
              />
              <ToggleRow
                title={t('config_management.visual.sections.quota.switch_preview_model')}
                description={t('config_management.visual.sections.quota.switch_preview_model_desc')}
                checked={values.quotaSwitchPreviewModel}
                disabled={disabled}
                onChange={(quotaSwitchPreviewModel) => onChange({ quotaSwitchPreviewModel })}
              />
              <ToggleRow
                title={t('config_management.visual.sections.quota.antigravity_credits')}
                description={t('config_management.visual.sections.quota.antigravity_credits_desc')}
                checked={values.quotaAntigravityCredits}
                disabled={disabled}
                onChange={(quotaAntigravityCredits) => onChange({ quotaAntigravityCredits })}
              />
            </SectionGrid>
          </ConfigSection>

          <ConfigSection
            id="streaming"
            ref={(node) => {
              sectionRefs.current.streaming = node;
            }}
            indexLabel="08"
            icon={<IconSatellite size={16} />}
            title={t('config_management.visual.sections.streaming.title')}
            description={t('config_management.visual.sections.streaming.description')}
          >
            <SectionStack>
              <SectionGrid>
                <FieldShell
                  label={t('config_management.visual.sections.streaming.keepalive_seconds')}
                  htmlFor={keepaliveInputId}
                  hint={t('config_management.visual.sections.streaming.keepalive_hint')}
                  hintId={keepaliveHintId}
                  error={keepaliveError}
                  errorId={keepaliveErrorId}
                >
                  <div className={styles.fieldControl}>
                    <input
                      id={keepaliveInputId}
                      className="input"
                      type="number"
                      placeholder="0"
                      value={values.streaming.keepaliveSeconds}
                      onChange={(e) =>
                        onChange({
                          streaming: {
                            ...values.streaming,
                            keepaliveSeconds: e.target.value,
                          },
                        })
                      }
                      disabled={disabled}
                    />
                    {isKeepaliveDisabled ? (
                      <span className={styles.inlinePill}>
                        {t('config_management.visual.sections.streaming.disabled')}
                      </span>
                    ) : null}
                  </div>
                </FieldShell>

                <Input
                  label={t('config_management.visual.sections.streaming.bootstrap_retries')}
                  type="number"
                  placeholder="1"
                  value={values.streaming.bootstrapRetries}
                  onChange={(e) =>
                    onChange({
                      streaming: {
                        ...values.streaming,
                        bootstrapRetries: e.target.value,
                      },
                    })
                  }
                  disabled={disabled}
                  hint={t('config_management.visual.sections.streaming.bootstrap_hint')}
                  error={bootstrapRetriesError}
                />
              </SectionGrid>

              <SectionGrid>
                <FieldShell
                  label={t('config_management.visual.sections.streaming.nonstream_keepalive')}
                  htmlFor={nonstreamKeepaliveInputId}
                  hint={t('config_management.visual.sections.streaming.nonstream_keepalive_hint')}
                  hintId={nonstreamKeepaliveHintId}
                  error={nonstreamKeepaliveError}
                  errorId={nonstreamKeepaliveErrorId}
                >
                  <div className={styles.fieldControl}>
                    <input
                      id={nonstreamKeepaliveInputId}
                      className="input"
                      type="number"
                      placeholder="0"
                      value={values.streaming.nonstreamKeepaliveInterval}
                      onChange={(e) =>
                        onChange({
                          streaming: {
                            ...values.streaming,
                            nonstreamKeepaliveInterval: e.target.value,
                          },
                        })
                      }
                      disabled={disabled}
                    />
                    {isNonstreamKeepaliveDisabled ? (
                      <span className={styles.inlinePill}>
                        {t('config_management.visual.sections.streaming.disabled')}
                      </span>
                    ) : null}
                  </div>
                </FieldShell>
              </SectionGrid>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="outputFilter"
            ref={(node) => {
              sectionRefs.current.outputFilter = node;
            }}
            indexLabel="09"
            icon={<IconShield size={16} />}
            title={t('config_management.visual.sections.output_filter.title')}
            description={t('config_management.visual.sections.output_filter.description')}
          >
            <SectionStack>
              <SectionSubsection
                title={t('config_management.visual.sections.output_filter.global_title')}
                description={t(
                  'config_management.visual.sections.output_filter.global_description'
                )}
              >
                <SectionGrid>
                  <ToggleRow
                    title={t('config_management.visual.sections.output_filter.enabled')}
                    description={t('config_management.visual.sections.output_filter.enabled_desc')}
                    checked={values.outputFilter.enabled}
                    disabled={disabled}
                    onChange={(enabled) => handleOutputFilterChange({ enabled })}
                  />
                  <Input
                    label={t('config_management.visual.sections.output_filter.max_length')}
                    type="number"
                    placeholder="200"
                    value={values.outputFilter.maxLength}
                    onChange={(event) =>
                      handleOutputFilterChange({ maxLength: event.target.value })
                    }
                    disabled={disabled}
                    hint={t('config_management.visual.sections.output_filter.max_length_hint')}
                    error={outputFilterMaxLengthError}
                  />
                </SectionGrid>

                <FieldShell
                  label={t('config_management.visual.sections.output_filter.keywords')}
                  htmlFor={outputFilterKeywordsId}
                  hint={t('config_management.visual.sections.output_filter.keywords_hint')}
                >
                  <textarea
                    id={outputFilterKeywordsId}
                    className="input"
                    value={values.outputFilter.keywordsText}
                    onChange={(event) =>
                      handleOutputFilterChange({ keywordsText: event.target.value })
                    }
                    disabled={disabled}
                    rows={5}
                    placeholder={'quota\\s+exceeded\npermission\\s+denied'}
                  />
                </FieldShell>
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.output_filter.providers_title')}
                description={t(
                  'config_management.visual.sections.output_filter.providers_description'
                )}
              >
                {values.outputFilter.providers.length === 0 ? (
                  <div className={styles.emptyState}>
                    {t('config_management.visual.sections.output_filter.providers_empty')}
                  </div>
                ) : (
                  <div className={styles.blockStack}>
                    {values.outputFilter.providers.map((entry, index) => {
                      const keywordsId = `output-filter-provider-keywords-${entry.id}`;
                      const maxLengthError = getValidationMessage(
                        t,
                        validationErrors?.[
                          `outputFilter.providers.${entry.id}.maxLength` as VisualConfigFieldPath
                        ]
                      );
                      return (
                        <div key={entry.id} className={styles.ruleCard}>
                          <div className={styles.ruleCardHeader}>
                            <div className={styles.ruleCardTitle}>
                              {entry.provider.trim() ||
                                t(
                                  'config_management.visual.sections.output_filter.provider_fallback',
                                  {
                                    index: index + 1,
                                  }
                                )}
                            </div>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => handleRemoveOutputFilterProvider(entry.id)}
                              disabled={disabled}
                            >
                              {t('config_management.visual.common.delete')}
                            </Button>
                          </div>
                          <SectionGrid>
                            <Input
                              label={t(
                                'config_management.visual.sections.output_filter.provider_name'
                              )}
                              placeholder="claude"
                              value={entry.provider}
                              onChange={(event) =>
                                handleOutputFilterProviderChange(entry.id, {
                                  provider: event.target.value,
                                })
                              }
                              disabled={disabled}
                            />
                            <Input
                              label={t(
                                'config_management.visual.sections.output_filter.max_length'
                              )}
                              type="number"
                              placeholder="200"
                              value={entry.maxLength}
                              onChange={(event) =>
                                handleOutputFilterProviderChange(entry.id, {
                                  maxLength: event.target.value,
                                })
                              }
                              disabled={disabled}
                              hint={t(
                                'config_management.visual.sections.output_filter.max_length_hint'
                              )}
                              error={maxLengthError}
                            />
                          </SectionGrid>
                          <ToggleRow
                            title={t('config_management.visual.sections.output_filter.enabled')}
                            description={t(
                              'config_management.visual.sections.output_filter.provider_enabled_desc'
                            )}
                            checked={entry.enabled}
                            disabled={disabled}
                            onChange={(enabled) =>
                              handleOutputFilterProviderChange(entry.id, { enabled })
                            }
                          />
                          <FieldShell
                            label={t('config_management.visual.sections.output_filter.keywords')}
                            htmlFor={keywordsId}
                            hint={t(
                              'config_management.visual.sections.output_filter.keywords_hint'
                            )}
                          >
                            <textarea
                              id={keywordsId}
                              className="input"
                              value={entry.keywordsText}
                              onChange={(event) =>
                                handleOutputFilterProviderChange(entry.id, {
                                  keywordsText: event.target.value,
                                })
                              }
                              disabled={disabled}
                              rows={4}
                              placeholder={'quota\\s+exceeded\npermission\\s+denied'}
                            />
                          </FieldShell>
                        </div>
                      );
                    })}
                  </div>
                )}
                <div className={styles.actionRow}>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    onClick={handleAddOutputFilterProvider}
                    disabled={disabled}
                  >
                    {t('config_management.visual.sections.output_filter.add_provider')}
                  </Button>
                </div>
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.output_filter.test_title')}
                description={t('config_management.visual.sections.output_filter.test_description')}
              >
                <FieldShell
                  label={t('config_management.visual.sections.output_filter.sample_text')}
                  htmlFor={outputFilterSampleId}
                >
                  <textarea
                    id={outputFilterSampleId}
                    className="input"
                    value={outputFilterSampleText}
                    onChange={(event) => setOutputFilterSampleText(event.target.value)}
                    rows={4}
                    placeholder='{"error":"Quota exceeded"}'
                  />
                </FieldShell>
                <div
                  className={
                    outputFilterTestResult.status === 'matched'
                      ? styles.outputFilterResultMatched
                      : styles.outputFilterResult
                  }
                  role="status"
                >
                  {outputFilterResultText}
                </div>
              </SectionSubsection>
            </SectionStack>
          </ConfigSection>

          <ConfigSection
            id="payload"
            ref={(node) => {
              sectionRefs.current.payload = node;
            }}
            indexLabel="10"
            icon={<IconCode size={16} />}
            title={t('config_management.visual.sections.payload.title')}
            description={t('config_management.visual.sections.payload.description')}
          >
            <SectionStack>
              <SectionSubsection
                title={t('config_management.visual.sections.payload.default_rules')}
                description={t('config_management.visual.sections.payload.default_rules_desc')}
              >
                <PayloadRulesEditor
                  value={values.payloadDefaultRules}
                  disabled={disabled}
                  onChange={handlePayloadDefaultRulesChange}
                />
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.payload.default_raw_rules')}
                description={t('config_management.visual.sections.payload.default_raw_rules_desc')}
              >
                <PayloadRulesEditor
                  value={values.payloadDefaultRawRules}
                  disabled={disabled}
                  rawJsonValues
                  onChange={handlePayloadDefaultRawRulesChange}
                />
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.payload.override_rules')}
                description={t('config_management.visual.sections.payload.override_rules_desc')}
              >
                <PayloadRulesEditor
                  value={values.payloadOverrideRules}
                  disabled={disabled}
                  protocolFirst
                  onChange={handlePayloadOverrideRulesChange}
                />
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.payload.override_raw_rules')}
                description={t('config_management.visual.sections.payload.override_raw_rules_desc')}
              >
                <PayloadRulesEditor
                  value={values.payloadOverrideRawRules}
                  disabled={disabled}
                  protocolFirst
                  rawJsonValues
                  onChange={handlePayloadOverrideRawRulesChange}
                />
              </SectionSubsection>

              <SectionSubsection
                title={t('config_management.visual.sections.payload.filter_rules')}
                description={t('config_management.visual.sections.payload.filter_rules_desc')}
              >
                <PayloadFilterRulesEditor
                  value={values.payloadFilterRules}
                  disabled={disabled}
                  onChange={handlePayloadFilterRulesChange}
                />
              </SectionSubsection>
            </SectionStack>
          </ConfigSection>
        </div>
      </div>

      {shouldRenderFloatingSidebar && typeof document !== 'undefined'
        ? createPortal(
            <div ref={floatingSidebarRef} className={styles.floatingSidebarContainer}>
              <div className={styles.floatingSidebarRail}>{navContent}</div>
            </div>,
            document.body
          )
        : null}
    </div>
  );
}
