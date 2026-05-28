import { useEffect, useRef, useState } from 'react';
import { motion, AnimatePresence } from 'framer-motion';
import { useTheme } from '../contexts/ThemeContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import {
  Sparkles, FolderPlus, Upload, Globe, MessageSquare, Telescope, Wand2, Share2, Rocket,
  ChevronLeft, ChevronRight
} from 'lucide-react';

interface OnboardingTourProps {
  show: boolean;
  onClose: () => void;
}

const steps = [
  { icon: Sparkles, titleKey: 'onboardingWelcomeTitle', descKey: 'onboardingWelcomeDesc', image: null },
  { icon: FolderPlus, titleKey: 'onboardingCreateKbTitle', descKey: 'onboardingCreateKbDesc', image: '/onboarding/create-kb.png' },
  { icon: Upload, titleKey: 'onboardingUploadTitle', descKey: 'onboardingUploadDesc', image: '/onboarding/upload-files.png' },
  { icon: Globe, titleKey: 'onboardingInternetTitle', descKey: 'onboardingInternetDesc', image: '/onboarding/internet-sources.png' },
  { icon: MessageSquare, titleKey: 'onboardingChatTitle', descKey: 'onboardingChatDesc', image: '/onboarding/chat.png' },
  { icon: Telescope, titleKey: 'onboardingResearchTitle', descKey: 'onboardingResearchDesc', image: '/onboarding/research.png' },
  { icon: Wand2, titleKey: 'onboardingStudioTitle', descKey: 'onboardingStudioDesc', image: '/onboarding/studio.png' },
  { icon: Share2, titleKey: 'onboardingShareTitle', descKey: 'onboardingShareDesc', image: '/onboarding/share-kb.png' },
  { icon: Rocket, titleKey: 'onboardingFinishTitle', descKey: 'onboardingFinishDesc', image: null },
];

export function OnboardingTour({ show, onClose }: OnboardingTourProps) {
  const { t } = useTheme();
  const reducedMotion = useReducedMotion();
  const [step, setStep] = useState(0);
  const [direction, setDirection] = useState(1);
  const modalRef = useRef<HTMLDivElement>(null);

  // Reset step when opening
  useEffect(() => {
    if (show) setStep(0);
  }, [show]);

  // Focus trap & Escape key
  useEffect(() => {
    if (!show) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
        return;
      }
      if (e.key === 'Tab' && modalRef.current) {
        const focusable = modalRef.current.querySelectorAll(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        const first = focusable[0] as HTMLElement;
        const last = focusable[focusable.length - 1] as HTMLElement;
        if (e.shiftKey) {
          if (document.activeElement === first) { last.focus(); e.preventDefault(); }
        } else {
          if (document.activeElement === last) { first.focus(); e.preventDefault(); }
        }
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [show, onClose]);

  const goTo = (newStep: number) => {
    setDirection(newStep > step ? 1 : -1);
    setStep(newStep);
  };

  const next = () => {
    if (step < steps.length - 1) goTo(step + 1);
  };

  const prev = () => {
    if (step > 0) goTo(step - 1);
  };

  if (!show) return null;

  const current = steps[step];
  const Icon = current.icon;
  const hasImage = !!current.image;
  const isFirst = step === 0;
  const isLast = step === steps.length - 1;

  const slideVariants = {
    enter: (d: number) => ({ x: d > 0 ? 80 : -80, opacity: 0 }),
    center: { x: 0, opacity: 1 },
    exit: (d: number) => ({ x: d > 0 ? -80 : 80, opacity: 0 }),
  };

  return (
    // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
    <div
      className="modal-overlay"
      style={{ zIndex: 3100 }}
      onClick={onClose}
    >
      <motion.div
        role="dialog"
        aria-modal="true"
        aria-labelledby="onboarding-title"
        aria-describedby="onboarding-desc"
        ref={modalRef}
        {...getMotionProps(reducedMotion)}
        initial={{ opacity: 0, scale: 0.95, y: 20 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        exit={{ opacity: 0, scale: 0.95, y: 20 }}
        className="modal-content"
        style={{ maxWidth: current.image ? '600px' : '480px', overflow: 'hidden', transition: 'max-width 0.3s ease' }}
        onClick={e => e.stopPropagation()}
      >
        {/* Step content with slide animation */}
        <div style={{ minHeight: hasImage ? '320px' : '240px', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', position: 'relative' }}>
          <AnimatePresence mode="wait" custom={direction}>
            <motion.div
              key={step}
              custom={direction}
              variants={slideVariants}
              {...getMotionProps(reducedMotion)}
              initial="enter"
              animate="center"
              exit="exit"
              transition={{ duration: 0.25, ease: 'easeInOut' }}
              style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', width: '100%' }}
            >
              <div style={{
                width: '72px',
                height: '72px',
                borderRadius: '50%',
                background: 'var(--tag-bg)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                marginBottom: '1rem',
                color: 'var(--accent-primary)',
              }}>
                <Icon size={32} aria-hidden="true" />
              </div>

              <h3
                id="onboarding-title"
                style={{ margin: '0 0 0.5rem 0', fontSize: '1.3rem' }}
              >
                {t(current.titleKey)}
              </h3>

              <p
                id="onboarding-desc"
                style={{
                  margin: 0,
                  color: 'var(--text-secondary)',
                  lineHeight: '1.6',
                  fontSize: '0.95rem',
                  maxWidth: '420px',
                }}
              >
                {t(current.descKey)}
              </p>

              {current.image && (
                <div style={{
                  marginTop: '1.25rem',
                  width: '100%',
                  borderRadius: '10px',
                  overflow: 'hidden',
                  border: '1px solid var(--border-color)',
                  background: 'var(--bg-primary)',
                }}>
                  <img
                    src={current.image}
                    alt={t(current.titleKey)}
                    style={{
                      width: '100%',
                      height: 'auto',
                      display: 'block',
                    }}
                  />
                </div>
              )}
            </motion.div>
          </AnimatePresence>
        </div>

        {/* Step indicator dots */}
        <div style={{ display: 'flex', justifyContent: 'center', gap: '8px', margin: '1.5rem 0' }}>
          {steps.map((_, i) => (
            <button
              key={i}
              onClick={() => goTo(i)}
              aria-label={`Step ${i + 1}`}
              style={{
                width: '44px',
                height: '44px',
                borderRadius: '4px',
                border: 'none',
                background: 'transparent',
                cursor: 'pointer',
                padding: 0,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                transition: 'all 0.3s ease',
              }}
            >
              <span style={{
                width: i === step ? '24px' : '8px',
                height: '8px',
                borderRadius: '4px',
                background: i === step ? 'var(--accent-primary)' : 'var(--border-color)',
                transition: 'all 0.3s ease',
                display: 'block',
              }} />
            </button>
          ))}
        </div>

        {/* Navigation buttons */}
        <div style={{ display: 'flex', gap: '12px', justifyContent: 'space-between', alignItems: 'center' }}>
          {isFirst ? (
            <>
              <button
                onClick={onClose}
                style={{
                  flex: 1,
                  padding: '0.75rem',
                  borderRadius: '12px',
                  border: '1px solid var(--border-color)',
                  background: 'var(--bg-primary)',
                  color: 'var(--text-secondary)',
                  cursor: 'pointer',
                  fontWeight: 500,
                  fontSize: '0.95rem',
                }}
              >
                {t('onboardingSkip')}
              </button>
              <button
                onClick={next}
                style={{
                  flex: 1,
                  padding: '0.75rem',
                  borderRadius: '12px',
                  border: 'none',
                  background: 'var(--accent-primary)',
                  color: 'white',
                  cursor: 'pointer',
                  fontWeight: 600,
                  fontSize: '0.95rem',
                }}
              >
                {t('onboardingStartTour')}
              </button>
            </>
          ) : isLast ? (
            <>
              <button
                onClick={prev}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '4px',
                  padding: '0.75rem 1rem',
                  borderRadius: '12px',
                  border: '1px solid var(--border-color)',
                  background: 'var(--bg-primary)',
                  color: 'var(--text-primary)',
                  cursor: 'pointer',
                  fontWeight: 500,
                  fontSize: '0.95rem',
                }}
              >
                <ChevronLeft size={16} />
                {t('onboardingPrevious')}
              </button>
              <button
                onClick={onClose}
                style={{
                  flex: 1,
                  padding: '0.75rem',
                  borderRadius: '12px',
                  border: 'none',
                  background: 'var(--accent-primary)',
                  color: 'white',
                  cursor: 'pointer',
                  fontWeight: 600,
                  fontSize: '0.95rem',
                }}
              >
                {t('onboardingGetStarted')}
              </button>
            </>
          ) : (
            <>
              <button
                onClick={prev}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '4px',
                  padding: '0.75rem 1rem',
                  borderRadius: '12px',
                  border: '1px solid var(--border-color)',
                  background: 'var(--bg-primary)',
                  color: 'var(--text-primary)',
                  cursor: 'pointer',
                  fontWeight: 500,
                  fontSize: '0.95rem',
                }}
              >
                <ChevronLeft size={16} />
                {t('onboardingPrevious')}
              </button>
              <button
                onClick={next}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '4px',
                  padding: '0.75rem 1rem',
                  borderRadius: '12px',
                  border: 'none',
                  background: 'var(--accent-primary)',
                  color: 'white',
                  cursor: 'pointer',
                  fontWeight: 600,
                  fontSize: '0.95rem',
                }}
              >
                {t('onboardingNext')}
                <ChevronRight size={16} />
              </button>
            </>
          )}
        </div>
      </motion.div>
    </div>
  );
}
