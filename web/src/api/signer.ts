// AWS Signature V2 签名，与 Go 侧 src/auth/auth.go 完全一致。

export async function signV2(
  method: string,
  contentType: string,
  canonicalResource: string,
  secretKey: string,
): Promise<{ signature: string; date: string }> {
  const date = new Date().toUTCString();
  const contentMD5 = '';
  const stringToSign = [method, contentMD5, contentType, date, canonicalResource].join('\n');

  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey(
    'raw',
    encoder.encode(secretKey),
    { name: 'HMAC', hash: 'SHA-1' },
    false,
    ['sign'],
  );
  const sigBuf = await crypto.subtle.sign('HMAC', key, encoder.encode(stringToSign));
  const signature = btoa(String.fromCharCode(...new Uint8Array(sigBuf)));

  return { signature, date };
}
