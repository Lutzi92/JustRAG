import type { GeneratedContent } from '../types';

// Markdown-text generated-content types. These all store their payload as
// { text: markdown } and share the Studio markdown-editor render/edit/export
// path (MarkdownEditor + save + copy + DOCX/PDF). Adding a new text artifact
// (see go-backend contentgen.TextArtifacts) means adding its type here.
export const MARKDOWN_ARTIFACT_TYPES = new Set<GeneratedContent['type']>([
    'analysis',
    'abstract',
    'research',
    'briefing_doc',
    'faq',
    'study_guide',
    'timeline',
]);

// isMarkdownArtifact reports whether a generated-content type is rendered and
// edited as markdown text (vs. a structured type like flashcards/presentation
// that has its own renderer).
export function isMarkdownArtifact(type: GeneratedContent['type']): boolean {
    return MARKDOWN_ARTIFACT_TYPES.has(type);
}

// Translation keys for each artifact type, used to render a readable label in
// content lists. Types without an entry fall back to their raw string.
const ARTIFACT_TYPE_LABEL_KEYS: Partial<Record<GeneratedContent['type'], string>> = {
    analysis: 'analysis',
    research: 'research',
    abstract: 'abstract',
    flashcards: 'flashcards',
    presentation: 'slides',
    podcast: 'podcast',
    briefing_doc: 'briefingDoc',
    faq: 'faq',
    study_guide: 'studyGuide',
    timeline: 'timeline',
    quiz: 'quiz',
};

// artifactTypeLabel returns a human-readable, translated label for a
// generated-content type, falling back to the raw type string.
export function artifactTypeLabel(type: GeneratedContent['type'], t: (key: string) => string): string {
    const key = ARTIFACT_TYPE_LABEL_KEYS[type];
    return key ? t(key) : type;
}
