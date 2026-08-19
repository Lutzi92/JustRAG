import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi } from 'vitest';
import { FlashcardsArtifact } from './FlashcardsArtifact';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

const cards = [
    { front: 'Frage 1', back: 'Antwort 1' },
    { front: 'Frage 2', back: 'Antwort 2' },
];

describe('FlashcardsArtifact', () => {
    it('zeigt die erste Karte und den CSV-Export-Button', () => {
        render(<FlashcardsArtifact id="f1" content={cards} />);
        expect(screen.getByText('Frage 1')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /exportCsv/ })).toBeInTheDocument();
        expect(screen.getByText('1 / 2')).toBeInTheDocument();
    });

    it('blättert zur nächsten Karte', async () => {
        render(<FlashcardsArtifact id="f2" content={cards} />);
        await userEvent.click(screen.getByLabelText('nextCard'));
        expect(screen.getByText('Frage 2')).toBeInTheDocument();
        expect(screen.getByText('2 / 2')).toBeInTheDocument();
    });

    it('setzt Index und Kartenseite zurück, wenn ein anderes Artefakt gewählt wird', async () => {
        const { rerender } = render(<FlashcardsArtifact id="f3" content={cards} />);
        await userEvent.click(screen.getByLabelText('nextCard'));
        expect(screen.getByText('Frage 2')).toBeInTheDocument();

        rerender(<FlashcardsArtifact id="f4" content={cards} />);
        expect(screen.getByText('Frage 1')).toBeInTheDocument();
        expect(screen.getByText('1 / 2')).toBeInTheDocument();
    });
});
