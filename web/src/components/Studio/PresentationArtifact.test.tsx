import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { PresentationArtifact } from './PresentationArtifact';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));

describe('PresentationArtifact', () => {
    it('zeigt Folienansicht und Download-Button, wenn eine Datei vorhanden ist', () => {
        render(
            <PresentationArtifact id="pr1" content={{ filePath: '/slides.pptx', summary: 'Kurzfassung der Folien' }} />,
        );
        expect(screen.getByText('presentationCreated')).toBeInTheDocument();
        expect(screen.getByText('Kurzfassung der Folien')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /downloadPptx/ })).toBeInTheDocument();
    });

    it('zeigt keinen Download-Button ohne Datei oder Markdown', () => {
        render(<PresentationArtifact id="pr2" content={{ summary: 'Nur Zusammenfassung' }} />);
        expect(screen.queryByRole('button', { name: /downloadPptx/ })).not.toBeInTheDocument();
    });
});
