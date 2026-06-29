import { lazy, Suspense } from 'react';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, LineChart, Line, PieChart, Pie, Cell, AreaChart, Area, ScatterChart, Scatter, ZAxis } from 'recharts';
import type { DataExplorerConfig, FileSchema } from '../types';

const DataExplorer = lazy(() => import('./DataExplorer/DataExplorer'));

const COLORS = ['#165a97', '#2d8f4e', '#cc8400', '#c0392b', '#6a1b9a', '#d84315'];

interface ChartConfig {
    xAxis?: string;
    yAxis?: string;
    keys: string[];
    valueKey?: string;
    nameKey?: string;
}

interface ChartData {
    type: 'bar' | 'line' | 'pie' | 'area' | 'scatter';
    config: ChartConfig;
    series: Record<string, unknown>[];
    description?: string;
    title?: string;
    // New fields for DataExplorer
    explorerConfig?: DataExplorerConfig;
    fileId?: string;
    schema?: FileSchema;
    kbId?: string;
}

// Render the chart-error fallback. Kept as a plain helper (not a component) so
// the parse/validation guards below can return it directly without wrapping JSX
// in a try/catch — render-time errors are owned by React error boundaries.
const renderChartError = (content: string, errorMessage: string) => (
    <div className="chart-container" style={{ padding: '1.5rem', background: 'var(--bg-secondary)', border: '1px solid var(--error-color)', borderRadius: '12px' }}>
        <div style={{ color: 'var(--error-color)', fontWeight: 600, marginBottom: '0.5rem' }}>Fehler beim Anzeigen des Diagramms</div>
        <div style={{ fontSize: '0.9rem', color: 'var(--text-secondary)' }}>{errorMessage}</div>
        <details style={{ marginTop: '0.5rem' }}>
            <summary style={{ cursor: 'pointer', fontSize: '0.85rem' }}>Rohdaten anzeigen</summary>
            <pre style={{ fontSize: '0.8rem', overflow: 'auto', maxHeight: '200px' }}><code>{content}</code></pre>
        </details>
    </div>
);

const ChartRenderer = ({ content, language }: { content: string; language?: string }) => {
    if (language !== 'chart') {
        return (
            <pre>
                <code className={language ? `language-${language}` : ''}>
                    {content}
                </code>
            </pre>
        );
    }

    let data: ChartData;
    try {
        data = JSON.parse(content);
    } catch (e: unknown) {
        console.error('Failed to parse chart data:', e);
        return renderChartError(content, e instanceof Error ? e.message : 'Unknown error');
    }

        // Delegate to DataExplorer if explorerConfig is present
        if (data.explorerConfig && data.fileId && data.schema && data.kbId) {
            return (
                <div className="chart-container" style={{ width: '100%', minHeight: 350, marginTop: '1rem', marginBottom: '1rem', background: 'var(--bg-secondary)', padding: '1.5rem', borderRadius: '12px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-sm)' }}>
                    <Suspense fallback={
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: 300, color: 'var(--text-secondary)' }}>
                            Loading Data Explorer...
                        </div>
                    }>
                        <DataExplorer
                            initialConfig={data.explorerConfig}
                            initialSeries={data.series}
                            schema={data.schema}
                            fileId={data.fileId}
                            kbId={data.kbId}
                        />
                    </Suspense>
                </div>
            );
        }

        // Fall through to existing static chart rendering
        const { type, config, series, description } = data;

        if (!type || !config || !series) {
            return renderChartError(content, 'Invalid chart data: missing required fields');
        }

        if (!Array.isArray(series) || series.length === 0) {
            return (
                <div className="chart-container" style={{ padding: '1.5rem', textAlign: 'center', color: 'var(--text-secondary)' }}>
                    Keine Daten zum Anzeigen verfügbar.
                </div>
            );
        }

        const renderChart = () => {
            switch (type) {
                case 'bar':
                    return (
                        <BarChart data={series}>
                            <CartesianGrid strokeDasharray="3 3" vertical={false} />
                            <XAxis dataKey={config.xAxis} axisLine={false} tickLine={false} />
                            <YAxis axisLine={false} tickLine={false} />
                            <Tooltip cursor={{ fill: 'rgba(0,0,0,0.05)' }} />
                            <Legend />
                            {config.keys.map((key: string, index: number) => (
                                <Bar key={key} dataKey={key} fill={COLORS[index % COLORS.length]} radius={[4, 4, 0, 0]} />
                            ))}
                        </BarChart>
                    );
                case 'line':
                    return (
                        <LineChart data={series}>
                            <CartesianGrid strokeDasharray="3 3" vertical={false} />
                            <XAxis dataKey={config.xAxis} axisLine={false} tickLine={false} />
                            <YAxis axisLine={false} tickLine={false} />
                            <Tooltip />
                            <Legend />
                            {config.keys.map((key: string, index: number) => (
                                <Line key={key} type="monotone" dataKey={key} stroke={COLORS[index % COLORS.length]} strokeWidth={2} dot={{ r: 4 }} activeDot={{ r: 6 }} />
                            ))}
                        </LineChart>
                    );
                case 'pie':
                    return (
                        <PieChart>
                            <Pie
                                data={series}
                                cx="50%"
                                cy="50%"
                                labelLine={false}
                                outerRadius={80}
                                fill={COLORS[0]}
                                dataKey={config.valueKey || 'value'}
                                nameKey={config.nameKey || 'name'}
                                label={({ name, percent }: { name?: string; percent?: number }) => `${name || ''} ${((percent || 0) * 100).toFixed(0)}%`}
                            >
                                {series.map((_entry, index) => (
                                    <Cell key={`cell-${index}`} fill={COLORS[index % COLORS.length]} />
                                ))}
                            </Pie>
                            <Tooltip />
                            <Legend />
                        </PieChart>
                    );
                case 'area':
                    return (
                        <AreaChart data={series}>
                            <CartesianGrid strokeDasharray="3 3" vertical={false} />
                            <XAxis dataKey={config.xAxis} axisLine={false} tickLine={false} />
                            <YAxis axisLine={false} tickLine={false} />
                            <Tooltip />
                            <Legend />
                            {config.keys.map((key: string, index: number) => (
                                <Area
                                    key={key}
                                    type="monotone"
                                    dataKey={key}
                                    stroke={COLORS[index % COLORS.length]}
                                    fill={COLORS[index % COLORS.length]}
                                    fillOpacity={0.6}
                                />
                            ))}
                        </AreaChart>
                    );
                case 'scatter':
                    return (
                        <ScatterChart>
                            <CartesianGrid strokeDasharray="3 3" />
                            <XAxis dataKey={config.xAxis} axisLine={false} tickLine={false} />
                            <YAxis dataKey={config.yAxis} axisLine={false} tickLine={false} />
                            <ZAxis range={[60, 400]} />
                            <Tooltip cursor={{ strokeDasharray: '3 3' }} />
                            <Legend />
                            <Scatter
                                name={config.keys[0] || 'Data'}
                                data={series}
                                fill={COLORS[0]}
                            />
                        </ScatterChart>
                    );
                default:
                    return <div>Nicht unterstützter Diagrammtyp: {type}</div>;
            }
        };

        return (
            <div className="chart-container" style={{ width: '100%', minHeight: 350, marginTop: '1rem', marginBottom: '1rem', background: 'var(--bg-secondary)', padding: '1.5rem', borderRadius: '12px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-sm)' }}>
                {data.title && <div style={{ fontWeight: 600, marginBottom: '0.5rem', textAlign: 'center', color: 'var(--text-primary)', fontSize: '1.1rem' }}>{data.title}</div>}
                {description && <div style={{ fontSize: '0.85rem', marginBottom: '1rem', textAlign: 'center', color: 'var(--text-secondary)' }}>{description}</div>}
                <div style={{ width: '100%', height: 300 }}>
                    <ResponsiveContainer width="100%" height="100%">
                        {renderChart()}
                    </ResponsiveContainer>
                </div>
            </div>
        );
};

export default ChartRenderer;
