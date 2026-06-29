import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { getApiErrorMessage } from './utils/apiError';
import {
    AreaChart, Area, BarChart, Bar, LineChart, Line,
    XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer
} from 'recharts';
import {
    Activity, Users, FileText, Database, HardDrive, Server,
    RefreshCw, Clock, AlertCircle, CheckCircle, AlertTriangle, Cpu,
} from 'lucide-react';
import { motion } from 'framer-motion';
import { API_BASE_URL } from './api';
import { useTheme } from './contexts/ThemeContext';

interface QueueStats {
    waiting: number;
    active: number;
    failed: number;
}

interface SubsystemHealth {
    status: 'healthy' | 'unhealthy' | 'degraded';
    latencyMs?: number;
    error?: string;
}

interface LiveMetrics {
    activeUsers: number;
    processingFiles: number;
    totalKnowledgeBases: number;
    totalFiles: number;
    totalStorageBytes: number;
    totalUsers: number;
    queueStats: {
        'rag-quick': QueueStats;
        'rag-heavy': QueueStats;
        'rag-batch': QueueStats;
    };
    resourceUsage: {
        nodeHeapUsedMB: number;
        systemMemoryUsedPercent: number;
        cpuLoadAvg1m: number;
        cpuLoadAvg5m: number;
        cpuLoadAvg15m: number;
        cpuCount: number;
    };
    subsystemHealth: {
        mainDb: SubsystemHealth;
        vectorDb: SubsystemHealth;
        redis: SubsystemHealth;
        storage: SubsystemHealth;
    };
    timestamp: string;
}

interface HistoricalData {
    metric: string;
    data: { timestamp: string; value: number }[];
}

const formatBytes = (bytes: number): string => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

const formatTime = (isoString: string): string => {
    return new Date(isoString).toLocaleTimeString('de-DE', {
        hour: '2-digit',
        minute: '2-digit'
    });
};

// Hoisted to module scope: defining a component type inside the dashboard's
// render would create a new type each render and reset its state.
const CustomTooltip = ({ active, payload, label }: { active?: boolean; payload?: { value: number; color: string }[]; label?: string }) => {
    if (active && payload && payload.length) {
        return (
            <div style={{
                background: 'var(--bg-primary)',
                border: '1px solid var(--border-color)',
                borderRadius: '8px',
                padding: '0.75rem',
                boxShadow: '0 4px 12px rgba(0,0,0,0.15)'
            }}>
                <div style={{ fontWeight: 500, marginBottom: '0.25rem' }}>{label}</div>
                {payload.map((entry, index: number) => (
                    <div key={index} style={{ color: entry.color, fontSize: '0.9rem' }}>
                        {entry.value}
                    </div>
                ))}
            </div>
        );
    }
    return null;
};

const StatCard = ({ title, value, subtitle, icon: Icon, color }: {
    title: string;
    value: string | number;
    subtitle?: string;
    icon: React.ElementType;
    color: string;
}) => (
    <motion.div
        initial={{ opacity: 0, y: 10 }}
        animate={{ opacity: 1, y: 0 }}
        style={{
            background: 'var(--bg-secondary)',
            borderRadius: '12px',
            padding: '1.25rem',
            border: '1px solid var(--border-color)'
        }}
    >
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between' }}>
            <div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                    {title}
                </div>
                <div style={{ fontSize: '1.75rem', fontWeight: 600, color: 'var(--text-primary)' }}>
                    {value}
                </div>
                {subtitle && (
                    <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                        {subtitle}
                    </div>
                )}
            </div>
            <div style={{
                padding: '0.75rem',
                background: `${color}15`,
                borderRadius: '10px',
                color: color
            }}>
                <Icon size={22} />
            </div>
        </div>
    </motion.div>
);

const QueueCard = ({ name, stats }: { name: string; stats: QueueStats }) => {
    return (
        <div style={{
            background: 'var(--bg-secondary)',
            borderRadius: '12px',
            padding: '1rem',
            border: '1px solid var(--border-color)'
        }}>
            <div style={{ fontWeight: 500, marginBottom: '0.75rem', fontSize: '0.9rem' }}>{name}</div>
            <div style={{ display: 'flex', gap: '1rem', fontSize: '0.85rem' }}>
                <div style={{ textAlign: 'center' }}>
                    <div style={{ color: 'var(--warning-text)', fontWeight: 600 }}>{stats.waiting}</div>
                    <div style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>Wartend</div>
                </div>
                <div style={{ textAlign: 'center' }}>
                    <div style={{ color: 'var(--accent-primary)', fontWeight: 600 }}>{stats.active}</div>
                    <div style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>Aktiv</div>
                </div>
                <div style={{ textAlign: 'center' }}>
                    <div style={{ color: 'var(--error-text)', fontWeight: 600 }}>{stats.failed}</div>
                    <div style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>Fehler</div>
                </div>
            </div>
        </div>
    );
};

const HealthIndicator = ({ name, health }: { name: string; health: SubsystemHealth }) => {
    const getStatusIcon = () => {
        switch (health.status) {
            case 'healthy': return <CheckCircle size={18} style={{ color: 'var(--success-text)' }} />;
            case 'degraded': return <AlertTriangle size={18} style={{ color: 'var(--warning-text)' }} />;
            case 'unhealthy': return <AlertCircle size={18} style={{ color: 'var(--error-text)' }} />;
        }
    };

    const getStatusColor = () => {
        switch (health.status) {
            case 'healthy': return 'var(--success-text)';
            case 'degraded': return 'var(--warning-text)';
            case 'unhealthy': return 'var(--error-text)';
        }
    };

    return (
        <div style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: '0.75rem 1rem',
            background: 'var(--bg-secondary)',
            borderRadius: '8px',
            border: '1px solid var(--border-color)',
            borderLeft: `3px solid ${getStatusColor()}`
        }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                {getStatusIcon()}
                <span style={{ fontWeight: 500 }}>{name}</span>
            </div>
            <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                {health.latencyMs !== undefined && `${health.latencyMs}ms`}
                {health.error && <span style={{ color: 'var(--error-text)' }}> - {health.error}</span>}
            </div>
        </div>
    );
};

const GaugeBar = ({ value, max, label, color }: {
    value: number;
    max: number;
    label: string;
    color: string;
}) => {
    const percentage = Math.min((value / max) * 100, 100);
    return (
        <div style={{ marginBottom: '1rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.5rem' }}>
                <span style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{label}</span>
                <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>{value.toFixed(1)}{max === 100 ? '%' : ''}</span>
            </div>
            <div style={{
                height: '8px',
                background: 'var(--bg-primary)',
                borderRadius: '4px',
                overflow: 'hidden'
            }}>
                <div style={{
                    height: '100%',
                    width: `${percentage}%`,
                    background: color,
                    borderRadius: '4px',
                    transition: 'width 0.3s ease'
                }} />
            </div>
        </div>
    );
};

const ChartCard = ({ title, children }: { title: string; children: React.ReactNode }) => (
    <div style={{
        background: 'var(--bg-secondary)',
        borderRadius: '12px',
        padding: '1.25rem',
        border: '1px solid var(--border-color)'
    }}>
        <h3 style={{ margin: '0 0 1rem 0', fontSize: '1rem', fontWeight: 500 }}>{title}</h3>
        {children}
    </div>
);

export default function SystemHealthDashboard() {
    const { t } = useTheme();
    const [metrics, setMetrics] = useState<LiveMetrics | null>(null);
    const [historicalData, setHistoricalData] = useState<{
        activeUsers: HistoricalData | null;
        storage: HistoricalData | null;
        totalFiles: HistoricalData | null;
        queueFailed: HistoricalData | null;
    }>({
        activeUsers: null,
        storage: null,
        totalFiles: null,
        queueFailed: null
    });
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [autoRefresh, setAutoRefresh] = useState(true);
    const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
    const [dateRange, setDateRange] = useState<'1h' | '24h' | '7d' | '30d'>('24h');
    const [aiHealth, setAiHealth] = useState<{
        status: 'idle' | 'testing' | 'healthy' | 'unhealthy';
        latencyMs?: number;
        error?: string;
        providerName?: string;
    }>({ status: 'idle' });

    const checkAiHealth = useCallback(async () => {
        setAiHealth({ status: 'testing' });
        try {
            const res = await axios.post(`${API_BASE_URL}/api/system-health/ai-check`);
            const data = res.data;
            setAiHealth({
                status: data.status === 'healthy' ? 'healthy' : 'unhealthy',
                latencyMs: data.latencyMs,
                error: data.error,
                providerName: data.providerName,
            });
        } catch {
            setAiHealth({ status: 'unhealthy', error: 'Prüfung fehlgeschlagen' });
        }
    }, []);

    const getDateRange = useCallback(() => {
        const to = new Date();
        let from: Date;
        switch (dateRange) {
            case '1h': from = new Date(to.getTime() - 60 * 60 * 1000); break;
            case '24h': from = new Date(to.getTime() - 24 * 60 * 60 * 1000); break;
            case '7d': from = new Date(to.getTime() - 7 * 24 * 60 * 60 * 1000); break;
            case '30d': from = new Date(to.getTime() - 30 * 24 * 60 * 60 * 1000); break;
        }
        return { from: from.toISOString(), to: to.toISOString() };
    }, [dateRange]);

    // Pure loaders: fetch and RETURN data, no state writes. Effects apply the
    // results inside async callbacks (react.dev fetch-then-set pattern), so no
    // setState happens synchronously within an effect body.
    const loadMetrics = useCallback(async (): Promise<LiveMetrics> => {
        const response = await axios.get(`${API_BASE_URL}/api/system-health/live`);
        return response.data;
    }, []);

    const loadHistoricalData = useCallback(async () => {
        const { from, to } = getDateRange();
        const metricsToFetch = ['active_users', 'storage_bytes', 'total_files', 'queue_failed'];

        const results = await Promise.all(
            metricsToFetch.map(metric =>
                axios.get(`${API_BASE_URL}/api/system-health/history`, {
                    params: { metric, from, to }
                }).then(res => res.data as HistoricalData)
            )
        );

        return {
            activeUsers: results[0],
            storage: results[1],
            totalFiles: results[2],
            queueFailed: results[3]
        };
    }, [getDateRange]);

    // State-applying wrappers for event handlers and interval callbacks.
    const fetchMetrics = useCallback(async () => {
        try {
            const data = await loadMetrics();
            setMetrics(data);
            setLastUpdated(new Date());
            setError(null);
        } catch (err: unknown) {
            console.error('Failed to fetch metrics:', err);
            setError(getApiErrorMessage(err, 'Fehler beim Laden der Metriken'));
        }
    }, [loadMetrics]);

    const fetchHistoricalData = useCallback(async () => {
        try {
            setHistoricalData(await loadHistoricalData());
        } catch (err: unknown) {
            console.error('Failed to fetch historical data:', err);
            setError(getApiErrorMessage(err, t('historicalDataError')));
        }
    }, [loadHistoricalData, t]);

    const fetchAll = useCallback(async () => {
        setLoading(true);
        await Promise.all([fetchMetrics(), fetchHistoricalData()]);
        setLoading(false);
    }, [fetchMetrics, fetchHistoricalData]);

    // Initial load + refetch when the date range changes (loadHistoricalData's
    // identity tracks dateRange via getDateRange). All state writes happen in
    // async callbacks, never synchronously in the effect body.
    useEffect(() => {
        let cancelled = false;

        const metricsDone = loadMetrics()
            .then(data => {
                if (cancelled) return;
                setMetrics(data);
                setLastUpdated(new Date());
                setError(null);
            })
            .catch((err: unknown) => {
                if (cancelled) return;
                console.error('Failed to fetch metrics:', err);
                setError(getApiErrorMessage(err, 'Fehler beim Laden der Metriken'));
            });

        const historicalDone = loadHistoricalData()
            .then(data => {
                if (!cancelled) setHistoricalData(data);
            })
            .catch((err: unknown) => {
                if (cancelled) return;
                console.error('Failed to fetch historical data:', err);
                setError(getApiErrorMessage(err, t('historicalDataError')));
            });

        Promise.allSettled([metricsDone, historicalDone]).then(() => {
            if (!cancelled) setLoading(false);
        });

        return () => { cancelled = true; };
    }, [loadMetrics, loadHistoricalData, t]);

    useEffect(() => {
        if (!autoRefresh) return;
        const interval = setInterval(fetchMetrics, 10000); // 10 seconds
        return () => clearInterval(interval);
    }, [autoRefresh, fetchMetrics]);

    if (loading && !metrics) {
        return (
            <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                height: '400px',
                color: 'var(--text-secondary)'
            }}>
                <RefreshCw size={24} className="spin" style={{ marginRight: '0.5rem' }} />
                Lade System-Health-Dashboard...
            </div>
        );
    }

    if (error && !metrics) {
        return (
            <div style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                justifyContent: 'center',
                height: '400px',
                color: 'var(--text-secondary)'
            }}>
                <AlertCircle size={48} style={{ color: 'var(--error-text)', marginBottom: '1rem' }} />
                <div style={{ marginBottom: '1rem' }}>{error}</div>
                <button onClick={fetchAll} className="secondary-button">
                    Erneut versuchen
                </button>
            </div>
        );
    }

    if (!metrics) return null;

    const formatChartData = (data: HistoricalData | null) => {
        if (!data) return [];
        return data.data.map(d => ({
            time: formatTime(d.timestamp),
            value: d.value
        }));
    };

    const formatChartDataDelta = (data: HistoricalData | null) => {
        if (!data || data.data.length < 2) return [];

        const deltaData = [];
        for (let i = 1; i < data.data.length; i++) {
            const current = data.data[i];
            const previous = data.data[i - 1];
            // Calculate difference, ensure non-negative (handle restarts/resets gracefully)
            const diff = Math.max(0, current.value - previous.value);

            deltaData.push({
                time: formatTime(current.timestamp),
                value: diff
            });
        }
        return deltaData;
    };

    return (
        <div style={{ padding: '1.5rem', maxHeight: 'calc(100vh - 200px)', overflowY: 'auto' }}>
            {/* Header */}
            <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                marginBottom: '1.5rem'
            }}>
                <div>
                    <h2 style={{ margin: 0, fontSize: '1.5rem', fontWeight: 600, display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <Activity size={24} />
                        System Health
                    </h2>
                    {lastUpdated && (
                        <div style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginTop: '0.25rem', display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
                            <Clock size={14} />
                            Zuletzt aktualisiert: {lastUpdated.toLocaleTimeString('de-DE')}
                        </div>
                    )}
                </div>

                <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
                    <label style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.5rem',
                        fontSize: '0.9rem',
                        color: 'var(--text-secondary)',
                        cursor: 'pointer'
                    }}>
                        <input
                            type="checkbox"
                            checked={autoRefresh}
                            onChange={(e) => setAutoRefresh(e.target.checked)}
                            style={{ cursor: 'pointer' }}
                        />
                        Auto-Refresh (10s)
                    </label>
                    <button
                        onClick={fetchAll}
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '0.5rem',
                            padding: '0.5rem 1rem',
                            background: 'var(--bg-secondary)',
                            border: '1px solid var(--border-color)',
                            borderRadius: '8px',
                            color: 'var(--text-primary)',
                            cursor: 'pointer'
                        }}
                        title="Aktualisieren"
                    >
                        <RefreshCw size={16} className={loading ? 'spin' : ''} />
                    </button>
                </div>
            </div>

            {/* Stat Cards */}
            <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
                gap: '1rem',
                marginBottom: '1.5rem'
            }}>
                <StatCard
                    title="Aktive Benutzer"
                    value={metrics.activeUsers}
                    subtitle="Letzte 15 Min"
                    icon={Users}
                    color="var(--accent-primary)"
                />
                <StatCard
                    title="Dateien in Verarbeitung"
                    value={metrics.processingFiles}
                    icon={FileText}
                    color="var(--warning-text)"
                />
                <StatCard
                    title="Knowledge Bases"
                    value={metrics.totalKnowledgeBases}
                    icon={Database}
                    color="var(--success-text)"
                />
                <StatCard
                    title="Dateien gesamt"
                    value={metrics.totalFiles}
                    icon={FileText}
                    color="#6a1b9a"
                />
                <StatCard
                    title="Speicher"
                    value={formatBytes(metrics.totalStorageBytes)}
                    icon={HardDrive}
                    color="#d84315"
                />
                <StatCard
                    title="Benutzer gesamt"
                    value={metrics.totalUsers}
                    icon={Users}
                    color="#00838f"
                />
            </div>

            {/* Queue Stats */}
            <div style={{ marginBottom: '1.5rem' }}>
                <h3 style={{ margin: '0 0 1rem 0', fontSize: '1rem', fontWeight: 500, color: 'var(--text-secondary)' }}>
                    Job-Warteschlangen
                </h3>
                <div style={{
                    display: 'grid',
                    gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                    gap: '1rem'
                }}>
                    <QueueCard name="rag-quick" stats={metrics.queueStats['rag-quick']} />
                    <QueueCard name="rag-heavy" stats={metrics.queueStats['rag-heavy']} />
                    <QueueCard name="rag-batch" stats={metrics.queueStats['rag-batch']} />
                </div>
            </div>

            {/* Subsystem Health + Resources */}
            <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 500px), 1fr))',
                gap: '1.5rem',
                marginBottom: '1.5rem'
            }}>
                {/* Subsystem Health */}
                <div>
                    <h3 style={{ margin: '0 0 1rem 0', fontSize: '1rem', fontWeight: 500, color: 'var(--text-secondary)' }}>
                        Subsystem-Status
                    </h3>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        <HealthIndicator name="Haupt-Datenbank" health={metrics.subsystemHealth.mainDb} />
                        <HealthIndicator name="Vektor-Datenbank" health={metrics.subsystemHealth.vectorDb} />
                        <HealthIndicator name="Redis" health={metrics.subsystemHealth.redis} />
                        <HealthIndicator name="Speicher" health={metrics.subsystemHealth.storage} />

                        {/* AI Provider Health - manual check */}
                        <div style={{
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'space-between',
                            padding: '0.75rem 1rem',
                            background: 'var(--bg-secondary)',
                            borderRadius: '8px',
                            border: '1px solid var(--border-color)',
                            borderLeft: `3px solid ${aiHealth.status === 'healthy' ? 'var(--success-text)' : aiHealth.status === 'unhealthy' ? 'var(--error-text)' : 'var(--border-color)'}`
                        }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                                {aiHealth.status === 'testing' && <RefreshCw size={18} className="spin" style={{ color: 'var(--text-secondary)' }} />}
                                {aiHealth.status === 'healthy' && <CheckCircle size={18} style={{ color: 'var(--success-text)' }} />}
                                {aiHealth.status === 'unhealthy' && <AlertCircle size={18} style={{ color: 'var(--error-text)' }} />}
                                {aiHealth.status === 'idle' && <Cpu size={18} style={{ color: 'var(--text-secondary)' }} />}
                                <span style={{ fontWeight: 500 }}>
                                    {aiHealth.providerName ? `KI-Anbieter (${aiHealth.providerName})` : 'KI-Anbieter'}
                                </span>
                            </div>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', fontSize: '0.85rem' }}>
                                {aiHealth.status === 'idle' && (
                                    <span style={{ color: 'var(--text-secondary)' }}>Nicht getestet</span>
                                )}
                                {aiHealth.status === 'testing' && (
                                    <span style={{ color: 'var(--text-secondary)' }}>Teste...</span>
                                )}
                                {aiHealth.status === 'healthy' && aiHealth.latencyMs !== undefined && (
                                    <span style={{ color: 'var(--text-secondary)' }}>{aiHealth.latencyMs}ms</span>
                                )}
                                {aiHealth.status === 'unhealthy' && aiHealth.error && (
                                    <span style={{ color: 'var(--error-text)' }}>{aiHealth.error}</span>
                                )}
                                <button
                                    onClick={checkAiHealth}
                                    disabled={aiHealth.status === 'testing'}
                                    style={{
                                        padding: '0.3rem 0.7rem',
                                        background: 'var(--bg-primary)',
                                        border: '1px solid var(--border-color)',
                                        borderRadius: '6px',
                                        color: 'var(--text-primary)',
                                        cursor: aiHealth.status === 'testing' ? 'not-allowed' : 'pointer',
                                        fontSize: '0.8rem',
                                        opacity: aiHealth.status === 'testing' ? 0.6 : 1,
                                    }}
                                >
                                    Prüfen
                                </button>
                            </div>
                        </div>
                    </div>
                </div>

                {/* Resource Usage */}
                <div style={{
                    background: 'var(--bg-secondary)',
                    borderRadius: '12px',
                    padding: '1.25rem',
                    border: '1px solid var(--border-color)'
                }}>
                    <h3 style={{ margin: '0 0 1rem 0', fontSize: '1rem', fontWeight: 500, display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <Server size={18} />
                        Ressourcenverbrauch
                    </h3>
                    <GaugeBar
                        value={metrics.resourceUsage.cpuLoadAvg1m}
                        max={metrics.resourceUsage.cpuCount || 4}
                        label="CPU Load (1m)"
                        color="var(--accent-primary)"
                    />
                    <GaugeBar
                        value={metrics.resourceUsage.systemMemoryUsedPercent}
                        max={100}
                        label="Systemspeicher"
                        color="var(--success-text)"
                    />
                    <GaugeBar
                        value={metrics.resourceUsage.nodeHeapUsedMB}
                        max={1024}
                        label={`Node Heap (${metrics.resourceUsage.nodeHeapUsedMB} MB)`}
                        color="#6a1b9a"
                    />
                </div>
            </div>

            {/* Historical Charts */}
            <div style={{ marginBottom: '1rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
                    <h3 style={{ margin: 0, fontSize: '1rem', fontWeight: 500, color: 'var(--text-secondary)' }}>
                        Historische Daten
                    </h3>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                        {(['1h', '24h', '7d', '30d'] as const).map(range => (
                            <button
                                key={range}
                                onClick={() => {
                                    if (range === dateRange) return;
                                    // Show the refresh spinner while the
                                    // range-change effect refetches.
                                    setLoading(true);
                                    setDateRange(range);
                                }}
                                style={{
                                    padding: '0.4rem 0.8rem',
                                    background: dateRange === range ? 'var(--accent-primary)' : 'var(--bg-secondary)',
                                    color: dateRange === range ? 'white' : 'var(--text-primary)',
                                    border: '1px solid var(--border-color)',
                                    borderRadius: '6px',
                                    cursor: 'pointer',
                                    fontSize: '0.85rem'
                                }}
                            >
                                {range}
                            </button>
                        ))}
                    </div>
                </div>

                <div style={{
                    display: 'grid',
                    gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 500px), 1fr))',
                    gap: '1rem'
                }}>
                    <ChartCard title="Aktive Benutzer">
                        <div style={{ height: '200px' }}>
                            {formatChartData(historicalData.activeUsers).length > 0 ? (
                                <ResponsiveContainer width="100%" height="100%">
                                    <AreaChart data={formatChartData(historicalData.activeUsers)}>
                                        <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                        <XAxis dataKey="time" tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <Tooltip content={<CustomTooltip />} />
                                        <Area type="monotone" dataKey="value" stroke="#165a97" fill="#165a9740" />
                                    </AreaChart>
                                </ResponsiveContainer>
                            ) : (
                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                    Keine Daten vorhanden
                                </div>
                            )}
                        </div>
                    </ChartCard>

                    <ChartCard title="Speicherverbrauch">
                        <div style={{ height: '200px' }}>
                            {formatChartData(historicalData.storage).length > 0 ? (
                                <ResponsiveContainer width="100%" height="100%">
                                    <AreaChart data={formatChartData(historicalData.storage).map(d => ({
                                        ...d,
                                        value: d.value / (1024 * 1024 * 1024) // Convert to GB
                                    }))}>
                                        <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                        <XAxis dataKey="time" tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} tickFormatter={(v) => `${v.toFixed(1)} GB`} />
                                        <Tooltip content={<CustomTooltip />} />
                                        <Area type="monotone" dataKey="value" stroke="#2d8f4e" fill="#2d8f4e40" />
                                    </AreaChart>
                                </ResponsiveContainer>
                            ) : (
                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                    Keine Daten vorhanden
                                </div>
                            )}
                        </div>
                    </ChartCard>

                    <ChartCard title="Dateien verarbeitet (pro Intervall)">
                        <div style={{ height: '200px' }}>
                            {formatChartDataDelta(historicalData.totalFiles).length > 0 ? (
                                <ResponsiveContainer width="100%" height="100%">
                                    <BarChart data={formatChartDataDelta(historicalData.totalFiles)}>
                                        <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                        <XAxis dataKey="time" tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <Tooltip content={<CustomTooltip />} />
                                        <Bar dataKey="value" fill="#6a1b9a" radius={[4, 4, 0, 0]} />
                                    </BarChart>
                                </ResponsiveContainer>
                            ) : (
                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                    Keine Daten vorhanden
                                </div>
                            )}
                        </div>
                    </ChartCard>

                    <ChartCard title="Fehler in Warteschlangen">
                        <div style={{ height: '200px' }}>
                            {formatChartData(historicalData.queueFailed).length > 0 ? (
                                <ResponsiveContainer width="100%" height="100%">
                                    <LineChart data={formatChartData(historicalData.queueFailed)}>
                                        <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                        <XAxis dataKey="time" tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 11 }} />
                                        <Tooltip content={<CustomTooltip />} />
                                        <Line type="monotone" dataKey="value" stroke="#c0392b" strokeWidth={2} dot={false} />
                                    </LineChart>
                                </ResponsiveContainer>
                            ) : (
                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                    Keine Daten vorhanden
                                </div>
                            )}
                        </div>
                    </ChartCard>
                </div>
            </div>

            <style>{`
                .spin {
                    animation: spin 1s linear infinite;
                }
                @keyframes spin {
                    from { transform: rotate(0deg); }
                    to { transform: rotate(360deg); }
                }
            `}</style>
        </div>
    );
}
