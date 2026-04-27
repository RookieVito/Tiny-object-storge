// AWS Signature V4 签名，与 Go 侧 src/auth/v4.go 完全一致。

const ALGORITHM = 'AWS4-HMAC-SHA256';
const SERVICE = 's3';
const REQUEST_TYPE = 'aws4_request';

async function hmacSHA256(key: CryptoKey, data: string): Promise<ArrayBuffer> {
  const encoder = new TextEncoder();
  return crypto.subtle.sign('HMAC', key, encoder.encode(data));
}

async function importKey(secret: string): Promise<CryptoKey> {
  const encoder = new TextEncoder();
  return crypto.subtle.importKey(
    'raw',
    encoder.encode(secret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
}

async function sha256Hex(data: string): Promise<string> {
  const encoder = new TextEncoder();
  const hash = await crypto.subtle.digest('SHA-256', encoder.encode(data));
  return Array.from(new Uint8Array(hash))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

async function deriveSigningKey(secretKey: string, dateStamp: string, region: string): Promise<CryptoKey> {
  const encoder = new TextEncoder();

  const kDate = await crypto.subtle.importKey(
    'raw',
    encoder.encode(`AWS4${secretKey}`),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
  const kDateSigned = await hmacSHA256(kDate, dateStamp);

  const kRegion = await crypto.subtle.importKey(
    'raw',
    kDateSigned,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
  const kRegionSigned = await hmacSHA256(kRegion, region);

  const kService = await crypto.subtle.importKey(
    'raw',
    kRegionSigned,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
  const kServiceSigned = await hmacSHA256(kService, SERVICE);

  const kSigning = await crypto.subtle.importKey(
    'raw',
    kServiceSigned,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
  return kSigning;
}

function uriEncode(str: string): string {
  return encodeURIComponent(str).replace(/!/g, '%21').replace(/'/g, '%27').replace(/\(/g, '%28').replace(/\)/g, '%29').replace(/\*/g, '%2A');
}

function canonicalURI(path: string): string {
  const segments = path.split('/');
  return segments.map((s) => uriEncode(s)).join('/');
}

function getCanonicalHeaders(host: string, amzDate: string, contentType: string, payloadHash: string): string {
  const headers: [string, string][] = [['host', host], ['x-amz-content-sha256', payloadHash], ['x-amz-date', amzDate]];
  if (contentType) {
    headers.push(['content-type', contentType]);
  }
  headers.sort((a, b) => a[0].localeCompare(b[0]));
  return headers.map(([k, v]) => `${k}:${v}`).join('\n') + '\n';
}

function getSignedHeaders(contentType: string): string {
  const headers = ['host', 'x-amz-content-sha256', 'x-amz-date'];
  if (contentType) {
    headers.push('content-type');
  }
  headers.sort();
  return headers.join(';');
}

export interface V4Result {
  authorization: string;
  amzDate: string;
  contentSha256: string;
}

export async function signV4(
  method: string,
  contentType: string,
  canonicalResource: string,
  secretKey: string,
  region: string = 'us-east-1',
  accessKey: string = '',
): Promise<V4Result> {
  const now = new Date();
  const amzDate = now.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}/, ''); // YYYYMMDDTHHmmssZ
  const dateStamp = amzDate.slice(0, 8);
  const scope = `${dateStamp}/${region}/${SERVICE}/${REQUEST_TYPE}`;
  const payloadHash = await sha256Hex('UNSIGNED-PAYLOAD');

  const endpoint = new URL(canonicalResource, 'http://localhost').href;
  const host = new URL(canonicalResource, 'http://localhost').host;

  const canonicalHeaders = getCanonicalHeaders(host, amzDate, contentType, payloadHash);
  const signedHeaders = getSignedHeaders(contentType);
  const canonicalURI = canonicalURI(canonicalResource);

  const canonicalRequest = [
    method,
    canonicalURI,
    '',
    canonicalHeaders,
    '',
    signedHeaders,
    payloadHash,
  ].join('\n');

  const stringToSign = [
    ALGORITHM,
    amzDate,
    scope,
    await sha256Hex(canonicalRequest),
  ].join('\n');

  const signingKey = await deriveSigningKey(secretKey, dateStamp, region);
  const sigBuf = await hmacSHA256(signingKey, stringToSign);
  const signature = Array.from(new Uint8Array(sigBuf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');

  return {
    authorization: `${ALGORITHM} Credential=${accessKey}/${dateStamp}/${region}/${SERVICE}/${REQUEST_TYPE}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
    amzDate,
    contentSha256: payloadHash,
  };
}
