import { useState } from 'react';
import { BarChart3, LineChart, AreaChart, PieChart, ScatterChart as ScatterIcon, Filter, X, Download } from 'lucide-react';
import type { FileSchema, DataExplorerConfig } from '../../types';
import { useIsMobileContext } from '../../contexts/MobileContext';
import { useTheme } from '../../contexts/ThemeContext';

type ChartType = DataExplorerConfig['chartType'];
type AggFn = 'SUM' | 'AVG' | 'COUNT' | 'MIN' | 'MAX';
type DateTruncUnit = 'year' | 'quarter' | 'month' | 'week' | 'day';

interface FilterRow {
    column: string;
    op: string;
    value: string;
}

interface DataExplorerToolbarProps {
    config: DataExplorerConfig;
    schema: FileSchema;
    onChange: (config: DataExplorerConfig) => void;
    onExportCsv: () => void;
    onExportPng: () => void;
    loading?: boolean;
}

const CHART_TYPES: { type: ChartType; icon: typeof BarChart3; label: string }[] = [
    { type: 'bar', icon: BarChart3, label: 'Bar' },
    { type: 'line', icon: LineChart, label: 'Line' },
    { type: 'area', icon: AreaChart, label: 'Area' },
    { type: 'pie', icon: PieChart, label: 'Pie' },
    { type: 'scatter', icon: ScatterIcon, label: 'Scatter' },
];

const AGG_FNS: AggFn[] = ['SUM', 'AVG', 'COUNT', 'MIN', 'MAX'];
const FILTER_OPS = ['=', '!=', '>', '<', '>=', '<=', 'LIKE', 'ILIKE'];

export default function DataExplorerToolbar({ config, schema, onChange, onExportCsv, onExportPng, loading }: DataExplorerToolbarProps) {
    const isMobile = useIsMobileContext();
    const { t } = useTheme();
    const [showFilters, setShowFilters] = useState(
        (config.query.filters?.length ?? 0) > 0
    );

    const numericColumns = schema.columns.filter(c => c.type === 'number').map(c => c.name);
    const dateColumns = new Set(schema.columns.filter(c => c.type === 'date').map(c => c.name));
    const allColumns = schema.columns.map(c => c.name);

    // Is the current groupBy column a date type?
    const groupByIsDate = !!(config.query.groupBy && dateColumns.has(config.query.groupBy));
    const currentGranularity = config.query.groupByTransform?.unit || '';

    // Current aggregation (first one for simplicity)
    const firstAgg = config.query.aggregations?.[0];
    const aggColumn = firstAgg?.column || '';
    const aggFn = firstAgg?.fn || 'SUM';

    function updateChartType(chartType: ChartType) {
        if (chartType === config.chartType) return;

        const updated = { ...config, chartType };
        // Chart type is a local change only (no query change)
        onChange(updated);
    }

    function updateGroupBy(groupBy: string) {
        const updated: DataExplorerConfig = {
            ...config,
            query: { ...config.query, groupBy: groupBy || undefined, groupByTransform: undefined },
            display: {
                ...config.display,
                xAxis: groupBy || undefined,
            },
        };
        onChange(updated);
    }

    function updateTimeGranularity(unit: string) {
        if (!config.query.groupBy) return;

        if (!unit) {
            // Clear transform (raw values)
            const updated: DataExplorerConfig = {
                ...config,
                query: { ...config.query, groupByTransform: undefined },
                display: { ...config.display, xAxis: config.query.groupBy },
            };
            onChange(updated);
            return;
        }

        const alias = `${unit.charAt(0).toUpperCase() + unit.slice(1)}`;
        const updated: DataExplorerConfig = {
            ...config,
            query: {
                ...config.query,
                groupByTransform: { fn: 'DATE_TRUNC', unit: unit as DateTruncUnit, alias },
            },
            display: { ...config.display, xAxis: alias },
        };
        onChange(updated);
    }

    function updateAggregation(column: string, fn: AggFn) {
        const alias = `${fn.toLowerCase()}_${column}`;
        const updated: DataExplorerConfig = {
            ...config,
            query: {
                ...config.query,
                aggregations: [{ column, fn, alias }],
            },
            display: {
                ...config.display,
                yAxis: [alias],
                valueKey: config.chartType === 'pie' ? alias : config.display.valueKey,
            },
        };
        onChange(updated);
    }

    function updateFilters(filters: FilterRow[]) {
        const queryFilters = filters
            .filter(f => f.column && f.value !== '')
            .map(f => ({
                column: f.column,
                op: f.op,
                value: isNaN(Number(f.value)) ? f.value : Number(f.value),
            }));

        const updated: DataExplorerConfig = {
            ...config,
            query: {
                ...config.query,
                filters: queryFilters.length > 0 ? queryFilters as DataExplorerConfig['query']['filters'] : undefined,
            },
        };
        onChange(updated);
    }

    const currentFilters: FilterRow[] = (config.query.filters || []).map(f => ({
        column: f.column,
        op: f.op,
        value: String(f.value),
    }));

    return (
        <div style={{
            display: 'flex',
            flexDirection: 'column',
            gap: '0.5rem',
            padding: '0.75rem',
            background: 'var(--bg-primary)',
            borderRadius: '8px',
            border: '1px solid var(--border-color)',
            fontSize: '0.85rem',
        }}>
            {/* Row 1: Chart type icons + Export */}
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                <div style={{ display: 'flex', gap: '2px', background: 'var(--bg-secondary)', borderRadius: '6px', padding: '2px' }}>
                    {CHART_TYPES.map(({ type, icon: Icon, label }) => (
                        <button
                            key={type}
                            onClick={() => updateChartType(type)}
                            title={label}
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                                width: 32,
                                height: 28,
                                border: 'none',
                                borderRadius: '4px',
                                cursor: 'pointer',
                                background: config.chartType === type ? 'var(--accent-color)' : 'transparent',
                                color: config.chartType === type ? '#fff' : 'var(--text-secondary)',
                                transition: 'all 0.15s',
                            }}
                        >
                            <Icon size={16} />
                        </button>
                    ))}
                </div>

                <div style={{ flex: 1 }} />

                <button
                    onClick={() => setShowFilters(v => !v)}
                    title={t('filters')}
                    aria-label={t('filters')}
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '4px',
                        padding: '4px 8px',
                        border: '1px solid var(--border-color)',
                        borderRadius: '4px',
                        background: showFilters ? 'var(--accent-color)' : 'transparent',
                        color: showFilters ? '#fff' : 'var(--text-secondary)',
                        cursor: 'pointer',
                        fontSize: '0.8rem',
                    }}
                >
                    <Filter size={14} />
                    Filter
                    {currentFilters.length > 0 && (
                        <span style={{
                            background: 'var(--accent-color)',
                            color: '#fff',
                            borderRadius: '50%',
                            width: 16,
                            height: 16,
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            fontSize: '0.75rem',
                        }}>
                            {currentFilters.length}
                        </span>
                    )}
                </button>

                <button onClick={onExportCsv} title={t('exportCsv')} aria-label={t('exportCsv')} style={exportBtnStyle}>
                    <Download size={14} /> CSV
                </button>
                <button onClick={onExportPng} title={t('exportPng')} aria-label={t('exportPng')} style={exportBtnStyle}>
                    <Download size={14} /> PNG
                </button>

                {loading && (
                    <div style={{ color: 'var(--text-secondary)', fontSize: '0.8rem' }}>Loading...</div>
                )}
            </div>

            {/* Row 2: X-Axis / GroupBy + Y-Axis / Aggregation */}
            <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', alignItems: 'center', flexDirection: isMobile ? 'column' : 'row' }}>
                <label style={labelStyle}>
                    {config.chartType === 'pie' ? 'Category' : 'X-Axis'}
                    <select
                        value={config.query.groupBy || ''}
                        onChange={e => updateGroupBy(e.target.value)}
                        style={selectStyle}
                    >
                        <option value="">-- None --</option>
                        {allColumns.map(col => (
                            <option key={col} value={col}>{col}</option>
                        ))}
                    </select>
                </label>

                {groupByIsDate && (
                    <label style={labelStyle}>
                        Granularity
                        <select
                            value={currentGranularity}
                            onChange={e => updateTimeGranularity(e.target.value)}
                            style={selectStyle}
                        >
                            <option value="">Raw</option>
                            <option value="day">Day</option>
                            <option value="week">Week</option>
                            <option value="month">Month</option>
                            <option value="quarter">Quarter</option>
                            <option value="year">Year</option>
                        </select>
                    </label>
                )}

                <label style={labelStyle}>
                    {config.chartType === 'pie' ? 'Value' : 'Y-Axis'}
                    <select
                        value={aggColumn}
                        onChange={e => updateAggregation(e.target.value, aggFn)}
                        style={selectStyle}
                    >
                        <option value="">-- Select --</option>
                        {numericColumns.map(col => (
                            <option key={col} value={col}>{col}</option>
                        ))}
                    </select>
                </label>

                <label style={labelStyle}>
                    Aggregation
                    <select
                        value={aggFn}
                        onChange={e => updateAggregation(aggColumn, e.target.value as AggFn)}
                        style={selectStyle}
                    >
                        {AGG_FNS.map(fn => (
                            <option key={fn} value={fn}>{fn}</option>
                        ))}
                    </select>
                </label>
            </div>

            {/* Row 3: Filters (expandable) */}
            {showFilters && (
                <FilterPanel
                    filters={currentFilters}
                    columns={allColumns}
                    onUpdate={updateFilters}
                />
            )}
        </div>
    );
}

function FilterPanel({ filters, columns, onUpdate }: {
    filters: FilterRow[];
    columns: string[];
    onUpdate: (filters: FilterRow[]) => void;
}) {
    const rows = filters.length > 0 ? filters : [{ column: '', op: '=', value: '' }];

    function updateRow(index: number, field: keyof FilterRow, val: string) {
        const updated = [...rows];
        updated[index] = { ...updated[index], [field]: val };
        onUpdate(updated);
    }

    function addRow() {
        onUpdate([...rows, { column: '', op: '=', value: '' }]);
    }

    function removeRow(index: number) {
        const updated = rows.filter((_, i) => i !== index);
        onUpdate(updated.length > 0 ? updated : []);
    }

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
            {rows.map((row, i) => (
                <div key={i} style={{ display: 'flex', gap: '4px', alignItems: 'center' }}>
                    <select
                        value={row.column}
                        onChange={e => updateRow(i, 'column', e.target.value)}
                        style={{ ...selectStyle, flex: 1 }}
                    >
                        <option value="">Column...</option>
                        {columns.map(c => <option key={c} value={c}>{c}</option>)}
                    </select>
                    <select
                        value={row.op}
                        onChange={e => updateRow(i, 'op', e.target.value)}
                        style={{ ...selectStyle, width: 70 }}
                    >
                        {FILTER_OPS.map(op => <option key={op} value={op}>{op}</option>)}
                    </select>
                    <input
                        type="text"
                        value={row.value}
                        onChange={e => updateRow(i, 'value', e.target.value)}
                        placeholder="Value..."
                        style={{
                            flex: 1,
                            padding: '4px 8px',
                            border: '1px solid var(--border-color)',
                            borderRadius: '4px',
                            background: 'var(--bg-secondary)',
                            color: 'var(--text-primary)',
                            fontSize: '0.8rem',
                        }}
                    />
                    <button
                        onClick={() => removeRow(i)}
                        style={{
                            background: 'none',
                            border: 'none',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            padding: '2px',
                        }}
                    >
                        <X size={14} />
                    </button>
                </div>
            ))}
            <button
                onClick={addRow}
                style={{
                    alignSelf: 'flex-start',
                    padding: '2px 8px',
                    fontSize: '0.8rem',
                    border: '1px dashed var(--border-color)',
                    borderRadius: '4px',
                    background: 'transparent',
                    color: 'var(--text-secondary)',
                    cursor: 'pointer',
                }}
            >
                + Add filter
            </button>
        </div>
    );
}

const labelStyle: React.CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    gap: '4px',
    color: 'var(--text-secondary)',
    fontSize: '0.8rem',
    whiteSpace: 'nowrap',
    width: '100%',
};

const selectStyle: React.CSSProperties = {
    padding: '4px 8px',
    border: '1px solid var(--border-color)',
    borderRadius: '4px',
    background: 'var(--bg-secondary)',
    color: 'var(--text-primary)',
    fontSize: '0.8rem',
    flex: 1,
    minWidth: 0,
};

const exportBtnStyle: React.CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    gap: '4px',
    padding: '4px 8px',
    border: '1px solid var(--border-color)',
    borderRadius: '4px',
    background: 'transparent',
    color: 'var(--text-secondary)',
    cursor: 'pointer',
    fontSize: '0.8rem',
};
