export interface BucketInfo {
  name: string;
  creationDate: string;
}

export interface ObjectInfo {
  key: string;
  size: number;
  lastModified: string;
  eTag: string;
  storageClass: string;
}

export interface ListObjectsResult {
  name: string;
  prefix: string;
  keyCount: number;
  maxKeys: number;
  delimiter: string;
  isTruncated: boolean;
  nextContinuationToken: string;
  contents: ObjectInfo[];
  commonPrefixes: string[];
}

export interface S3Error {
  code: string;
  message: string;
  statusCode: number;
}
