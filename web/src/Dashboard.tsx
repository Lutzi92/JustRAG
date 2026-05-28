import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { getApiErrorMessage } from './utils/apiError';
import {
    BarChart, Bar, PieChart, Pie, Cell, AreaChart, Area,
    XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer
} from 'recharts';
import {
    Calendar, FileText, MessageSquare, Sparkles, TrendingUp,
    RefreshCw, Download, ChevronDown, ThumbsUp
} from 'lucide-react';
import { API_BASE_URL } from './api';

const COLORS = ['#165a97', '#2d8f4e', '#cc8400', '#c0392b', '#6a1b9a', '#d84315', '#00838f', '#5c6bc0'];

interface DashboardProps {
    kbId: string;
    kbName: string;
}

interface FileStats {
    byType: { type: string; count: number; totalSize: number }[];
    byStatus: { status: string; count: number }[];
    byOrigin: { origin: string; count: number }[];
    totalFiles: number;
    totalSize: number;
}

interface ActivityStats {
    filesOverTime: { date: string; count: number }[];
    chatsOverTime: { date: string; count: number }[];
    messagesOverTime: { date: string; count: number }[];
}

interface ChatStats {
    totalChats: number;
    totalMessages: number;
    messagesByRole: { role: string; count: number }[];
    avgMessagesPerChat: number;
}

interface GeneratedContentStats {
    byType: { type: string; count: number }[];
    totalGenerated: number;
    overTime: { date: string; count: number }[];
}

interface RetrievalQualityStats {
    feedbackStats: { feedback: string; count: number }[];
    scoreOverTime: { day: string; avgScore: number; messageCount: number }[];
    lowScoreQueries: { id: string; content: string; createdAt: string; avgScore: number }[];
}

interface DashboardData {
    files: FileStats;
    activity: ActivityStats;
    chats: ChatStats;
    generatedContent: GeneratedContentStats;
}

const formatBytes = (bytes: number): string => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

const formatDate = (dateStr: string): string => {
    const date = new Date(dateStr);
    return date.toLocaleDateString('de-DE', { day: '2-digit', month: '2-digit' });
};

const StatCard = ({ title, value, subtitle, icon: Icon, color }: {
    title: string;
    value: string | number;
    subtitle?: string;
    icon: React.ElementType;
    color: string;
}) => (
    <article style={{
        background: 'var(--bg-secondary)',
        borderRadius: '12px',
        padding: '1.25rem',
        border: '1px solid var(--border-color)'
    }}>
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
    </article>
);

const ChartCard = ({ title, children, onExport }: {
    title: string;
    children: React.ReactNode;
    onExport?: () => void;
}) => (
    <section
        style={{
            background: 'var(--bg-secondary)',
            borderRadius: '12px',
            padding: '1.25rem',
            border: '1px solid var(--border-color)'
        }}
    >
        <div style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: '1rem'
        }}>
            <h3 style={{ margin: 0, fontSize: '1rem', fontWeight: 500 }}>{title}</h3>
            {onExport && (
                <button
                    onClick={onExport}
                    style={{
                        background: 'none',
                        border: 'none',
                        color: 'var(--text-secondary)',
                        cursor: 'pointer',
                        padding: '4px'
                    }}
                    title="Export"
                >
                    <Download size={16} />
                </button>
            )}
        </div>
        <div role="img" aria-label={title}>
            {children}
        </div>
    </section>
);

interface TooltipEntry {
    name: string;
    value: number;
    color: string;
}

const CustomTooltip = ({ active, payload, label }: { active?: boolean; payload?: TooltipEntry[]; label?: string }) => {
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
                {payload.map((entry: TooltipEntry, index: number) => (
                    <div key={index} style={{ color: entry.color, fontSize: '0.9rem' }}>
                        {entry.name}: {entry.value}
                    </div>
                ))}
            </div>
        );
    }
    return null;
};

export default function Dashboard({ kbId, kbName }: DashboardProps) {
    const [data, setData] = useState<DashboardData | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const [qualityData, setQualityData] = useState<RetrievalQualityStats | null>(null);

    // Filter state
    const [dateRange, setDateRange] = useState<'7d' | '30d' | '90d' | 'all'>('30d');
    const [showFilters, setShowFilters] = useState(false);

    const fetchData = useCallback(async () => {
        setLoading(true);
        setError(null);
        try {
            const params = new URLSearchParams();
            const now = new Date();
            let from;
            switch (dateRange) {
                case '7d': from = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString(); break;
                case '30d': from = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString(); break;
                case '90d': from = new Date(now.getTime() - 90 * 24 * 60 * 60 * 1000).toISOString(); break;
            }
            if (from) params.set('from', from);

            const response = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/analytics?${params}`);
            setData(response.data);

            // Also fetch retrieval quality data
            try {
                const qualityResponse = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/analytics/retrieval-quality?${params}`);
                setQualityData(qualityResponse.data);
            } catch {
                // Retrieval quality is optional — don't fail the whole dashboard
                setQualityData(null);
            }
        } catch (err: unknown) {
            console.error('Failed to fetch dashboard data:', err);
            setError(getApiErrorMessage(err, 'Failed to load dashboard data'));
        } finally {
            setLoading(false);
        }
    }, [kbId, dateRange]);

    useEffect(() => {
        fetchData();
    }, [fetchData]);

    if (loading) {
        return (
            <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                height: '400px',
                color: 'var(--text-secondary)'
            }}>
                <RefreshCw size={24} className="spin" style={{ marginRight: '0.5rem' }} />
                Lade Dashboard...
            </div>
        );
    }

    if (error) {
        return (
            <div style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                justifyContent: 'center',
                height: '400px',
                color: 'var(--text-secondary)'
            }}>
                <div style={{ marginBottom: '1rem' }}>{error}</div>
                <button onClick={fetchData} className="secondary-button">
                    Erneut versuchen
                </button>
            </div>
        );
    }

    if (!data) return null;

    // Fill in missing dates for activity timeline
    const allDates = new Set([
        ...data.activity.filesOverTime.map(f => f.date),
        ...data.activity.chatsOverTime.map(c => c.date),
        ...data.activity.messagesOverTime.map(m => m.date)
    ]);
    const sortedDates = Array.from(allDates).sort();
    const fullActivityData = sortedDates.map(date => {
        const file = data.activity.filesOverTime.find(f => f.date === date);
        const chat = data.activity.chatsOverTime.find(c => c.date === date);
        const msg = data.activity.messagesOverTime.find(m => m.date === date);
        return {
            date: formatDate(date),
            Dateien: file?.count || 0,
            Chats: chat?.count || 0,
            Nachrichten: msg?.count || 0
        };
    });

    return (
        <main style={{ padding: '1.5rem' }}>
            {/* Header */}
            <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                marginBottom: '1.5rem'
            }}>
                <div>
                    <h1 style={{ margin: 0, fontSize: '1.5rem', fontWeight: 600 }}>
                        Dashboard
                    </h1>
                    <div style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', marginTop: '0.25rem' }}>
                        {kbName}
                    </div>
                </div>

                <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
                    {/* Date Range Selector */}
                    <div style={{ position: 'relative' }}>
                        <button
                            onClick={() => setShowFilters(!showFilters)}
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '0.5rem',
                                padding: '0.5rem 1rem',
                                background: 'var(--bg-secondary)',
                                border: '1px solid var(--border-color)',
                                borderRadius: '8px',
                                color: 'var(--text-primary)',
                                cursor: 'pointer',
                                fontSize: '0.9rem'
                            }}
                        >
                            <Calendar size={16} />
                            {dateRange === '7d' && 'Letzte 7 Tage'}
                            {dateRange === '30d' && 'Letzte 30 Tage'}
                            {dateRange === '90d' && 'Letzte 90 Tage'}
                            {dateRange === 'all' && 'Alle Zeit'}
                            <ChevronDown size={16} />
                        </button>

                        {showFilters && (
                            <div style={{
                                position: 'absolute',
                                top: '100%',
                                right: 0,
                                marginTop: '0.5rem',
                                background: 'var(--bg-primary)',
                                border: '1px solid var(--border-color)',
                                borderRadius: '8px',
                                boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
                                zIndex: 100,
                                minWidth: '150px'
                            }}>
                                {(['7d', '30d', '90d', 'all'] as const).map(range => (
                                    <button
                                        key={range}
                                        onClick={() => { setDateRange(range); setShowFilters(false); }}
                                        style={{
                                            display: 'block',
                                            width: '100%',
                                            padding: '0.75rem 1rem',
                                            background: dateRange === range ? 'var(--tag-bg)' : 'transparent',
                                            border: 'none',
                                            textAlign: 'left',
                                            cursor: 'pointer',
                                            color: 'var(--text-primary)',
                                            fontSize: '0.9rem'
                                        }}
                                    >
                                        {range === '7d' && 'Letzte 7 Tage'}
                                        {range === '30d' && 'Letzte 30 Tage'}
                                        {range === '90d' && 'Letzte 90 Tage'}
                                        {range === 'all' && 'Alle Zeit'}
                                    </button>
                                ))}
                            </div>
                        )}
                    </div>

                    <button
                        onClick={fetchData}
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
                        <RefreshCw size={16} />
                    </button>
                </div>
            </div>

            {/* Stats Cards */}
            <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                gap: '1rem',
                marginBottom: '1.5rem'
            }}>
                <StatCard
                    title="Dateien"
                    value={data.files.totalFiles}
                    subtitle={formatBytes(data.files.totalSize)}
                    icon={FileText}
                    color="#165a97"
                />
                <StatCard
                    title="Chats"
                    value={data.chats.totalChats}
                    subtitle={`${data.chats.avgMessagesPerChat} Nachr./Chat`}
                    icon={MessageSquare}
                    color="#2d8f4e"
                />
                <StatCard
                    title="Nachrichten"
                    value={data.chats.totalMessages}
                    icon={TrendingUp}
                    color="#cc8400"
                />
                <StatCard
                    title="Generiert"
                    value={data.generatedContent.totalGenerated}
                    icon={Sparkles}
                    color="#6a1b9a"
                />
                {qualityData && (
                    <StatCard
                        title="Feedback"
                        value={qualityData.feedbackStats.reduce((sum, f) => sum + (f.feedback !== 'none' ? f.count : 0), 0)}
                        subtitle={`${qualityData.feedbackStats.find(f => f.feedback === 'positive')?.count || 0} positiv`}
                        icon={ThumbsUp}
                        color="#2d8f4e"
                    />
                )}
            </div>

            {/* Charts Grid */}
            <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 500px), 1fr))',
                gap: '1rem'
            }}>
                {/* Activity Over Time */}
                <ChartCard title="Aktivität im Zeitverlauf">
                    <div style={{ height: '280px' }}>
                        {fullActivityData.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <AreaChart data={fullActivityData}>
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis
                                        dataKey="date"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                        tickLine={{ stroke: 'var(--border-color)' }}
                                    />
                                    <YAxis
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                        tickLine={{ stroke: 'var(--border-color)' }}
                                    />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Legend />
                                    <Area type="monotone" dataKey="Nachrichten" stackId="1" stroke="#cc8400" fill="#cc840040" />
                                    <Area type="monotone" dataKey="Chats" stackId="1" stroke="#2d8f4e" fill="#2d8f4e40" />
                                    <Area type="monotone" dataKey="Dateien" stackId="1" stroke="#165a97" fill="#165a9740" />
                                </AreaChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine Aktivitätsdaten vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Files by Type */}
                <ChartCard title="Dateien nach Typ">
                    <div style={{ height: '280px' }}>
                        {data.files.byType.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <PieChart>
                                    <Pie
                                        data={data.files.byType}
                                        cx="50%"
                                        cy="50%"
                                        innerRadius={60}
                                        outerRadius={100}
                                        paddingAngle={2}
                                        dataKey="count"
                                        nameKey="type"
                                        label={({ name, percent }: { name?: string; percent?: number }) => `${(name ?? '').split('/').pop()} (${((percent ?? 0) * 100).toFixed(0)}%)`}
                                        labelLine={false}
                                    >
                                        {data.files.byType.map((_, index) => (
                                            <Cell key={`cell-${index}`} fill={COLORS[index % COLORS.length]} />
                                        ))}
                                    </Pie>
                                    <Tooltip content={<CustomTooltip />} />
                                </PieChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine Dateien vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Files by Status */}
                <ChartCard title="Dateien nach Status">
                    <div style={{ height: '280px' }}>
                        {data.files.byStatus.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <BarChart data={data.files.byStatus} layout="vertical">
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis type="number" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                                    <YAxis
                                        type="category"
                                        dataKey="status"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                        width={100}
                                    />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Bar dataKey="count" fill="#165a97" radius={[0, 4, 4, 0]} />
                                </BarChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine Statusdaten vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Messages by Role */}
                <ChartCard title="Nachrichten nach Rolle">
                    <div style={{ height: '280px' }}>
                        {data.chats.messagesByRole.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <PieChart>
                                    <Pie
                                        data={data.chats.messagesByRole.map(r => ({
                                            ...r,
                                            role: r.role === 'user' ? 'Benutzer' : 'KI'
                                        }))}
                                        cx="50%"
                                        cy="50%"
                                        innerRadius={60}
                                        outerRadius={100}
                                        paddingAngle={2}
                                        dataKey="count"
                                        nameKey="role"
                                        label={({ role, percent }) => `${role} (${((percent ?? 0) * 100).toFixed(0)}%)`}
                                        labelLine={false}
                                    >
                                        <Cell fill="#165a97" />
                                        <Cell fill="#2d8f4e" />
                                    </Pie>
                                    <Tooltip content={<CustomTooltip />} />
                                </PieChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine Nachrichten vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Generated Content by Type */}
                <ChartCard title="Generierte Inhalte nach Typ">
                    <div style={{ height: '280px' }}>
                        {data.generatedContent.byType.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <BarChart data={data.generatedContent.byType}>
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis
                                        dataKey="type"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                    />
                                    <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Bar dataKey="count" fill="#6a1b9a" radius={[4, 4, 0, 0]} />
                                </BarChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine generierten Inhalte vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Files by Origin */}
                <ChartCard title="Dateien nach Herkunft">
                    <div style={{ height: '280px' }}>
                        {data.files.byOrigin.length > 0 ? (
                            <ResponsiveContainer width="100%" height="100%">
                                <BarChart data={data.files.byOrigin.map(o => ({
                                    ...o,
                                    origin: o.origin === 'upload' ? 'Upload' :
                                        o.origin === 'url' ? 'URL' :
                                            o.origin === 'crawl' ? 'Crawler' :
                                                o.origin === 'research' ? 'Research' : o.origin
                                }))}>
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis
                                        dataKey="origin"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                    />
                                    <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Bar dataKey="count" fill="#d84315" radius={[4, 4, 0, 0]} />
                                </BarChart>
                            </ResponsiveContainer>
                        ) : (
                            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                                Keine Herkunftsdaten vorhanden
                            </div>
                        )}
                    </div>
                </ChartCard>

                {/* Feedback Distribution */}
                {qualityData && qualityData.feedbackStats.length > 0 && (
                    <ChartCard title="Feedback-Verteilung">
                        <div style={{ height: '280px' }}>
                            <ResponsiveContainer width="100%" height="100%">
                                <BarChart data={qualityData.feedbackStats.map(f => ({
                                    ...f,
                                    feedback: f.feedback === 'positive' ? 'Positiv' :
                                        f.feedback === 'negative' ? 'Negativ' : 'Kein Feedback'
                                }))}>
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis
                                        dataKey="feedback"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                    />
                                    <YAxis tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Bar dataKey="count" radius={[4, 4, 0, 0]}>
                                        {qualityData.feedbackStats.map((entry, index) => (
                                            <Cell
                                                key={`feedback-${index}`}
                                                fill={entry.feedback === 'positive' ? '#2d8f4e' :
                                                    entry.feedback === 'negative' ? '#c0392b' : '#6b7280'}
                                            />
                                        ))}
                                    </Bar>
                                </BarChart>
                            </ResponsiveContainer>
                        </div>
                    </ChartCard>
                )}

                {/* Retrieval Score Trend */}
                {qualityData && qualityData.scoreOverTime.length > 0 && (
                    <ChartCard title="Retrieval-Score im Zeitverlauf">
                        <div style={{ height: '280px' }}>
                            <ResponsiveContainer width="100%" height="100%">
                                <AreaChart data={qualityData.scoreOverTime.map(s => ({
                                    ...s,
                                    day: formatDate(s.day),
                                    avgScore: Number((s.avgScore ?? 0).toFixed(3))
                                }))}>
                                    <CartesianGrid strokeDasharray="3 3" stroke="var(--border-color)" />
                                    <XAxis
                                        dataKey="day"
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                    />
                                    <YAxis
                                        tick={{ fill: 'var(--text-secondary)', fontSize: 12 }}
                                        domain={[0, 1]}
                                    />
                                    <Tooltip content={<CustomTooltip />} />
                                    <Area type="monotone" dataKey="avgScore" stroke="#165a97" fill="#165a9740" name="Ø Score" />
                                </AreaChart>
                            </ResponsiveContainer>
                        </div>
                    </ChartCard>
                )}

                {/* Low-Score Queries */}
                {qualityData && qualityData.lowScoreQueries.length > 0 && (
                    <ChartCard title="Niedrig bewertete Anfragen (letzte 7 Tage)">
                        <div style={{ maxHeight: '280px', overflowY: 'auto' }}>
                            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem' }}>
                                <thead>
                                    <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
                                        <th style={{ textAlign: 'left', padding: '0.5rem', color: 'var(--text-secondary)', fontWeight: 500 }}>Anfrage</th>
                                        <th style={{ textAlign: 'right', padding: '0.5rem', color: 'var(--text-secondary)', fontWeight: 500, whiteSpace: 'nowrap' }}>Ø Score</th>
                                        <th style={{ textAlign: 'right', padding: '0.5rem', color: 'var(--text-secondary)', fontWeight: 500 }}>Datum</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {qualityData.lowScoreQueries.map(q => (
                                        <tr key={q.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                            <td style={{ padding: '0.5rem', color: 'var(--text-primary)', maxWidth: '300px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                                {q.content.length > 80 ? q.content.substring(0, 80) + '...' : q.content}
                                            </td>
                                            <td style={{ padding: '0.5rem', textAlign: 'right', color: '#c0392b', fontWeight: 500 }}>
                                                {Number(q.avgScore).toFixed(3)}
                                            </td>
                                            <td style={{ padding: '0.5rem', textAlign: 'right', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                                                {formatDate(q.createdAt)}
                                            </td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                    </ChartCard>
                )}
            </div>
        </main>
    );
}
