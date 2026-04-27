import { signV4 } from './signer';
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

async function buildAuthHeaders(
  config: S3Config,
  method: string,
  contentType: string,
  canonicalResource: string,
): Promise<Record<string, string>> {
  const { authorization, amzDate, contentSha256 } = await signV4(
    method, contentType, canonicalResource, config.secretKey,
    undefined, undefined, config.accessKey,
  );
  return {
    Authorization: authorization,
    'X-Amz-Date': amzDate,
    'X-Amz-Content-Sha256': contentSha256,
  };
}

export async function s3Request(
  config: S3Config,
  method: string,
  path: string,
  options?: { body?: BodyInit; contentType?: string },
): Promise<{ ok: boolean; status: number; body: string; headers: Headers }> {
  const contentType = options?.contentType ?? '';
  const canonicalResource = path.split('?')[0];
  const headers = await buildAuthHeaders(config, method, contentType, canonicalResource);

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

    const contentType = file.type || 'application/octet-stream';
    const canonicalResource = path.split('?')[0];
    buildAuthHeaders(config, 'PUT', contentType, canonicalResource)
      .then((headers) => {
        for (const [k, v] of Object.entries(headers)) {
          xhr.setRequestHeader(k, v);
        }
        xhr.setRequestHeader('Content-Type', contentType);
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
  const headers = await buildAuthHeaders(config, 'GET', contentType, canonicalResource);

  const resp = await fetch(`${config.endpoint}${path}`, {
    method: 'GET',
    headers,
  });
  if (!resp.ok) {
    const body = await resp.text();
    const s3Err = parseError(body);
    throw new S3ClientError({ ...s3Err, statusCode: resp.status }, resp.status);
  }
  return resp.blob();
}
