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
}

/**
 * Upload-section ingestion indicator: spinner + n/x + a short label describing
 * the current pipeline stage. Replaces the old percentage progress bar.
 */
export function IngestStageIndicator({ stage, index, total, fileName }: IngestStageIndicatorProps) {
    const { t } = useTheme();
    const label = t(STAGE_LABEL_KEYS[stage] ?? 'ingestStageGeneric');
    const counter = index != null && total != null ? `${index}/${total}` : null;
    return (
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
    );
}
