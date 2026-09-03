import React from 'react';
import { useTheme } from '../../contexts/ThemeContext';
import type { SyncSchedule } from '../../types';

interface SyncScheduleSelectProps {
    id: string;
    value: SyncSchedule;
    onChange: (value: SyncSchedule) => void;
    /** Optional label text; defaults to the shared "Synchronisation" label. */
    label?: string;
}

/**
 * The single sync-schedule control for every source type. Automatic syncs run
 * inside the admin-configured night window, which is why the options say
 * "nachts" rather than naming a time.
 */
export const SyncScheduleSelect: React.FC<SyncScheduleSelectProps> = ({ id, value, onChange, label }) => {
    const { t } = useTheme();
    return (
        <div className="sidebar-left__slider-row">
            <label htmlFor={id}>{label ?? t('syncScheduleLabel')}</label>
            <select
                id={id}
                value={value}
                onChange={(e) => onChange(e.target.value as SyncSchedule)}
                className="sidebar-left__tools-select"
            >
                <option value="manual">{t('syncScheduleManual')}</option>
                <option value="daily">{t('syncScheduleDaily')}</option>
                <option value="weekly">{t('syncScheduleWeekly')}</option>
            </select>
        </div>
    );
};
