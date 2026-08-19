import type { ReactNode } from 'react';
import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { ChartArtifact } from './ChartArtifact';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

// recharts' ResponsiveContainer needs a measured, non-zero DOM box to render
// its children at all — jsdom never provides one, so every sub-component here
// is stubbed to a plain div that always renders. This suite is about the
// artifact's own chrome (download control, description), not chart pixels.
vi.mock('recharts', () => {
    const Stub = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
    return {
        ResponsiveContainer: Stub, BarChart: Stub, Bar: Stub, LineChart: Stub, Line: Stub,
        AreaChart: Stub, Area: Stub, PieChart: Stub, Pie: Stub, Cell: Stub,
        ScatterChart: Stub, Scatter: Stub, XAxis: Stub, YAxis: Stub, ZAxis: Stub,
        CartesianGrid: Stub, Tooltip: Stub, Legend: Stub,
    };
});

describe('ChartArtifact', () => {
    it('zeigt Beschreibung und SVG-Download-Button', () => {
        render(
            <ChartArtifact
                title="Umsatz Q2"
                content={{ type: 'bar', description: 'Umsatz je Quartal', config: { xAxis: 'q', keys: ['value'] }, series: [{ q: 'Q1', value: 10 }] }}
            />,
        );
        expect(screen.getByText('Umsatz je Quartal')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /downloadSvg/ })).toBeInTheDocument();
    });

    it('zeigt einen Hinweis, wenn keine Daten vorhanden sind', () => {
        render(<ChartArtifact title="Leer" content={{ type: 'bar', config: {}, series: [] }} />);
        expect(screen.getByText('noDataAvailable')).toBeInTheDocument();
    });
});
