import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import axios from 'axios';
import { PodcastArtifact } from './PodcastArtifact';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));
vi.mock('axios');

describe('PodcastArtifact', () => {
    beforeEach(() => {
        vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-audio');
        vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('zeigt Audio-Player und Download-Button, wenn Audio vorhanden ist', async () => {
        vi.mocked(axios.get).mockResolvedValue({ data: new Blob(['audio-bytes']) });
        const { container } = render(
            <PodcastArtifact id="p1" content={{ audioPath: '/x.mp3', script: 'Hallo Welt' }} />,
        );

        await waitFor(() => {
            expect(container.querySelector('audio')).toBeInTheDocument();
        });
        expect(screen.getByRole('button', { name: /downloadAudio/ })).toBeInTheDocument();
        expect(screen.getByText('Hallo Welt')).toBeInTheDocument();
    });

    it('zeigt weder Player noch Download-Button ohne Audiopfad', () => {
        render(<PodcastArtifact id="p2" content={{ script: 'Nur Text' }} />);
        expect(screen.queryByRole('button', { name: /downloadAudio/ })).not.toBeInTheDocument();
        expect(screen.getByText('Nur Text')).toBeInTheDocument();
    });
});
