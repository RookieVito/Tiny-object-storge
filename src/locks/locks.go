package locks

import "sync"

// BucketLocks 提供基于 bucket 名称的互斥锁，用于并发安全。
// 写操作（CreateBucket、DeleteBucket、PutObject、DeleteObject）
// 在修改文件系统前获取对应 bucket 的锁。读操作不需要加锁，
// 因为 Linux 的并发 read/write 是安全的，原子 rename 保证一致性。
type BucketLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewBucketLocks 创建 BucketLocks 实例。
func NewBucketLocks() *BucketLocks {
	return &BucketLocks{locks: make(map[string]*sync.Mutex)}
}

// Lock 获取指定 bucket 的互斥锁。
func (bl *BucketLocks) Lock(bucket string) {
	bl.mu.Lock()
	m, ok := bl.locks[bucket]
	if !ok {
		m = &sync.Mutex{}
		bl.locks[bucket] = m
	}
	bl.mu.Unlock()
	m.Lock()
}

// Unlock 释放指定 bucket 的互斥锁。
func (bl *BucketLocks) Unlock(bucket string) {
	bl.mu.Lock()
	m, ok := bl.locks[bucket]
	bl.mu.Unlock()
	if ok {
		m.Unlock()
	}
}
