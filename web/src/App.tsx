import { useState, useCallback } from 'react';
import { useAuth } from './hooks/useAuth';
import LoginScreen from './components/LoginScreen';
import BucketList from './components/BucketList';
import ObjectBrowser from './components/ObjectBrowser';

type View = { type: 'buckets' } | { type: 'objects'; bucket: string };

export default function App() {
  const { config, logout } = useAuth();
  const [view, setView] = useState<View>({ type: 'buckets' });

  const handleSelectBucket = useCallback((bucket: string) => {
    setView({ type: 'objects', bucket });
  }, []);

  const handleBack = useCallback(() => {
    setView({ type: 'buckets' });
  }, []);

  if (!config) {
    return <LoginScreen />;
  }

  return (
    <div className="cyber-mesh cyber-grid min-h-screen flex flex-col">
      {/* Header */}
      <header className="glass-bright sticky top-0 z-40">
        <div className="mx-auto flex max-w-7xl items-center justify-between px-5 py-3">
          <div className="flex items-center gap-4">
            {/* Logo */}
            <h1 className="font-display text-sm font-bold tracking-[0.2em] text-white">
              TINY<span className="text-neon-cyan text-glow-cyan">.</span>STORAGE
            </h1>

            {/* Divider */}
            <span className="h-4 w-px bg-terminal-bright" />

            {/* Navigation */}
            <nav className="flex gap-1">
              <button
                onClick={handleBack}
                className={`font-ui text-sm font-medium px-3 py-1.5 rounded-md transition-all duration-200 ${
                  view.type === 'buckets'
                    ? 'text-neon-cyan bg-neon-cyan/5 shadow-neon-cyan'
                    : 'text-gray-500 hover:text-gray-300 hover:bg-white/3'
                }`}
              >
                BUCKETS
              </button>
              {view.type === 'objects' && (
                <button
                  onClick={handleBack}
                  className="font-ui text-sm text-gray-500 hover:text-gray-300 px-3 py-1.5 rounded-md transition-all duration-200 hover:bg-white/3 flex items-center gap-1.5 view-enter-fast"
                >
                  <span className="text-neon-cyan/50">&larr;</span>
                  {view.bucket}
                </button>
              )}
            </nav>
          </div>

          {/* Right side */}
          <div className="flex items-center gap-3">
            {/* Status indicator */}
            <div className="flex items-center gap-2">
              <span className="inline-block h-1.5 w-1.5 rounded-full bg-emerald-500 shadow-[0_0_6px_rgba(16,185,129,0.5)]" />
              <span className="font-mono text-xs text-gray-500">ONLINE</span>
            </div>

            {/* Divider */}
            <span className="h-4 w-px bg-terminal-bright" />

            {/* Logout */}
            <button
              onClick={logout}
              className="font-ui text-xs font-medium uppercase tracking-wider text-gray-500 hover:text-red-400 transition-colors duration-200 px-2 py-1"
            >
              LOGOUT
            </button>
          </div>
        </div>
        <div className="header-line" />
      </header>

      {/* Main Content */}
      <main className="mx-auto w-full max-w-7xl flex-1 px-5 py-6">
        {view.type === 'buckets' ? (
          <BucketList onSelect={handleSelectBucket} />
        ) : (
          <ObjectBrowser bucket={view.bucket} />
        )}
      </main>

      {/* Footer */}
      <footer className="py-3 text-center">
        <div className="header-line mb-3" />
        <span className="font-mono text-[10px] tracking-widest text-gray-600 uppercase">
          Tiny Object Storage // S3 Compatible // Go + React
        </span>
      </footer>
    </div>
  );
}
