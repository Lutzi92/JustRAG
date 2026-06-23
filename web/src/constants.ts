export const MAX_FILES_PER_KB = 500;
export const MAX_FILES_PER_GLOBAL_KB = 1000;

// ACCEPTED_FILE_TYPES is the file-picker `accept` allowlist for KB uploads.
// It mirrors the backend's supported-extension allowlist
// (go-backend/internal/parser/supported.go); the backend rejects anything
// outside that list with a 400, so keep the two in sync. This is a UX hint
// only — the server-side check is authoritative.
export const ACCEPTED_FILE_TYPES = [
    // Documents
    '.pdf', '.docx', '.pptx', '.odt', '.epub', '.tex',
    // Spreadsheets / tabular
    '.xlsx', '.xls', '.ods', '.csv',
    // Images
    '.png', '.jpg', '.jpeg', '.webp', '.bmp', '.tif', '.tiff', '.gif',
    // Audio
    '.mp3', '.wav', '.m4a', '.mp4', '.mpeg', '.ogg', '.flac', '.webm',
    // Text / markup / config
    '.txt', '.md', '.markdown', '.json', '.log', '.yaml', '.yml',
].join(',');
