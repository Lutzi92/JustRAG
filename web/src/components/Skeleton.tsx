import './Skeleton.css';

interface SkeletonProps {
  width?: string | number;
  height?: string | number;
  borderRadius?: string | number;
  className?: string;
  style?: React.CSSProperties;
}

export function Skeleton({ width, height, borderRadius, className = '', style }: SkeletonProps) {
  return (
    <div
      className={`skeleton ${className}`}
      style={{ width, height, borderRadius, ...style }}
      aria-hidden="true"
    />
  );
}

Skeleton.Text = function SkeletonText({ width = '100%', className = '' }: { width?: string | number; className?: string }) {
  return <div className={`skeleton skeleton--text ${className}`} style={{ width }} aria-hidden="true" />;
};

Skeleton.Circle = function SkeletonCircle({ size = 20 }: { size?: number }) {
  return <div className="skeleton skeleton--circle" style={{ width: size, height: size }} aria-hidden="true" />;
};

/* Pre-composed skeleton layouts */

export function KBCardSkeleton() {
  return (
    <li className="skeleton--card skeleton-kb-card" aria-hidden="true">
      <div className="skeleton-kb-card__top">
        <Skeleton.Circle size={20} />
        <Skeleton width={60} height={16} borderRadius={8} />
      </div>
      <Skeleton.Text width="70%" />
      <Skeleton.Text width="40%" />
    </li>
  );
}

export function FileRowSkeleton() {
  return (
    <li className="skeleton-file-row" aria-hidden="true">
      <Skeleton.Circle size={18} />
      <div className="skeleton-file-row__text">
        <Skeleton.Text width="80%" />
        <Skeleton.Text width="50%" />
      </div>
    </li>
  );
}

export function ContentRowSkeleton() {
  return (
    <li className="skeleton-file-row" aria-hidden="true">
      <div className="skeleton-file-row__text">
        <Skeleton.Text width="70%" />
        <Skeleton.Text width="45%" />
      </div>
    </li>
  );
}

export function MessageSkeleton({ role }: { role: 'user' | 'ai' }) {
  return (
    <div className={`skeleton-message skeleton-message--${role}`} aria-hidden="true">
      <Skeleton.Text width={role === 'user' ? '100%' : '90%'} />
      {role === 'ai' && (
        <>
          <Skeleton.Text width="100%" />
          <Skeleton.Text width="60%" />
        </>
      )}
    </div>
  );
}
