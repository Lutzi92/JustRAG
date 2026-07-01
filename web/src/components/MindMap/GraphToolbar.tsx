import { useState } from 'react';
import { Panel } from '@xyflow/react';

interface Props {
    allTypes: string[];
    activeTypes: Set<string>;
    onToggleType: (t: string) => void;
    onSearch: (q: string) => void;
    t: (k: string) => string;
}

export const GraphToolbar: React.FC<Props> = ({ allTypes, activeTypes, onToggleType, onSearch, t }) => {
    const [q, setQ] = useState('');
    return (
        <Panel position="top-right">
            <div className="graph-toolbar">
                <input
                    type="search"
                    value={q}
                    placeholder={t('graphSearchPlaceholder')}
                    onChange={e => setQ(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter') onSearch(q.trim()); }}
                    aria-label={t('graphSearchPlaceholder')}
                />
                {allTypes.length > 1 && (
                    <div className="graph-toolbar-chips" role="group" aria-label={t('graphFilterTypes')}>
                        {allTypes.map(ty => (
                            <button
                                key={ty}
                                type="button"
                                className="graph-type-chip"
                                aria-pressed={activeTypes.has(ty)}
                                onClick={() => onToggleType(ty)}
                            >
                                {ty}
                            </button>
                        ))}
                    </div>
                )}
            </div>
        </Panel>
    );
};
