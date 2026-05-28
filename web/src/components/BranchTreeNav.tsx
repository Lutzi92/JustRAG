import { useState, useEffect, useRef, useCallback } from 'react';
import { GitBranch, X, ChevronRight, ChevronDown } from 'lucide-react';
import type { Message } from '../types';
import { getPathToRoot } from '../utils/messageTree';
import { useTheme } from '../contexts/ThemeContext';

interface BranchTreeNavProps {
    messageTree: Map<string, Message>;
    activeLeafId: string | null;
    onSelectBranch: (leafId: string) => void;
}

/** Check if there are any branch points in the tree */
function hasBranches(map: Map<string, Message>): boolean {
    if (map.size > 0) return true; // DEBUG: Force show
    for (const msg of map.values()) {
        if (msg.childIds && msg.childIds.length > 1) return true;
    }
    return false;
}

/** Get root messages (no parent or parent not in map) */
function getRoots(map: Map<string, Message>): Message[] {
    const roots: Message[] = [];
    for (const msg of map.values()) {
        if (!msg.parentMessageId || !map.has(msg.parentMessageId)) {
            roots.push(msg);
        }
    }
    return roots;
}

/** Follow last child to find leaf of a branch */
function findLeafFrom(map: Map<string, Message>, startId: string): string {
    let currentId = startId;
    let current = map.get(currentId);
    while (current?.childIds && current.childIds.length > 0) {
        const lastChildId = current.childIds[current.childIds.length - 1];
        const child = map.get(lastChildId);
        if (!child) break;
        currentId = lastChildId;
        current = child;
    }
    return currentId;
}

/** Truncate text for preview */
function truncate(text: string, maxLen: number): string {
    if (text.length <= maxLen) return text;
    return text.slice(0, maxLen) + '…';
}

interface TreeNodeProps {
    msg: Message;
    map: Map<string, Message>;
    activePath: Set<string>;
    onSelectBranch: (leafId: string) => void;
    depth: number;
}

function TreeNode({ msg, map, activePath, onSelectBranch, depth }: TreeNodeProps) {
    const isActive = msg.id ? activePath.has(msg.id) : false;
    const children = (msg.childIds || []).map(id => map.get(id)).filter(Boolean) as Message[];
    const isBranchPoint = children.length > 1;
    const isUser = msg.role === 'user';
    const [collapsed, setCollapsed] = useState(false);

    // Skip enhanced query messages in the tree
    if (msg.isEnhanced) {
        return (
            <>
                {children.map(child => (
                    <TreeNode
                        key={child.id}
                        msg={child}
                        map={map}
                        activePath={activePath}
                        onSelectBranch={onSelectBranch}
                        depth={depth}
                    />
                ))}
            </>
        );
    }

    // For AI messages, group with the preceding user message — render children directly
    // We only render AI nodes as a combined "turn" below their user parent
    if (!isUser && depth > 0) return null;

    // Find the AI response paired with this user message
    const aiChild = isUser ? children.find(c => c.role === 'ai') : null;
    // Get the continuations after the AI response
    const nextMessages = aiChild
        ? (aiChild.childIds || []).map(id => map.get(id)).filter(Boolean) as Message[]
        : children;

    const leafId = msg.id ? findLeafFrom(map, msg.id) : null;
    const aiIsActive = aiChild?.id ? activePath.has(aiChild.id) : false;

    return (
        <div style={{ marginLeft: depth > 0 ? 10 : 0 }}>
            {/* The conversation turn: user question + AI answer */}
            <div
                style={{
                    display: 'flex',
                    alignItems: 'stretch',
                    gap: '0',
                    borderRadius: '6px',
                    background: isActive || aiIsActive ? 'var(--tag-bg)' : 'transparent',
                    border: isActive || aiIsActive ? '1px solid var(--accent-primary)' : '1px solid transparent',
                    marginBottom: '2px',
                    transition: 'background 0.15s',
                }}
                onMouseEnter={(e) => {
                    if (!isActive && !aiIsActive) e.currentTarget.style.background = 'var(--bg-secondary)';
                }}
                onMouseLeave={(e) => {
                    if (!isActive && !aiIsActive) e.currentTarget.style.background = 'transparent';
                }}
            >
                {/* Branch/collapse indicator */}
                <div style={{ display: 'flex', alignItems: 'flex-start', minWidth: 20, paddingTop: 6, paddingLeft: 6 }}>
                    {isBranchPoint || (aiChild && nextMessages.length > 1) ? (
                        <button
                            onClick={(e) => {
                                e.stopPropagation();
                                setCollapsed(!collapsed);
                            }}
                            style={{
                                background: 'none',
                                border: 'none',
                                padding: 0,
                                cursor: 'pointer',
                                color: 'var(--text-secondary)',
                                display: 'flex',
                            }}
                            aria-label={collapsed ? "Erweitern" : "Einklappen"}
                        >
                            <span className="icon-swap" key={collapsed ? 'right' : 'down'}>{collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}</span>
                        </button>
                    ) : (
                        <div style={{ width: 12 }} />
                    )}
                </div>

                <button
                    onClick={() => leafId && onSelectBranch(leafId)}
                    style={{
                        background: 'none',
                        border: 'none',
                        flex: 1,
                        textAlign: 'left',
                        cursor: 'pointer',
                        padding: '4px 6px 4px 6px',
                        display: 'flex',
                        alignItems: 'flex-start',
                        gap: '6px',
                    }}
                >


                    {/* Content preview */}
                    <div style={{ flex: 1, minWidth: 0 }} title={msg.content}>
                        <div style={{
                            fontSize: '0.75rem',
                            color: isActive ? 'var(--accent-primary)' : 'var(--text-primary)',
                            fontWeight: isActive ? 600 : 400,
                            lineHeight: 1.3,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                        }}>
                            {truncate(msg.content, 40)}
                        </div>
                        {aiChild && (
                            <div title={aiChild.content} style={{
                                fontSize: '0.75rem',
                                color: aiIsActive ? 'var(--accent-primary)' : 'var(--text-secondary)',
                                lineHeight: 1.3,
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                                marginTop: 1,
                                opacity: 0.8,
                            }}>
                                {truncate(aiChild.content || '…', 40)}
                            </div>
                        )}
                    </div>
                </button>
            </div>

            {/* Children (next user messages after AI response) */}
            {!collapsed && nextMessages.length > 0 && (
                <div style={{
                    borderLeft: nextMessages.length > 1 ? '2px solid var(--border-color)' : 'none',
                    marginLeft: nextMessages.length > 1 ? 19 : 0,
                    paddingLeft: nextMessages.length > 1 ? 4 : 0,
                }}>
                    {nextMessages.map(child => (
                        <TreeNode
                            key={child.id}
                            msg={child}
                            map={map}
                            activePath={activePath}
                            onSelectBranch={onSelectBranch}
                            depth={depth + 1}
                        />
                    ))}
                </div>
            )}
        </div>
    );
}

export function BranchTreeNav({ messageTree, activeLeafId, onSelectBranch }: BranchTreeNavProps) {
    const { language } = useTheme();
    const [isOpen, setIsOpen] = useState(false);
    const [width, setWidth] = useState(260);
    const [isResizing, setIsResizing] = useState(false);
    const sidebarRef = useRef<HTMLDivElement>(null);

    const startResizing = useCallback(() => {
        setIsResizing(true);
    }, []);

    const stopResizing = useCallback(() => {
        setIsResizing(false);
    }, []);

    const resize = useCallback((mouseMoveEvent: MouseEvent) => {
        if (isResizing && sidebarRef.current) {
            setWidth(currentWidth => {
                const newWidth = currentWidth - mouseMoveEvent.movementX;
                if (newWidth < 200) return 200;
                if (newWidth > 600) return 600;
                return newWidth;
            });
        }
    }, [isResizing]);

    useEffect(() => {
        window.addEventListener("mousemove", resize);
        window.addEventListener("mouseup", stopResizing);
        return () => {
            window.removeEventListener("mousemove", resize);
            window.removeEventListener("mouseup", stopResizing);
        };
    }, [resize, stopResizing]);

    const showTree = hasBranches(messageTree);
    const roots = getRoots(messageTree);
    let activePath = new Set<string>();
    if (activeLeafId) {
        const path = getPathToRoot(messageTree, activeLeafId);
        activePath = new Set(path.map(m => m.id!).filter(Boolean));
    }

    if (!showTree) return null;

    return (
        <div
            ref={sidebarRef}
            style={{
                width: isOpen ? width : 40,
                flexShrink: 0,
                borderLeft: '1px solid var(--border-color)',
                background: 'var(--bg-primary)',
                display: 'flex',
                flexDirection: 'column',
                transition: isResizing ? 'none' : 'width 0.2s ease',
                overflow: 'hidden',
                position: 'relative',
            }}>
            {isOpen && (
                <div
                    onMouseDown={startResizing}
                    style={{
                        position: 'absolute',
                        left: 0,
                        top: 0,
                        bottom: 0,
                        width: '4px',
                        cursor: 'ew-resize',
                        zIndex: 10,
                        background: isResizing ? 'var(--accent-primary)' : 'transparent',
                        opacity: isResizing ? 0.5 : 0,
                        transition: 'opacity 0.2s',
                    }}
                    onMouseEnter={(e) => e.currentTarget.style.opacity = '1'}
                    onMouseLeave={(e) => !isResizing && (e.currentTarget.style.opacity = '0')}
                />
            )}
            {!isOpen ? (
                <button
                    onClick={() => setIsOpen(true)}
                    style={{
                        display: 'flex',
                        flexDirection: 'column',
                        alignItems: 'center',
                        gap: '8px',
                        padding: '12px 0',
                        background: 'none',
                        border: 'none',
                        cursor: 'pointer',
                        color: 'var(--accent-primary)',
                        width: '100%',
                    }}
                    title={language === 'en' ? 'Show branch tree' : 'Zweig-Baum anzeigen'}
                >
                    <GitBranch size={18} />
                    <span style={{
                        writingMode: 'vertical-rl',
                        fontSize: '0.75rem',
                        fontWeight: 600,
                        letterSpacing: '0.5px',
                    }}>
                        {language === 'en' ? 'Branches' : 'Zweige'}
                    </span>
                </button>
            ) : (
                <>
                    <div style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        padding: '8px 10px',
                        borderBottom: '1px solid var(--border-color)',
                        flexShrink: 0,
                    }}>
                        <div style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '6px',
                            fontSize: '0.8rem',
                            fontWeight: 600,
                            color: 'var(--text-primary)',
                            whiteSpace: 'nowrap',
                        }}>
                            <GitBranch size={14} />
                            {language === 'en' ? 'Conversation Tree' : 'Gesprächsbaum'}
                        </div>
                        <button
                            onClick={() => setIsOpen(false)}
                            style={{
                                background: 'none',
                                border: 'none',
                                cursor: 'pointer',
                                color: 'var(--text-secondary)',
                                padding: '2px',
                                display: 'flex',
                                flexShrink: 0,
                            }}
                        >
                            <X size={14} />
                        </button>
                    </div>
                    <div style={{ flex: 1, overflowY: 'auto', padding: '6px' }}>
                        {roots.map(root => (
                            <TreeNode
                                key={root.id}
                                msg={root}
                                map={messageTree}
                                activePath={activePath}
                                onSelectBranch={onSelectBranch}
                                depth={0}
                            />
                        ))}
                    </div>
                </>
            )}
        </div>
    );
}
