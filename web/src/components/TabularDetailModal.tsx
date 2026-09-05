import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { X, Loader2 } from 'lucide-react';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import type { TabularFileDetail, TabularTable } from '../types';
import './TabularDetailModal.css';

interface TabularDetailModalProps {
    show: boolean;
    kbId: string;
    fileId: string;
    fileName: string;
    onClose: () => void;
}

type TabularDetailContentProps = Omit<TabularDetailModalProps, 'show'>;

/**
 * "Tabellen" file-detail panel: fetches GET /api/kb/{kbId}/files/{fileId}/tabular
 * (tabular.FileTabularDTO) and renders the per-file ingest report (one card
 * per sheet) plus the materialized table/column structure for each sheet.
 */
const TabularDetailContent: React.FC<TabularDetailContentProps> = ({ kbId, fileId, fileName, onClose }) => {
    const { t, language } = useTheme();
    const [detail, setDetail] = useState<TabularFileDetail | null>(null);
    // Content remounts per fileId (see the default export below), so this
    // effect fetches exactly once per mounted instance — the loading/error
    // state's initial values below are that fetch's starting state, not a
    // reset performed from inside the effect.
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        let cancelled = false;

        axios.get<TabularFileDetail>(`${API_BASE_URL}/api/kb/${kbId}/files/${fileId}/tabular`)
            .then(res => {
                if (cancelled) return;
                setDetail(res.data);
            })
            .catch(() => {
                if (cancelled) return;
                setError(t('tabularLoadError'));
            })
            .finally(() => {
                if (!cancelled) setLoading(false);
            });

        return () => { cancelled = true; };
    }, [kbId, fileId, t]);

    useEffect(() => {
        const handleEsc = (e: KeyboardEvent) => {
            if (e.key === 'Escape') onClose();
        };
        window.addEventListener('keydown', handleEsc);
        return () => window.removeEventListener('keydown', handleEsc);
    }, [onClose]);

    const formatNum = (n: number): string =>
        language === 'de' ? n.toLocaleString('de-DE') : n.toLocaleString('en-US');

    // The report's Sheets array is appended in sheet-index order (one entry
    // per SheetInfo.Index, ingester.go) — the array position IS the
    // sheet_index tables[] entries reference, since ingest never skips or
    // reorders sheets (a failed sheet still gets a placeholder SheetReport).
    const tablesForSheet = (sheetIndex: number): TabularTable[] =>
        (detail?.tables ?? []).filter(tbl => tbl.sheet_index === sheetIndex);

    return (
        <div
            className="modal-overlay"
            role="presentation"
            onClick={e => { if (e.target === e.currentTarget) onClose(); }}
        >
            <div
                className="modal-content tabular-detail-modal"
                role="dialog"
                aria-modal="true"
                aria-labelledby="tabular-detail-title"
            >
                <div className="tabular-detail-modal__header">
                    <h2 id="tabular-detail-title" className="tabular-detail-modal__title">
                        {fileName} · {t('tabularPanelTitle')}
                    </h2>
                    <button onClick={onClose} className="icon-button" aria-label={t('close')}>
                        <X size={20} />
                    </button>
                </div>

                <div className="tabular-detail-modal__body">
                    {loading && (
                        <div className="tabular-detail-modal__status">
                            <Loader2 className="animate-spin" size={20} />
                        </div>
                    )}

                    {!loading && error && (
                        <div className="tabular-detail-modal__status tabular-detail-modal__status--error">
                            {error}
                        </div>
                    )}

                    {!loading && !error && detail && detail.report === null && (
                        <div className="tabular-detail-modal__status">{t('tabularNoReport')}</div>
                    )}

                    {!loading && !error && detail && detail.report && (
                        <div className="tabular-detail-modal__sheets">
                            {detail.report.sheets.map((sheet, sheetIndex) => (
                                <div key={sheetIndex} className="tabular-detail-modal__sheet-card">
                                    <div className="tabular-detail-modal__sheet-header">
                                        <span className="tabular-detail-modal__sheet-name">{sheet.name}</span>
                                        <span className="tabular-detail-modal__badge" title={t('tabularKind')}>
                                            {sheet.kind}
                                        </span>
                                        {sheet.hidden && (
                                            <span className="tabular-detail-modal__badge tabular-detail-modal__badge--muted">
                                                {t('tabularHidden')}
                                            </span>
                                        )}
                                        {sheet.used_llm && (
                                            <span className="tabular-detail-modal__badge tabular-detail-modal__badge--accent">
                                                {t('tabularUsedLLM')}
                                            </span>
                                        )}
                                    </div>

                                    <div className="tabular-detail-modal__counts">
                                        <span>{t('tabularHeaderRow')}: {sheet.header_row >= 0 ? formatNum(sheet.header_row + 1) : '—'}</span>
                                        <span>{t('tabularRowsRead')}: {formatNum(sheet.rows_read)}</span>
                                        <span>{t('tabularRowsMaterialised')}: {formatNum(sheet.rows_materialised)}</span>
                                        <span>{t('tabularRowsEmbedded')}: {formatNum(sheet.rows_embedded)}</span>
                                        <span>{t('tabularRowsPastCap')}: {formatNum(sheet.rows_past_cap)}</span>
                                        <span>{t('tabularFormulaEmpty')}: {formatNum(sheet.formula_cells_empty)}</span>
                                        <span>{t('tabularCoercionFailures')}: {formatNum(sheet.coercion_failures)}</span>
                                    </div>

                                    {sheet.dropped_columns > 0 && (
                                        <div className="tabular-detail-modal__note">
                                            {t('tabularDroppedColumns')}: {formatNum(sheet.dropped_columns)}
                                        </div>
                                    )}

                                    {sheet.notes && sheet.notes.length > 0 && (
                                        <ul className="tabular-detail-modal__notes">
                                            <li className="tabular-detail-modal__notes-label">{t('tabularNotes')}</li>
                                            {sheet.notes.map((note, i) => <li key={i}>{note}</li>)}
                                        </ul>
                                    )}

                                    {tablesForSheet(sheetIndex).map(table => (
                                        <div
                                            key={`${table.sheet_index}-${table.region_index}`}
                                            className="tabular-detail-modal__table-wrap"
                                        >
                                            <table className="tabular-detail-modal__table">
                                                <thead>
                                                    <tr>
                                                        <th>{t('tabularColHeader')}</th>
                                                        <th>{t('tabularColSQLName')}</th>
                                                        <th>{t('tabularColType')}</th>
                                                        <th>{t('tabularColRole')}</th>
                                                        <th>{t('tabularColDescription')}</th>
                                                        <th>{t('tabularColShadow')}</th>
                                                    </tr>
                                                </thead>
                                                <tbody>
                                                    {table.columns.map((col, i) => (
                                                        <tr key={i}>
                                                            <td>{col.original}</td>
                                                            <td>{col.name}</td>
                                                            <td>{col.type}</td>
                                                            <td>{col.role || '—'}</td>
                                                            <td>{col.description || ''}</td>
                                                            <td>{col.shadow_of || col.shadow_column || ''}</td>
                                                        </tr>
                                                    ))}
                                                </tbody>
                                            </table>
                                        </div>
                                    ))}
                                </div>
                            ))}
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
};

export const TabularDetailModal: React.FC<TabularDetailModalProps> = ({ show, ...rest }) => {
    if (!show) return null;
    // Remount inner content on fileId change (PdfPreviewModal pattern) so
    // loading/error/detail state resets per file.
    return <TabularDetailContent key={rest.fileId} {...rest} />;
};
