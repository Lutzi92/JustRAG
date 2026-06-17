import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { ComparisonFindings } from './ComparisonFindings';

describe('ComparisonFindings', () => {
  it('groups findings by mode and shows severity + issue', () => {
    render(
      <ComparisonFindings findings={[
        { mode: 'contradiction', severity: 'high', sectionIdx: 0, uploadQuote: 'ECTS: 6', issue: 'ECTS differs', citedFileIds: ['F1'], citedQuote: 'ECTS: 5' },
        { mode: 'completeness', severity: 'low', sectionIdx: 1, uploadQuote: '', issue: 'missing learning outcomes', citedFileIds: ['F2'], citedQuote: '' },
      ]} />
    );
    expect(screen.getByText(/ECTS differs/)).toBeInTheDocument();
    expect(screen.getByText(/missing learning outcomes/)).toBeInTheDocument();
    expect(screen.getByText(/high/i)).toBeInTheDocument();
  });

  it('renders nothing for empty findings', () => {
    const { container } = render(<ComparisonFindings findings={[]} />);
    expect(container).toBeEmptyDOMElement();
  });
});
