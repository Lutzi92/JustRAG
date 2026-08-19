import React, { useRef } from 'react';
import { Download } from 'lucide-react';
import {
    BarChart, Bar, LineChart, Line, AreaChart, Area, PieChart, Pie, Cell, ScatterChart, Scatter,
    XAxis, YAxis, ZAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer,
} from 'recharts';
import type { ChartContentData } from '../../types';
import { useTheme } from '../../contexts/ThemeContext';

const COLORS = ['#165a97', '#2d8f4e', '#cc8400', '#c0392b', '#6a1b9a', '#d84315'];

/**
 * Chart artifact view: rendered chart + SVG download. Ported from the retired
 * `ContentModal` (fix wave item 1) so charts opened from the Workspace keep a
 * viewer and a download control instead of a raw JSON dump.
 */
export const ChartArtifact: React.FC<{ content: ChartContentData; title: string }> = ({ content, title }) => {
    const { t } = useTheme();
    const containerRef = useRef<HTMLDivElement>(null);

    const { type, config, series } = content;
    // Support both old and new schema
    const chartData = (series || (config as { data?: Record<string, unknown>[] }).data) as Record<string, unknown>[] | undefined;
    const xAxisKey = ((config as { xAxis?: string; xKey?: string }).xAxis || (config as { xKey?: string }).xKey) as string | undefined;
    const yAxisKeys = ((config as { keys?: string[]; yKeys?: string[] }).keys || (config as { yKeys?: string[] }).yKeys || []) as string[];

    const handleDownload = () => {
        const svg = containerRef.current?.querySelector('svg');
        if (!svg) return;
        const svgData = new XMLSerializer().serializeToString(svg);
        const blob = new Blob([svgData], { type: 'image/svg+xml;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        const link = document.createElement('a');
        link.href = url;
        link.download = `${title.replace(/[^a-z0-9]/gi, '_').toLowerCase()}.svg`;
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
        URL.revokeObjectURL(url);
    };

    const renderChart = () => {
        if (!chartData || chartData.length === 0) {
            return <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-secondary)' }}>{t('noDataAvailable')}</div>;
        }

        const commonProps = {
            data: chartData,
            margin: { top: 20, right: 30, left: 20, bottom: 5 },
        };

        switch (type) {
            case 'line':
                return (
                    <LineChart {...commonProps}>
                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                        <YAxis axisLine={false} tickLine={false} />
                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                        <Legend />
                        {yAxisKeys.map((key: string, idx: number) => (
                            <Line key={key} type="monotone" dataKey={key} stroke={COLORS[idx % COLORS.length]} strokeWidth={2} dot={{ r: 4 }} activeDot={{ r: 6 }} />
                        ))}
                    </LineChart>
                );
            case 'area':
                return (
                    <AreaChart {...commonProps}>
                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                        <YAxis axisLine={false} tickLine={false} />
                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                        <Legend />
                        {yAxisKeys.map((key: string, idx: number) => (
                            <Area key={key} type="monotone" dataKey={key} fill={COLORS[idx % COLORS.length]} stroke={COLORS[idx % COLORS.length]} fillOpacity={0.6} />
                        ))}
                    </AreaChart>
                );
            case 'pie':
                return (
                    <PieChart>
                        <Pie
                            data={chartData}
                            dataKey={(config.valueKey as string) || yAxisKeys?.[0] || 'value'}
                            nameKey={(config.nameKey as string) || xAxisKey || 'name'}
                            cx="50%"
                            cy="50%"
                            outerRadius={120}
                            label={({ name, percent }: { name?: string; percent?: number }) => `${name ?? ''} ${((percent ?? 0) * 100).toFixed(0)}%`}
                            labelLine={false}
                        >
                            {chartData.map((_: unknown, index: number) => (
                                <Cell key={`cell-${index}`} fill={COLORS[index % COLORS.length]} />
                            ))}
                        </Pie>
                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                        <Legend />
                    </PieChart>
                );
            case 'scatter':
                return (
                    <ScatterChart margin={{ top: 20, right: 30, left: 20, bottom: 5 }}>
                        <CartesianGrid strokeDasharray="3 3" />
                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                        <YAxis dataKey={(config.yAxis as string) || yAxisKeys?.[0]} axisLine={false} tickLine={false} />
                        <ZAxis range={[60, 400]} />
                        <Tooltip cursor={{ strokeDasharray: '3 3' }} contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                        <Legend />
                        <Scatter name={yAxisKeys?.[0] || 'Data'} data={chartData} fill={COLORS[0]} />
                    </ScatterChart>
                );
            case 'bar':
            default:
                return (
                    <BarChart {...commonProps}>
                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                        <YAxis axisLine={false} tickLine={false} />
                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                        <Legend />
                        {yAxisKeys.map((key: string, idx: number) => (
                            <Bar key={key} dataKey={key} fill={COLORS[idx % COLORS.length]} radius={[4, 4, 0, 0]} />
                        ))}
                    </BarChart>
                );
        }
    };

    return (
        <div style={{ padding: '1rem', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
            <div>
                <button className="secondary-button" onClick={handleDownload}>
                    <Download size={16} aria-hidden="true" /> {t('downloadSvg')}
                </button>
            </div>
            {content.description && <p style={{ color: 'var(--text-secondary)', margin: '1rem 0' }}>{content.description}</p>}
            <div ref={containerRef} style={{ width: '100%', height: 400 }}>
                <ResponsiveContainer width="100%" height="100%">
                    {renderChart()}
                </ResponsiveContainer>
            </div>
        </div>
    );
};
