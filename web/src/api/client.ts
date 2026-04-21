import { signV2 } from './signer';
import type { S3Error } from './types';
import { parseError } from './xml-parser';

export interface S3Config {
  endpoint: string;
  accessKey: string;
  secretKey: string;
}

class S3ClientError extends Error {
  code: string;
  statusCode: number;

  constructor(s3Err: S3Error, statusCode: number) {
    super(s3Err.message);
    this.code = s3Err.code;
    this.statusCode = statusCode;
    this.name = 'S3ClientError';
  }
}

export async function s3Request(
  config: S3Config,
  method: string,
  path: string,
  options?: { body?: BodyInit; contentType?: string },
): Promise<{ ok: boolean; status: number; body: string; headers: Headers }> {
  const contentType = options?.contentType ?? '';
  // canonicalResource 只包含路径，不含查询参数（与服务器侧 auth.go 一致）
  const canonicalResource = path.split('?')[0];
  const { signature, date } = await signV2(method, contentType, canonicalResource, config.secretKey);

  const headers: Record<string, string> = {
    Authorization: `AWS ${config.accessKey}:${signature}`,
    'X-Amz-Date': date,
  };
  if (contentType) {
    headers['Content-Type'] = contentType;
  }

  const resp = await fetch(`${config.endpoint}${path}`, {
    method,
    headers,
    body: options?.body,
  });

  const body = await resp.text();
  if (!resp.ok && body) {
    const s3Err = parseError(body);
    throw new S3ClientError({ ...s3Err, statusCode: resp.status }, resp.status);
  }

  return { ok: resp.ok, status: resp.status, body, headers: resp.headers };
}

export function uploadObject(
  config: S3Config,
  path: string,
  file: File,
  onProgress?: (loaded: number, total: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', `${config.endpoint}${path}`);

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) {
        onProgress(e.loaded, e.total);
      }
    };

    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve();
      } else {
        const s3Err = parseError(xhr.responseText);
        reject(new S3ClientError({ ...s3Err, statusCode: xhr.status }, xhr.status));
      }
    };

    xhr.onerror = () => reject(new Error('Network error'));

    // 异步签名后发送。
    signV2('PUT', file.type || 'application/octet-stream', path.split('?')[0], config.secretKey)
      .then(({ signature, date }) => {
        xhr.setRequestHeader('Authorization', `AWS ${config.accessKey}:${signature}`);
        xhr.setRequestHeader('X-Amz-Date', date);
        xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
        xhr.send(file);
      })
      .catch(reject);
  });
}

export function downloadUrl(config: S3Config, bucket: string, key: string): string {
  return `${config.endpoint}/${bucket}/${key}`;
}

export async function downloadObject(
  config: S3Config,
  bucket: string,
  key: string,
): Promise<Blob> {
  const path = `/${bucket}/${key}`;
  const contentType = '';
  const canonicalResource = path.split('?')[0];
  const { signature, date } = await signV2('GET', contentType, canonicalResource, config.secretKey);

  const resp = await fetch(`${config.endpoint}${path}`, {
    method: 'GET',
    headers: {
      Authorization: `AWS ${config.accessKey}:${signature}`,
      'X-Amz-Date': date,
    },
  });
  if (!resp.ok) {
    const body = await resp.text();
    const s3Err = parseError(body);
    throw new S3ClientError({ ...s3Err, statusCode: resp.status }, resp.status);
  }
  return resp.blob();
}
