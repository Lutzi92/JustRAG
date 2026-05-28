import { useMemo, useState, useEffect } from 'react';
import { Loader2 } from 'lucide-react';
import { useKbCore } from '../contexts/KbCoreContext';
import { useKbChat } from '../contexts/KbChatContext';
import { useKbData } from '../contexts/KbDataContext';
import { useTheme } from '../contexts/ThemeContext';
import { getActiveJobs, type RegisteredJob } from '../utils/jobRegistry';

const JOB_TYPE_LABELS: Record<string, { de: string; en: string }> = {
  research: { de: 'Recherche', en: 'Research' },
  academicResearch: { de: 'Akademische Recherche', en: 'Academic research' },
};

export function BackgroundJobsIndicator() {
  const { t, language } = useTheme();
  const { currentKb } = useKbCore();
  const { researchRunning, academicResearchRunning } = useKbChat();
  const { webTools, rssLoading, confluenceLoading, confluenceSources, fileMgmt } = useKbData();

  // Poll registry for cross-KB jobs (every 3s)
  const [registryJobs, setRegistryJobs] = useState<RegisteredJob[]>([]);
  useEffect(() => {
    const update = () => setRegistryJobs(getActiveJobs());
    update();
    const interval = setInterval(update, 3000);
    return () => clearInterval(interval);
  }, []);

  const activeJobs = useMemo(() => {
    const jobs: string[] = [];
    const currentKbId = currentKb?.id;

    // Current KB jobs from context (real-time)
    if (webTools.toolLoading) jobs.push(t('bgJobUrlFetch'));
    if (webTools.webResearchRunning) jobs.push(t('bgJobWebResearch'));
    if (rssLoading) jobs.push(t('bgJobRss'));
    if (confluenceLoading || confluenceSources.some(s => s.status === 'syncing')) {
      jobs.push(t('bgJobConfluence'));
    }
    if (fileMgmt.files?.some(f => f.status === 'processing')) {
      jobs.push(t('bgJobFileProcessing'));
    }
    if (researchRunning) jobs.push(t('bgJobResearch'));
    if (academicResearchRunning) jobs.push(t('bgJobAcademicResearch'));

    // Registry jobs (cross-KB always shown, current-KB only if context doesn't already cover it)
    const contextCovers: Record<string, boolean> = {
      research: researchRunning,
      academicResearch: academicResearchRunning,
    };
    for (const job of registryJobs) {
      const isCurrentKb = job.kbId === currentKbId;
      if (isCurrentKb && contextCovers[job.jobType]) continue; // already shown above
      const label = JOB_TYPE_LABELS[job.jobType]?.[language] || job.jobType;
      const suffix = isCurrentKb ? '' : ` (${job.kbName})`;
      jobs.push(`${label}${suffix}`);
    }

    return jobs;
  }, [
    currentKb?.id, webTools.toolLoading, webTools.webResearchRunning,
    rssLoading, confluenceLoading, confluenceSources,
    fileMgmt.files, researchRunning, academicResearchRunning,
    registryJobs, t, language,
  ]);

  if (activeJobs.length === 0) return null;

  const tooltipText = activeJobs.join('\n');

  return (
    <div
      className="bg-jobs-indicator"
      title={tooltipText}
      aria-label={t('bgJobsRunning')}
      role="status"
    >
      <Loader2 size={18} className="bg-jobs-spinner" aria-hidden="true" />
    </div>
  );
}
