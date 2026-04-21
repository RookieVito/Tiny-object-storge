import { useState, useEffect, useCallback, useRef } from 'react';
import { useAuth } from '../hooks/useAuth';
import { s3Request, uploadObject, downloadObject } from '../api/client';
import { parseListObjects } from '../api/xml-parser';
import type { ObjectInfo } from '../api/types';
import Breadcrumb from './Breadcrumb';

interface ObjectBrowserProps {
  bucket: string;
}

function formatSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB';
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB';
  return n + ' B';
}

function fileIcon(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || '';
  const map: Record<string, string> = {
    pdf: '&#x1F4C4;', doc: '&#x1F4C4;', docx: '&#x1F4C4;',
    xls: '&#x1F4CA;', xlsx: '&#x1F4CA;', csv: '&#x1F4CA;',
    png: '&#x1F5BC;', jpg: '&#x1F5BC;', jpeg: '&#x1F5BC;', gif: '&#x1F5BC;', svg: '&#x1F5BC;', webp: '&#x1F5BC;',
    mp4: '&#x1F3AC;', avi: '&#x1F3AC;', mkv: '&#x1F3AC;',
    mp3: '&#x1F3B5;', wav: '&#x1F3B5;', flac: '&#x1F3B5;',
    zip: '&#x1F4E6;', tar: '&#x1F4E6;', gz: '&#x1F4E6;', rar: '&#x1F4E6;',
    js: '&#x1F4BB;', ts: '&#x1F4BB;', py: '&#x1F4BB;', go: '&#x1F4BB;', rs: '&#x1F4BB;',
    json: '&#x1F4DD;', xml: '&#x1F4DD;', yaml: '&#x1F4DD;', yml: '&#x1F4DD;', toml: '&#x1F4DD;',
    md: '&#x1F4DD;', txt: '&#x1F4DD;',
  };
  return map[ext] || '&#x1F4C4;';
}

export default function ObjectBrowser({ bucket }: ObjectBrowserProps) {
  const { config } = useAuth();
  const [prefix, setPrefix] = useState('');
  const [objects, setObjects] = useState<ObjectInfo[]>([]);
  const [commonPrefixes, setCommonPrefixes] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState({ loaded: 0, total: 0 });
  const [deleting, setDeleting] = useState<string | null>(null);
  const [downloading, setDownloading] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const configRef = useRef(config);
  configRef.current = config;

  const loadObjects = useCallback(async () => {
    const cfg = configRef.current;
    if (!cfg) return;
    try {
      setLoading(true);
      let path = `/${bucket}?delimiter=/&max-keys=1000`;
      if (prefix) path += `&prefix=${encodeURIComponent(prefix)}`;
      const resp = await s3Request(cfg, 'GET', path);
      const result = parseListObjects(resp.body);
      setObjects(result.contents);
      setCommonPrefixes(result.commonPrefixes);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to list objects');
    } finally {
      setLoading(false);
    }
  }, [bucket, prefix]);

  useEffect(() => {
    loadObjects();
  }, [loadObjects]);

  const doUpload = useCallback(async (files: FileList | File[]) => {
    if (!config) return;
    setUploading(true);
    setUploadProgress({ loaded: 0, total: 0 });

    try {
      for (let i = 0; i < files.length; i++) {
        const file = files[i];
        const key = prefix + file.name;
        await uploadObject(config, `/${bucket}/${key}`, file, (loaded, total) => {
          setUploadProgress({ loaded, total });
        });
      }
      await loadObjects();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Upload failed');
    } finally {
      setUploading(false);
    }
  }, [config, bucket, prefix, loadObjects]);

  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files) return;
    await doUpload(files);
    e.target.value = '';
  };

  const handleDelete = async (key: string) => {
    if (!config || !confirm(`确定删除 "${key}"？`)) return;
    setDeleting(key);
    try {
      await s3Request(config, 'DELETE', `/${bucket}/${key}`);
      await loadObjects();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Delete failed');
    } finally {
      setDeleting(null);
    }
  };

  const handleDownload = async (key: string) => {
    if (!config) return;
    setDownloading(key);
    try {
      const blob = await downloadObject(config, bucket, key);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = key.split('/').pop() || key;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Download failed');
    } finally {
      setDownloading(null);
    }
  };

  // Drag & drop handlers
  const onDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(true);
  }, []);

  const onDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  }, []);

  const onDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
    if (e.dataTransfer.files.length > 0) {
      await doUpload(e.dataTransfer.files);
    }
  }, [doUpload]);

  const totalItems = commonPrefixes.length + objects.length;

  return (
    <div
      className="view-enter"
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {/* Drag Overlay */}
      {dragOver && (
        <div className="drag-overlay animate-fade-in">
          <div className="relative text-center">
            <div className="text-6xl mb-4 text-neon-cyan animate-pulse-glow">&#x2B06;</div>
            <p className="font-display text-xl text-neon-cyan text-glow-cyan tracking-wider">DROP TO UPLOAD</p>
            <p className="font-ui text-sm text-gray-400 mt-2">文件将上传到当前目录</p>
          </div>
        </div>
      )}

      {/* Header */}
      <div className="mb-5 flex items-center justify-between animate-slide-up opacity-0 stagger-1">
        <div>
          <h2 className="font-display text-lg font-bold tracking-wider text-white">
            <span className="text-neon-purple/60 mr-2">&#x25C6;</span>
            {bucket}
          </h2>
        </div>
        {totalItems > 0 && (
          <span className="font-mono text-xs text-gray-500">
            {objects.length} OBJ / {commonPrefixes.length} DIR
          </span>
        )}
      </div>

      {/* Breadcrumb */}
      <Breadcrumb prefix={prefix} onNavigate={setPrefix} />

      {/* Toolbar */}
      <div className="mb-5 flex flex-wrap items-center gap-3 animate-slide-up opacity-0 stagger-2">
        <label className="btn-cyan cursor-pointer">
          <input
            type="file"
            multiple
            onChange={handleUpload}
            disabled={uploading}
            className="hidden"
          />
          {uploading ? (
            <span className="flex items-center gap-2">
              <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-void" />
              上传中...
            </span>
          ) : (
            '+ 上传文件'
          )}
        </label>
        {uploading && uploadProgress.total > 0 && (
          <div className="flex items-center gap-3 animate-fade-in">
            <div className="h-1.5 w-36 overflow-hidden rounded-full bg-terminal-dim">
              <div
                className="progress-glow h-full rounded-full"
                style={{ width: `${Math.round((uploadProgress.loaded / uploadProgress.total) * 100)}%` }}
              />
            </div>
            <span className="font-mono text-xs text-gray-400">
              {formatSize(uploadProgress.loaded)} / {formatSize(uploadProgress.total)}
            </span>
          </div>
        )}
      </div>

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
              LOADING OBJECTS...
            </p>
          </div>
        </div>
      ) : totalItems === 0 ? (
        <div className="py-16 text-center animate-fade-in">
          <div className="mb-4 text-5xl opacity-20">&#x2B22;</div>
          <p className="font-ui text-gray-500 mb-2">此目录为空</p>
          <p className="font-mono text-xs text-gray-600">拖拽文件到此处或点击上传按钮</p>
        </div>
      ) : (
        <div className="glass rounded-xl overflow-hidden animate-slide-up opacity-0 stagger-3">
          <table className="neon-table">
            <thead>
              <tr>
                <th className="px-4 py-3">名称</th>
                <th className="px-4 py-3">大小</th>
                <th className="px-4 py-3">修改时间</th>
                <th className="px-4 py-3 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {/* Directories */}
              {commonPrefixes.map((cp, idx) => {
                const name = cp.endsWith('/') ? cp.slice(0, -1) : cp;
                const displayName = name.split('/').pop() || name;
                return (
                  <tr
                    key={cp}
                    className="cursor-pointer animate-slide-up opacity-0 group"
                    style={{ animationDelay: `${0.03 * idx}s` }}
                    onClick={() => setPrefix(cp)}
                  >
                    <td className="px-4 py-3">
                      <span className="flex items-center gap-3 font-ui text-sm font-medium text-neon-purple group-hover:text-neon-magenta transition-colors duration-200">
                        <span className="text-base group-hover:scale-110 transition-transform duration-200">&#x1F4C1;</span>
                        {displayName}/
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <span className="font-mono text-xs text-gray-600">DIR</span>
                    </td>
                    <td className="px-4 py-3">
                      <span className="font-mono text-xs text-gray-600">-</span>
                    </td>
                    <td className="px-4 py-3" />
                  </tr>
                );
              })}

              {/* Files */}
              {objects.map((obj, idx) => {
                const displayName = obj.key.split('/').pop() || obj.key;
                return (
                  <tr
                    key={obj.key}
                    className="animate-slide-up opacity-0 group"
                    style={{ animationDelay: `${0.03 * (commonPrefixes.length + idx)}s` }}
                  >
                    <td className="px-4 py-3">
                      <span
                        className="flex items-center gap-3 font-ui text-sm text-gray-300 group-hover:text-neon-cyan transition-colors duration-200"
                        dangerouslySetInnerHTML={{ __html: `${fileIcon(displayName)} ${displayName}` }}
                      />
                    </td>
                    <td className="px-4 py-3">
                      <span className="font-mono text-xs text-gray-400">
                        {formatSize(obj.size)}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <span className="font-mono text-xs text-gray-500">
                        {new Date(obj.lastModified).toLocaleString('zh-CN')}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <button
                          onClick={() => handleDownload(obj.key)}
                          disabled={downloading === obj.key}
                          className="btn-ghost text-xs py-1.5 px-3"
                        >
                          {downloading === obj.key ? (
                            <span className="flex items-center gap-1">
                              <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-neon-cyan" />
                              下载中
                            </span>
                          ) : '下载'}
                        </button>
                        <button
                          onClick={() => handleDelete(obj.key)}
                          disabled={deleting === obj.key}
                          className="btn-danger text-xs py-1.5 px-3"
                        >
                          {deleting === obj.key ? '删除中...' : '删除'}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {/* Bottom decorative line */}
          <div className="h-px bg-gradient-to-r from-transparent via-neon-cyan/20 to-transparent" />
        </div>
      )}
    </div>
  );
}
