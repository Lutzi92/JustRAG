import './ChatComparisonControls.css';
import type { ChatAttachment } from '../hooks/useChatAttachment';

export type ComparisonMode = 'contradiction' | 'formal' | 'completeness';

/**
 * Default (English) labels. Pass `t` (the `useTheme()` translator) to localize;
 * the matching keys live in `translations.ts` (`comparisonMode*`,
 * `comparisonSections`, `comparisonRemoveAttachment`, `comparisonModesLabel`).
 */
const MODES: { id: ComparisonMode; key: string; label: string }[] = [
  { id: 'contradiction', key: 'comparisonModeContradiction', label: 'Contradiction' },
  { id: 'formal', key: 'comparisonModeFormal', label: 'Formal' },
  { id: 'completeness', key: 'comparisonModeCompleteness', label: 'Completeness' },
];

type Translator = (key: string) => string;

export function ChatComparisonControls(props: {
  attachment: ChatAttachment | null;
  selectedModes: ComparisonMode[];
  onModesChange: (m: ComparisonMode[]) => void;
  onClear: () => void;
  t?: Translator;
}) {
  const { attachment, selectedModes, onModesChange, onClear, t } = props;
  if (!attachment) return null;

  const label = (key: string, fallback: string) => (t ? t(key) : fallback);
  const toggle = (m: ComparisonMode) =>
    onModesChange(
      selectedModes.includes(m)
        ? selectedModes.filter((x) => x !== m)
        : [...selectedModes, m],
    );

  return (
    <div className="chat-comparison-controls">
      <span className="chat-comparison-chip">
        📎 {attachment.filename} ({attachment.sectionCount} {label('comparisonSections', 'sections')})
        <button
          type="button"
          aria-label={label('comparisonRemoveAttachment', 'remove attachment')}
          onClick={onClear}
        >
          ×
        </button>
      </span>
      <div
        className="chat-comparison-modes"
        role="group"
        aria-label={label('comparisonModesLabel', 'comparison modes')}
      >
        {MODES.map((m) => (
          <button
            key={m.id}
            type="button"
            aria-pressed={selectedModes.includes(m.id)}
            className={selectedModes.includes(m.id) ? 'mode active' : 'mode'}
            onClick={() => toggle(m.id)}
          >
            {label(m.key, m.label)}
          </button>
        ))}
      </div>
    </div>
  );
}
