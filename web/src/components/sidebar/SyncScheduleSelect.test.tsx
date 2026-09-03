import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { SyncScheduleSelect } from './SyncScheduleSelect';

vi.mock('../../contexts/ThemeContext', () => ({
    useTheme: () => ({ t: (key: string) => key }),
}));

describe('SyncScheduleSelect', () => {
    it('offers exactly manual, daily and weekly', () => {
        render(<SyncScheduleSelect id="s" value="manual" onChange={() => {}} />);
        const options = screen.getAllByRole('option');
        expect(options.map(o => (o as HTMLOptionElement).value)).toEqual(['manual', 'daily', 'weekly']);
    });

    it('reports the selected value', () => {
        const onChange = vi.fn();
        render(<SyncScheduleSelect id="s" value="manual" onChange={onChange} />);
        fireEvent.change(screen.getByRole('combobox'), { target: { value: 'weekly' } });
        expect(onChange).toHaveBeenCalledWith('weekly');
    });
});
