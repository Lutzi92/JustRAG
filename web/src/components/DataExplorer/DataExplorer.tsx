import { useState, useRef, useCallback, lazy, Suspense } from 'react';
import { API_BASE_URL, authFetch } from '../../api';
import type { DataExplorerConfig, FileSchema, DataExplorerQueryResult } from '../../types';
import DataExplorerToolbar from './DataExplorerToolbar';
import { exportCsv, exportPng } from './ExportButtons';

const ChartRenderer = lazy(() => import('../ChartRenderer'));

interface DataExplorerProps {
    initialConfig: DataExplorerConfig;
    initialSeries: Record<string, unknown>[];
    schema: FileSchema;
    fileId: string;
    kbId: string;
}

export default function DataExplorer({ initialConfig, initialSeries, schema, fileId, kbId }: DataExplorerProps) {
    const [config, setConfig] = useState<DataExplorerConfig>(initialConfig);
    const [series, setSeries] = useState<Record<string, unknown>[]>(initialSeries);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const chartRef = useRef<HTMLDivElement>(null);
    const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

    // Track whether this is a chart-type-only change (no backend call needed)
    const prevQueryRef = useRef(JSON.stringify(initialConfig.query));

    const executeQuery = useCallback(async (newConfig: DataExplorerConfig) => {
        const queryStr = JSON.stringify(newConfig.query);

        // Skip backend call if only chart type or display changed
        if (queryStr === prevQueryRef.current) {
            return;
        }
        prevQueryRef.current = queryStr;

        setLoading(true);
        setError(null);

        try {
            const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/data-explorer/query`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    fileId,
                    ...newConfig.query,
                }),
            });

            if (!response.ok) {
                const err = await response.json().catch(() => ({ error: 'Query failed' }));
                throw new Error(err.error || 'Query failed');
            }

            const result: DataExplorerQueryResult = await response.json();
            setSeries(result.rows);
        } catch (err: unknown) {
            setError(err instanceof Error ? err.message : 'Query failed');
        } finally {
            setLoading(false);
        }
    }, [kbId, fileId]);

    const handleConfigChange = useCallback((newConfig: DataExplorerConfig) => {
        setConfig(newConfig);

        // Debounce backend calls
        if (debounceRef.current) {
            clearTimeout(debounceRef.current);
        }
        debounceRef.current = setTimeout(() => {
            executeQuery(newConfig);
        }, 300);
    }, [executeQuery]);

    const handleExportCsv = useCallback(async () => {
        try {
            await exportCsv(kbId, fileId, config.query);
        } catch (err: unknown) {
            setError(err instanceof Error ? err.message : 'Export failed');
        }
    }, [kbId, fileId, config.query]);

    const handleExportPng = useCallback(() => {
        exportPng(chartRef, config.title);
    }, [config.title]);

    // Build the chart data JSON that ChartRenderer expects
    const chartContent = buildChartContent(config, series);

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
            <DataExplorerToolbar
                config={config}
                schema={schema}
                onChange={handleConfigChange}
                onExportCsv={handleExportCsv}
                onExportPng={handleExportPng}
                loading={loading}
            />

            {error && (
                <div style={{
                    padding: '0.5rem 0.75rem',
                    background: 'var(--error-bg, #fef2f2)',
                    color: 'var(--error-text)',
                    borderRadius: '6px',
                    fontSize: '0.85rem',
                    border: '1px solid var(--error-text)',
                }}>
                    {error}
                </div>
            )}

            <div ref={chartRef}>
                <Suspense fallback={<div style={{ height: 300, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>Loading chart...</div>}>
                    <ChartRenderer content={chartContent} language="chart" />
                </Suspense>
            </div>
        </div>
    );
}

/**
 * Build the JSON string that ChartRenderer expects from the current config and series data.
 */
function buildChartContent(config: DataExplorerConfig, series: Record<string, unknown>[]): string {
    const chartData: Record<string, unknown> = {
        type: config.chartType,
        title: config.title,
        description: config.description,
        series,
        config: {},
    };

    if (config.chartType === 'pie') {
        chartData.config = {
            nameKey: config.display.nameKey || config.query.groupBy || 'name',
            valueKey: config.display.valueKey || config.query.aggregations?.[0]?.alias || 'value',
        };
    } else if (config.chartType === 'scatter') {
        chartData.config = {
            xAxis: config.display.xAxis,
            yAxis: config.display.yAxis?.[0],
            keys: config.display.yAxis || [],
        };
    } else {
        chartData.config = {
            xAxis: config.display.xAxis || config.query.groupBy || 'index',
            keys: config.display.yAxis || config.query.aggregations?.map(
                a => a.alias || `${a.fn.toLowerCase()}_${a.column}`
            ) || [],
        };
    }

    return JSON.stringify(chartData);
}
