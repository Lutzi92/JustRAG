import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import axios from 'axios';

vi.mock('axios', () => ({ default: { get: vi.fn() } }));
const mockedGet = axios.get as unknown as ReturnType<typeof vi.fn>;

import { EntityCard } from './EntityCard';

const t = (k: string) => k;
const detail = {
  id: 7, name: 'Alice, Inc.', type: 'org', aliases: [], degree: 2,
  sources: [{ fileId: 'f1', fileName: 'report.pdf', chunkId: 'c1' }],
  neighbors: [{ id: 9, name: 'Bob', type: 'person', rel: 'employs' }],
};

describe('EntityCard', () => {
  beforeEach(() => vi.clearAllMocks());

  it('fetches and renders entity detail; source opens the document', async () => {
    mockedGet.mockResolvedValueOnce({ data: detail });
    const onOpenSource = vi.fn();
    render(<EntityCard kbId="kb1" entityId="7" entityName="Alice, Inc." onAsk={() => {}} onOpenSource={onOpenSource} onClose={() => {}} t={t} />);
    await waitFor(() => expect(screen.getByText('report.pdf')).toBeTruthy());
    fireEvent.click(screen.getByText('report.pdf'));
    expect(onOpenSource).toHaveBeenCalledWith('f1', 'report.pdf');
  });

  it('fires onAsk with the entity name', async () => {
    mockedGet.mockResolvedValueOnce({ data: detail });
    const onAsk = vi.fn();
    render(<EntityCard kbId="kb1" entityId="7" entityName="Alice, Inc." onAsk={onAsk} onOpenSource={() => {}} onClose={() => {}} t={t} />);
    await waitFor(() => expect(screen.getByText('report.pdf')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /askAbout/ }));
    expect(onAsk).toHaveBeenCalledWith('Alice, Inc.');
  });

  it('shows an error but still offers Ask when detail fetch fails', async () => {
    mockedGet.mockRejectedValueOnce(new Error('boom'));
    const onAsk = vi.fn();
    render(<EntityCard kbId="kb1" entityId="7" entityName="Alice, Inc." onAsk={onAsk} onOpenSource={() => {}} onClose={() => {}} t={t} />);
    await waitFor(() => expect(screen.getByText('entityDetailError')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /askAbout/ }));
    expect(onAsk).toHaveBeenCalledWith('Alice, Inc.');
  });
});
