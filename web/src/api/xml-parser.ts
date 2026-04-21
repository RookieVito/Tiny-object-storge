import type { BucketInfo, ListObjectsResult, ObjectInfo, S3Error } from './types';

function getText(parent: Element, tag: string): string {
  const el = parent.querySelector(tag);
  return el?.textContent ?? '';
}

export function parseListBuckets(xml: string): BucketInfo[] {
  const doc = new DOMParser().parseFromString(xml, 'application/xml');
  const buckets: BucketInfo[] = [];
  doc.querySelectorAll('Bucket').forEach((el) => {
    buckets.push({
      name: getText(el, 'Name'),
      creationDate: getText(el, 'CreationDate'),
    });
  });
  return buckets;
}

export function parseListObjects(xml: string): ListObjectsResult {
  const doc = new DOMParser().parseFromString(xml, 'application/xml');
  const result = doc.querySelector('ListBucketResult');
  if (!result) throw new Error('Invalid ListObjects response');

  const contents: ObjectInfo[] = [];
  result.querySelectorAll('Contents').forEach((el) => {
    contents.push({
      key: getText(el, 'Key'),
      size: parseInt(getText(el, 'Size'), 10),
      lastModified: getText(el, 'LastModified'),
      eTag: getText(el, 'ETag'),
      storageClass: getText(el, 'StorageClass'),
    });
  });

  const commonPrefixes: string[] = [];
  result.querySelectorAll('CommonPrefixes > Prefix').forEach((el) => {
    const p = el.textContent?.trim();
    if (p) commonPrefixes.push(p);
  });

  return {
    name: getText(result, 'Name'),
    prefix: getText(result, 'Prefix'),
    keyCount: parseInt(getText(result, 'KeyCount'), 10),
    maxKeys: parseInt(getText(result, 'MaxKeys'), 10),
    delimiter: getText(result, 'Delimiter'),
    isTruncated: getText(result, 'IsTruncated') === 'true',
    nextContinuationToken: getText(result, 'NextContinuationToken'),
    contents,
    commonPrefixes,
  };
}

export function parseError(xml: string): S3Error {
  const doc = new DOMParser().parseFromString(xml, 'application/xml');
  const el = doc.documentElement;
  return {
    code: el.querySelector('Code')?.textContent ?? '',
    message: el.querySelector('Message')?.textContent ?? '',
    statusCode: 0,
  };
}
