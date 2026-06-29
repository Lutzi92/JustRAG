import type { ButtonHTMLAttributes, ReactNode } from 'react';

// Button hierarchy (improvement #7). One component for the four tiers so
// weight/padding/radius/states can't drift across call sites — the visual
// system lives in index.css under `.btn`. Prefer this over hand-rolled
// `.search-button` / `.secondary-button` / `.text-button` markup in new code.

export type ButtonVariant = 'primary' | 'secondary' | 'tertiary' | 'destructive';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  /** Optional leading icon node (e.g. a lucide icon at size 16–18). */
  icon?: ReactNode;
}

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: 'btn--primary',
  secondary: 'btn--secondary',
  tertiary: 'btn--tertiary',
  destructive: 'btn--destructive',
};

export function Button({
  variant = 'primary',
  icon,
  children,
  className,
  type,
  ...rest
}: ButtonProps) {
  const cls = ['btn', VARIANT_CLASS[variant], className].filter(Boolean).join(' ');
  return (
    <button type={type ?? 'button'} className={cls} {...rest}>
      {icon}
      {children}
    </button>
  );
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Accessible name — required: icon-only buttons must carry a label. */
  label: string;
  icon: ReactNode;
  /** Draw the resting border/background (dense toolbars / table rows). */
  bordered?: boolean;
}

export function IconButton({
  label,
  icon,
  bordered = false,
  className,
  type,
  ...rest
}: IconButtonProps) {
  const cls = ['btn', 'btn--icon', bordered && 'btn--icon-bordered', className]
    .filter(Boolean)
    .join(' ');
  return (
    <button type={type ?? 'button'} className={cls} aria-label={label} title={label} {...rest}>
      {icon}
    </button>
  );
}
