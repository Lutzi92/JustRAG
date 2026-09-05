import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import { TabularDetailModal } from './TabularDetailModal';
import { API_BASE_URL } from '../api';
import type { TabularFileDetail } from '../types';

// Automocks every axios method to a vi.fn() (repo convention — see
// hooks/useSharing.test.tsx / MembersModal.test.tsx). Tests below then use
// vi.spyOn(axios, 'get')... per test.
vi.mock('axios');

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({ t: (k: string) => k, language: 'de' }),
}));

const fixture: TabularFileDetail = {
  report: {
    version: 1,
    materialised: true,
    sheets: [
      {
        name: 'Gebäude',
        kind: 'table',
        hidden: true,
        header_row: 0,
        columns: 2,
        rows_read: 120000,
        rows_materialised: 120000,
        rows_embedded: 5000,
        rows_past_cap: 0,
        formula_cells_empty: 2,
        coercion_failures: 1,
        used_llm: true,
        dropped_columns: 1,
        notes: ['Ein Hinweis'],
        tables: ['Gebäude'],
      },
    ],
  },
  tables: [
    {
      sheet_index: 0,
      region_index: 0,
      sheet_name: 'Gebäude',
      table_name: 'gebaeude',
      sheet_kind: 'table',
      hidden: true,
      header_row: 0,
      row_count: 120000,
      columns: [
        {
          original: 'Baujahr', name: 'baujahr', type: 'text',
          null_count: 0, distinct_count: 50, coercion_failed: 0,
          shadow_column: 'baujahr_num',
        },
        {
          original: 'Baujahr (Zahl)', name: 'baujahr_num', type: 'numeric', role: 'measure',
          null_count: 3, distinct_count: 40, coercion_failed: 3,
          shadow_of: 'baujahr',
        },
      ],
    },
  ],
};

const baseProps = {
  show: true,
  kbId: 'kb1',
  fileId: 'f1',
  fileName: 'gebaeude.xlsx',
  onClose: vi.fn(),
};

beforeEach(() => {
  vi.clearAllMocks();
});

describe('TabularDetailModal', () => {
  it('renders the sheet name, hidden badge, counts, and a column row with baujahr_num in the Shadow cell', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: fixture });
    render(<TabularDetailModal {...baseProps} />);

    expect(await screen.findByText('Gebäude')).toBeInTheDocument();
    expect(screen.getByText('tabularHidden')).toBeInTheDocument();
    // rows_read formatted with de-DE grouping.
    expect(screen.getByText(/tabularRowsRead:\s*120\.000/)).toBeInTheDocument();

    // The "baujahr" row's Shadow cell carries its shadow column's name.
    const baujahrRow = screen.getByText('Baujahr').closest('tr');
    expect(baujahrRow).not.toBeNull();
    expect(baujahrRow).toHaveTextContent('baujahr_num');
  });

  it('shows the empty-state text when report is null', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: { report: null, tables: [] } });
    render(<TabularDetailModal {...baseProps} />);

    expect(await screen.findByText('tabularNoReport')).toBeInTheDocument();
  });

  it('shows an error state when the request rejects', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue(new Error('network error'));
    render(<TabularDetailModal {...baseProps} />);

    expect(await screen.findByText('tabularLoadError')).toBeInTheDocument();
  });

  it('fetches from the exact per-file tabular endpoint', async () => {
    const getSpy = vi.spyOn(axios, 'get').mockResolvedValue({ data: fixture });
    render(<TabularDetailModal {...baseProps} />);

    await waitFor(() => {
      expect(getSpy).toHaveBeenCalledWith(`${API_BASE_URL}/api/kb/kb1/files/f1/tabular`);
    });
  });

  it('renders nothing when show is false', () => {
    const getSpy = vi.spyOn(axios, 'get').mockResolvedValue({ data: fixture });
    const { container } = render(<TabularDetailModal {...baseProps} show={false} />);
    expect(container).toBeEmptyDOMElement();
    expect(getSpy).not.toHaveBeenCalled();
  });
});
