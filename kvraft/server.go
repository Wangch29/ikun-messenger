package kvraft

import (
	"bytes"
	"context"
	"encoding/gob"
	"log"
	"net"
	"sync"
	"time"

	"github.com/Wangch29/IkunMessenger/api/kvpb"
	"github.com/Wangch29/IkunMessenger/raft"
	"google.golang.org/grpc"
)

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"
	ErrWrongLeader = "ErrWrongLeader"
	ErrTimeout     = "ErrTimeout"
)

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	dead int32

	maxraftstate int // snapshot threshold

	// database in memory
	db map[string]string

	// index -> channel, for notifying the caller.
	waitCh map[int]chan OpResult

	// ClientId -> LastRequestId, for deduplication.
	lastApplied map[int64]int64

	kvpb.UnimplementedKVServiceServer
}

type OpType string

const (
	OpPut OpType = "Put"
	OpGet OpType = "Get"
)

type Op struct {
	Type      OpType
	Key       string
	Value     string
	ClientId  int64
	RequestId int64
}

type OpResult struct {
	Err   string
	Value string
}

func NewKVServer(me int, rf *raft.Raft, applyCh chan raft.ApplyMsg, maxraftstate int) *KVServer {
	kv := &KVServer{
		me:           me,
		rf:           rf,
		applyCh:      applyCh,
		maxraftstate: maxraftstate,
		db:           make(map[string]string),
		waitCh:       make(map[int]chan OpResult),
		lastApplied:  make(map[int64]int64),
	}

	go kv.applier()

	return kv
}

func (kv *KVServer) StartKVServer(port string) error {
	lis, err := net.Listen("tcp", port)
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	kvpb.RegisterKVServiceServer(s, kv)
	log.Printf("[KVServer %d] Listening on %s", kv.me, port)
	return s.Serve(lis)
}

// Put RPC Handler
func (kv *KVServer) Put(ctx context.Context, args *kvpb.PutArgs) (*kvpb.PutReply, error) {
	reply := &kvpb.PutReply{}
	op := Op{
		Type:      OpPut,
		Key:       args.Key,
		Value:     args.Value,
		ClientId:  args.ClientId,
		RequestId: args.RequestId,
	}

	// 1. Serialize Op
	command, err := encodeOp(op)
	if err != nil {
		log.Printf("Encode error: %v", err)
		return nil, err
	}

	// 2. Submit to Raft
	index, _, isLeader := kv.rf.Start(command)
	if !isLeader {
		reply.Err = ErrWrongLeader
		return reply, nil
	}

	// 3. Create wait channel
	kv.mu.Lock()
	ch := make(chan OpResult, 1)
	kv.waitCh[index] = ch
	kv.mu.Unlock()

	log.Printf("[KVServer %d] Started Put key=%s at index=%d", kv.me, args.Key, index)

	// 4. Wait for result
	select {
	case res := <-ch:
		reply.Err = res.Err
		return reply, nil
	case <-time.After(500 * time.Millisecond):
		kv.mu.Lock()
		delete(kv.waitCh, index)
		kv.mu.Unlock()
		reply.Err = ErrTimeout
		return reply, nil
	}
}

// Get RPC Handler
func (kv *KVServer) Get(ctx context.Context, args *kvpb.GetArgs) (*kvpb.GetReply, error) {
	reply := &kvpb.GetReply{}
	op := Op{
		Type:      OpGet,
		Key:       args.Key,
		ClientId:  args.ClientId,
		RequestId: args.RequestId,
	}

	command, _ := encodeOp(op)

	index, _, isLeader := kv.rf.Start(command)
	if !isLeader {
		reply.Err = ErrWrongLeader
		return reply, nil
	}

	kv.mu.Lock()
	ch := make(chan OpResult, 1)
	kv.waitCh[index] = ch
	kv.mu.Unlock()

	select {
	case res := <-ch:
		reply.Err = res.Err
		reply.Value = res.Value
		return reply, nil
	case <-time.After(500 * time.Millisecond):
		kv.mu.Lock()
		delete(kv.waitCh, index)
		kv.mu.Unlock()
		reply.Err = ErrTimeout
		return reply, nil
	}
}

func (kv *KVServer) applier() {
	for msg := range kv.applyCh {
		if msg.CommandValid {
			kv.mu.Lock()
			op, err := decodeOp(msg.Command)
			if err != nil {
				log.Printf("Failed to decode command: %v", err)
				kv.mu.Unlock()
				continue
			}

			var res OpResult
			res.Err = OK

			// Deduplication
			if op.Type == OpPut {
				if lastReq, ok := kv.lastApplied[op.ClientId]; ok && lastReq >= op.RequestId {
					// Duplicate request, do nothing
				} else {
					kv.db[op.Key] = op.Value
					kv.lastApplied[op.ClientId] = op.RequestId
					log.Printf("[KVServer %d] Applied Put key=%s val=%s", kv.me, op.Key, op.Value)
				}
			} else {
				// Get
				val, ok := kv.db[op.Key]
				if ok {
					res.Value = val
				} else {
					res.Err = ErrNoKey
				}
			}

			// Notify waiter
			if ch, ok := kv.waitCh[msg.CommandIndex]; ok {
				select {
				case ch <- res:
				default:
				}
				delete(kv.waitCh, msg.CommandIndex)
			}
			kv.mu.Unlock()
		}
	}
}

// --- Helpers ---

func encodeOp(op Op) ([]byte, error) {
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	if err := e.Encode(op); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func decodeOp(data []byte) (Op, error) {
	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)
	var op Op
	err := d.Decode(&op)
	return op, err
}
