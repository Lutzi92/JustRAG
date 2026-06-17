import './ComparisonFindings.css';
import type { ComparisonFinding } from '../types';

export type { ComparisonFinding };

/**
 * Default (English) section labels per comparison mode. Pass `t` (the
 * `useTheme()` translator) to localize via the `comparisonMode*` keys;
 * English fallbacks are used when `t` is omitted.
 */
const MODE_LABEL: Record<ComparisonFinding['mode'], string> = {
  contradiction: 'Contradictions',
  formal: 'Formal / structural',
  completeness: 'Completeness',
};

const MODE_KEY: Record<ComparisonFinding['mode'], string> = {
  contradiction: 'comparisonModeContradiction',
  formal: 'comparisonModeFormal',
  completeness: 'comparisonModeCompleteness',
};

type Translator = (key: string) => string;

export function ComparisonFindings({
  findings,
  t,
}: {
  findings?: ComparisonFinding[];
  t?: Translator;
}) {
  if (!findings || findings.length === 0) return null;

  const label = (key: string, fallback: string) => (t ? t(key) : fallback);

  const byMode = findings.reduce<Record<string, ComparisonFinding[]>>((acc, f) => {
    (acc[f.mode] ||= []).push(f);
    return acc;
  }, {});

  return (
    <div className="comparison-findings">
      {Object.entries(byMode).map(([mode, list]) => {
        const m = mode as ComparisonFinding['mode'];
        return (
          <section key={mode}>
            <h4>{MODE_KEY[m] ? label(MODE_KEY[m], MODE_LABEL[m]) : (MODE_LABEL[m] ?? mode)}</h4>
            {list.map((f, i) => (
              <div key={i} className={`comparison-finding sev-${f.severity}`}>
                <span className="sev-badge">{f.severity}</span>
                <p className="issue">{f.issue}</p>
                {f.uploadQuote && <blockquote className="upload">{'“'}{f.uploadQuote}{'”'}</blockquote>}
                {f.citedQuote && (
                  <blockquote className="cited">
                    {label('comparisonCitedKb', 'KB')}: {'“'}{f.citedQuote}{'”'}
                  </blockquote>
                )}
                {f.citedFileIds.length > 0 && (
                  <p className="cited-files">
                    {label('comparisonCitedFiles', 'Files')}: {f.citedFileIds.join(', ')}
                  </p>
                )}
              </div>
            ))}
          </section>
        );
      })}
    </div>
  );
}
