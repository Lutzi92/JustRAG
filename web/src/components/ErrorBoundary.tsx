import { Component, type ReactNode, type ErrorInfo } from 'react';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('ErrorBoundary caught:', error, info.componentStack);
  }

  handleReload = () => {
    window.location.reload();
  };

  handleReset = () => {
    this.setState({ hasError: false, error: null });
  };

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) return this.props.fallback;

      return (
        <div style={{
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          height: '100dvh', width: '100%', fontFamily: 'system-ui, sans-serif',
          backgroundColor: 'var(--bg-primary, #111)', color: 'var(--text-primary, #eee)',
          flexDirection: 'column', gap: '16px', padding: '24px', textAlign: 'center',
        }}>
          <h2 style={{ margin: 0, fontSize: '1.25rem' }}>Something went wrong</h2>
          <p style={{ margin: 0, color: 'var(--text-secondary, #999)', maxWidth: '400px' }}>
            An unexpected error occurred. Try reloading the page.
          </p>
          {this.state.error && (
            <pre style={{
              margin: 0, padding: '12px', borderRadius: '8px',
              backgroundColor: 'var(--bg-secondary, #222)', fontSize: '0.8rem',
              maxWidth: '600px', overflow: 'auto', whiteSpace: 'pre-wrap',
              color: 'var(--text-secondary, #999)',
            }}>
              {this.state.error.message}
            </pre>
          )}
          <div style={{ display: 'flex', gap: '8px' }}>
            <button
              onClick={this.handleReset}
              style={{
                padding: '8px 20px', borderRadius: '8px', border: '1px solid var(--border-primary, #333)',
                backgroundColor: 'transparent', color: 'var(--text-primary, #eee)',
                cursor: 'pointer', fontSize: '0.9rem',
              }}
            >
              Try Again
            </button>
            <button
              onClick={this.handleReload}
              style={{
                padding: '8px 20px', borderRadius: '8px', border: 'none',
                backgroundColor: 'var(--accent-primary, #6366f1)', color: '#fff',
                cursor: 'pointer', fontSize: '0.9rem',
              }}
            >
              Reload Page
            </button>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
}
