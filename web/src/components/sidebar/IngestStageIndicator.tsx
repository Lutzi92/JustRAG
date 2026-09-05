import { Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';

const STAGE_LABEL_KEYS: Record<string, string> = {
    parse: 'ingestStageParse',
    tabular: 'ingestStageTabular',
    enrich: 'ingestStageEnrich',
    embed: 'ingestStageEmbed',
    kg: 'ingestStageKg',
    hype: 'ingestStageHype',
    raptor: 'ingestStageRaptor',
};

interface IngestStageIndicatorProps {
    stage: string;
    index?: number;
    total?: number;
    fileName: string;
    detail?: string;
}

/**
 * Upload-section ingestion indicator: spinner + n/x + a short label describing
 * the current pipeline stage. Replaces the old percentage progress bar.
 * `detail` (files.stage_detail — live per-stage progress text, e.g. a
 * spreadsheet's "Blatt 2/3 · 120000 Zeilen") renders as a muted second line
 * when present.
 */
export function IngestStageIndicator({ stage, index, total, fileName, detail }: IngestStageIndicatorProps) {
    const { t } = useTheme();
    const label = t(STAGE_LABEL_KEYS[stage] ?? 'ingestStageGeneric');
    const counter = index != null && total != null ? `${index}/${total}` : null;
    return (
        <div className="sidebar-left__file-stage-wrap">
            <div
                className="sidebar-left__file-stage"
                role="status"
                aria-live="polite"
                aria-label={`${label} ${fileName}`}
            >
                <Loader2 className="animate-spin" size={14} />
                {counter && <span className="sidebar-left__file-stage-count">{counter}</span>}
                <span className="sidebar-left__file-stage-label">{label}</span>
            </div>
            {detail && <div className="sidebar-left__file-stage-detail">{detail}</div>}
        </div>
    );
}
