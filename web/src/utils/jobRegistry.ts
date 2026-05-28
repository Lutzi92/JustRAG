/**
 * Global job registry backed by localStorage.
 * Tracks long-running background jobs across KB switches and page reloads.
 */

const STORAGE_KEY = 'justrag-active-jobs';
const DEFAULT_TTL_MS = 30 * 60 * 1000; // 30 minutes safety net

export interface RegisteredJob {
  kbId: string;
  kbName: string;
  jobType: string;
  startedAt: number;
  ttlMs: number;
}

function readJobs(): RegisteredJob[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const jobs: RegisteredJob[] = JSON.parse(raw);
    const now = Date.now();
    // Filter out expired entries
    return jobs.filter(j => now - j.startedAt < j.ttlMs);
  } catch {
    return [];
  }
}

function writeJobs(jobs: RegisteredJob[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(jobs));
  } catch {
    // localStorage full or unavailable — silently ignore
  }
}

export function registerJob(kbId: string, kbName: string, jobType: string, ttlMs = DEFAULT_TTL_MS): void {
  const jobs = readJobs().filter(j => !(j.kbId === kbId && j.jobType === jobType));
  jobs.push({ kbId, kbName, jobType, startedAt: Date.now(), ttlMs });
  writeJobs(jobs);
}

export function unregisterJob(kbId: string, jobType: string): void {
  const jobs = readJobs().filter(j => !(j.kbId === kbId && j.jobType === jobType));
  writeJobs(jobs);
}

export function getActiveJobs(): RegisteredJob[] {
  return readJobs();
}

export function getActiveJobsForKb(kbId: string): RegisteredJob[] {
  return readJobs().filter(j => j.kbId === kbId);
}
