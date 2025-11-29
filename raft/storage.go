package raft

import "sync"

// Storage 定义了 Raft 需要的持久化接口
// 暂时用于替代 labrpc 中的 Persister
type Storage interface {
	Save(raftState []byte, snapshot []byte)
	ReadRaftState() []byte
	ReadSnapshot() []byte
	RaftStateSize() int
}

// MemoryStorage implements the Storage interface based on memory for testing.
type MemoryStorage struct {
	mu        sync.Mutex
	raftState []byte
	snapshot  []byte
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{}
}

func (ms *MemoryStorage) Save(raftState []byte, snapshot []byte) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.raftState = raftState
	ms.snapshot = snapshot
}

func (ms *MemoryStorage) ReadRaftState() []byte {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.raftState
}

func (ms *MemoryStorage) ReadSnapshot() []byte {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.snapshot
}

func (ms *MemoryStorage) RaftStateSize() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return len(ms.raftState)
}
