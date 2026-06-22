import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { IngestStageIndicator } from './IngestStageIndicator';
import { translations } from '../../translations';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

describe('IngestStageIndicator', () => {
  it('shows n/x and the mapped stage label', () => {
    render(<IngestStageIndicator stage="embed" index={3} total={5} fileName="doc.pdf" />);
    expect(screen.getByText('3/5')).toBeInTheDocument();
    expect(screen.getByText('Generating embeddings')).toBeInTheDocument();
  });

  it('falls back to the generic label for an unknown stage', () => {
    render(<IngestStageIndicator stage="future_stage" index={1} total={2} fileName="doc.pdf" />);
    expect(screen.getByText('Processing')).toBeInTheDocument();
  });

  it('omits the counter when index/total are missing', () => {
    render(<IngestStageIndicator stage="kg" fileName="doc.pdf" />);
    expect(screen.queryByText(/\//)).not.toBeInTheDocument();
  });
});
