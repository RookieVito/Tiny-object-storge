import { useState, useEffect, useCallback, useRef } from 'react';
import { useAuth } from '../hooks/useAuth';
import { s3Request } from '../api/client';
import { parseListBuckets } from '../api/xml-parser';
import type { BucketInfo } from '../api/types';

interface BucketListProps {
  onSelect: (bucket: string) => void;
}

export default function BucketList({ onSelect }: BucketListProps) {
  const { config } = useAuth();
  const configRef = useRef(config);
  configRef.current = config;
  const [buckets, setBuckets] = useState<BucketInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [newBucket, setNewBucket] = useState('');
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const loadBuckets = useCallback(async () => {
    const cfg = configRef.current;
    if (!cfg) return;
    try {
      setLoading(true);
      const resp = await s3Request(cfg, 'GET', '/');
      setBuckets(parseListBuckets(resp.body));
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to list buckets');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadBuckets();
  }, [loadBuckets]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!config || !newBucket.trim()) return;
    setCreating(true);
    try {
      await s3Request(config, 'PUT', `/${newBucket.trim()}`);
      setNewBucket('');
      await loadBuckets();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create bucket');
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async (name: string) => {
    if (!config || !confirm(`确定删除 Bucket "${name}"？`)) return;
    setDeleting(name);
    try {
      await s3Request(config, 'DELETE', `/${name}`);
      await loadBuckets();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete bucket');
    } finally {
      setDeleting(null);
    }
  };

  return (
    <div className="view-enter">
      {/* Create Bucket */}
      <form onSubmit={handleCreate} className="mb-6 flex gap-3 animate-slide-up opacity-0 stagger-1">
        <input
          type="text"
          value={newBucket}
          onChange={(e) => setNewBucket(e.target.value)}
          placeholder="new-bucket-name"
          className="neon-input flex-1"
        />
        <button
          type="submit"
          disabled={creating || !newBucket.trim()}
          className="btn-cyan"
        >
          {creating ? (
            <span className="flex items-center gap-2">
              <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-void" />
              创建中
            </span>
          ) : '+ 创建'}
        </button>
      </form>

      {/* Error */}
      {error && (
        <div className="mb-4 animate-fade-in rounded-lg border border-red-500/20 bg-red-500/5 px-4 py-3 font-ui text-sm text-red-400">
          <div className="flex items-center justify-between">
            <span className="flex items-center gap-2">
              <span className="inline-block h-1.5 w-1.5 rounded-full bg-red-500 animate-pulse" />
              {error}
            </span>
            <button onClick={() => setError('')} className="text-red-500/50 hover:text-red-400 transition-colors">x</button>
          </div>
        </div>
      )}

      {/* Content */}
      {loading ? (
        <div className="py-16">
          <div className="mx-auto max-w-xs space-y-3">
            <div className="loading-bar" />
            <p className="text-center font-mono text-xs text-gray-500 animate-pulse-glow">
              SCANNING BUCKETS...
            </p>
          </div>
        </div>
      ) : buckets.length === 0 ? (
        <div className="py-16 text-center animate-fade-in">
          <div className="mb-4 text-4xl opacity-30">&#x2B22;</div>
          <p className="font-ui text-gray-500">暂无 Bucket，创建一个开始使用</p>
        </div>
      ) : (
        <div className="glass rounded-xl overflow-hidden animate-slide-up opacity-0 stagger-2">
          <table className="neon-table">
            <thead>
              <tr>
                <th className="px-4 py-3">Bucket 名称</th>
                <th className="px-4 py-3">创建时间</th>
                <th className="px-4 py-3 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {buckets.map((b, idx) => (
                <tr key={b.name} className="animate-slide-up opacity-0 group" style={{ animationDelay: `${0.05 * idx}s` }}>
                  <td className="px-4 py-3.5">
                    <button
                      onClick={() => onSelect(b.name)}
                      className="flex items-center gap-3 font-ui text-base font-semibold text-neon-cyan hover:text-glow-cyan transition-all duration-200 group-hover:translate-x-0.5"
                    >
                      <span className="text-neon-purple/60 text-lg">&#x25C6;</span>
                      {b.name}
                    </button>
                  </td>
                  <td className="px-4 py-3.5">
                    <span className="font-mono text-xs text-gray-500">
                      {b.creationDate}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-right">
                    <button
                      onClick={() => handleDelete(b.name)}
                      disabled={deleting === b.name}
                      className="btn-danger text-xs py-1.5 px-3"
                    >
                      {deleting === b.name ? '删除中...' : '删除'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* Bottom decorative line */}
          <div className="h-px bg-gradient-to-r from-transparent via-neon-cyan/20 to-transparent" />
        </div>
      )}
    </div>
  );
}
