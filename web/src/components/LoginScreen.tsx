import { useState } from 'react';
import { useAuth } from '../hooks/useAuth';

export default function LoginScreen() {
  const { login } = useAuth();
  const [endpoint, setEndpoint] = useState(() => window.location.origin);
  const [accessKey, setAccessKey] = useState('');
  const [secretKey, setSecretKey] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      const resp = await fetch(`${endpoint}/`);
      if (!resp.ok) {
        throw new Error(`Server returned ${resp.status}`);
      }
      login({ endpoint, accessKey, secretKey });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Connection failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="cyber-mesh cyber-grid flex min-h-screen items-center justify-center p-4">
      {/* Animated background orbs */}
      <div className="pointer-events-none absolute inset-0 overflow-hidden">
        <div className="absolute -top-40 -left-40 h-80 w-80 rounded-full bg-neon-purple/10 blur-[100px] animate-pulse-glow" />
        <div className="absolute top-1/3 -right-20 h-60 w-60 rounded-full bg-neon-cyan/8 blur-[80px] animate-pulse-glow" style={{ animationDelay: '1s' }} />
        <div className="absolute -bottom-20 left-1/3 h-72 w-72 rounded-full bg-neon-magenta/6 blur-[90px] animate-pulse-glow" style={{ animationDelay: '2s' }} />
      </div>

      <div className="relative w-full max-w-lg animate-slide-up">
        {/* Top decorative line */}
        <div className="header-line mb-8" />

        {/* Title Block */}
        <div className="mb-8 text-center">
          <div className="mb-3 flex items-center justify-center gap-2">
            <span className="inline-block h-2 w-2 rounded-full bg-neon-cyan shadow-neon-cyan animate-pulse-glow" />
            <span className="font-display text-xs tracking-[0.4em] text-neon-cyan/70 uppercase">
              System Access
            </span>
            <span className="inline-block h-2 w-2 rounded-full bg-neon-magenta shadow-neon-magenta animate-pulse-glow" />
          </div>
          <h1 className="font-display text-3xl font-bold tracking-wider text-white">
            TINY<span className="text-neon-cyan text-glow-cyan">.</span>STORAGE
          </h1>
          <p className="mt-2 font-ui text-sm text-gray-500 tracking-wide">
            S3-compatible Object Storage Console
          </p>
        </div>

        {/* Login Card */}
        <div className="glass-bright relative corner-brackets rounded-xl p-8">
          <form onSubmit={handleSubmit} className="space-y-5">
            {/* Endpoint */}
            <div className="animate-slide-up opacity-0 stagger-1">
              <label className="mb-2 block font-ui text-xs font-semibold uppercase tracking-widest text-gray-400">
                Server Endpoint
              </label>
              <input
                type="url"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                className="neon-input w-full"
                placeholder="http://localhost:9000"
                required
              />
            </div>

            {/* Access Key */}
            <div className="animate-slide-up opacity-0 stagger-2">
              <label className="mb-2 block font-ui text-xs font-semibold uppercase tracking-widest text-gray-400">
                Access Key
              </label>
              <input
                type="text"
                value={accessKey}
                onChange={(e) => setAccessKey(e.target.value)}
                className="neon-input w-full"
                placeholder="minioadmin"
                required
              />
            </div>

            {/* Secret Key */}
            <div className="animate-slide-up opacity-0 stagger-3">
              <label className="mb-2 block font-ui text-xs font-semibold uppercase tracking-widest text-gray-400">
                Secret Key
              </label>
              <input
                type="password"
                value={secretKey}
                onChange={(e) => setSecretKey(e.target.value)}
                className="neon-input w-full"
                placeholder="••••••••"
                required
              />
            </div>

            {/* Error */}
            {error && (
              <div className="animate-fade-in rounded-lg border border-red-500/20 bg-red-500/5 px-4 py-3 font-ui text-sm text-red-400">
                <div className="flex items-center gap-2">
                  <span className="inline-block h-1.5 w-1.5 rounded-full bg-red-500 animate-pulse" />
                  {error}
                </div>
              </div>
            )}

            {/* Submit */}
            <div className="animate-slide-up opacity-0 stagger-4 pt-2">
              <button
                type="submit"
                disabled={loading}
                className="btn-magenta w-full py-3.5 text-base"
              >
                {loading ? (
                  <span className="flex items-center justify-center gap-2">
                    <span className="inline-block h-2 w-2 animate-pulse rounded-full bg-white" />
                    Connecting...
                  </span>
                ) : (
                  'INITIALIZE CONNECTION'
                )}
              </button>
            </div>
          </form>
        </div>

        {/* Bottom decorative line */}
        <div className="header-line mt-8" />

        {/* Version tag */}
        <div className="mt-4 text-center font-mono text-xs text-gray-600">
          v1.0 // EC + Distributed // Go Standard Library
        </div>
      </div>
    </div>
  );
}
