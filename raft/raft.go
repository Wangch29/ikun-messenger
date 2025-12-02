package raft

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wangch29/ikun-messenger/api/raftpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type ApplyMsg struct {
	CommandValid bool
	Command      []byte
	CommandIndex int

	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

type LogEntry struct {
	Term    int
	Command []byte
}

type PersistentState struct {
	CurrentTerm       int
	VotedFor          int
	Log               []LogEntry
	LastIncludedIndex int
	LastIncludedTerm  int
}

// StateType Raft Node roles: Follower, Candidate, Leader
type StateType int

const (
	Follower StateType = iota
	Candidate
	Leader
)

type Raft struct {
	mu         sync.Mutex
	peers      []raftpb.RaftServiceClient // gRPC clients for peers.
	peerConns  []*grpc.ClientConn         // gRPC connections to peers.
	grpcServer *grpc.Server               // gRPC server for this node.

	persister Storage // Persister for persistent state.
	me        int     // The node id of myself
	dead      int32

	raftpb.UnimplementedRaftServiceServer

	state       StateType
	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	applyChan chan ApplyMsg

	lastIncludedIndex int
	lastIncludedTerm  int

	received bool
}

func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == Leader
}

func (rf *Raft) persist() {
	rf.persistSnapshot(rf.persister.ReadSnapshot())
}

func (rf *Raft) persistSnapshot(snapshot []byte) {
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	e.Encode(PersistentState{
		CurrentTerm:       rf.currentTerm,
		VotedFor:          rf.votedFor,
		Log:               rf.log,
		LastIncludedIndex: rf.lastIncludedIndex,
		LastIncludedTerm:  rf.lastIncludedTerm,
	})
	raftstate := w.Bytes()
	rf.persister.Save(raftstate, snapshot)
}

func (rf *Raft) readPersist(data []byte) {
	if len(data) == 0 {
		return
	}
	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)
	var ps PersistentState
	if d.Decode(&ps) != nil {
		log.Printf("Fail to decode.\n")
		return
	} else {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		rf.currentTerm = ps.CurrentTerm
		rf.votedFor = ps.VotedFor
		rf.log = ps.Log
		rf.lastIncludedIndex = ps.LastIncludedIndex
		rf.lastIncludedTerm = ps.LastIncludedTerm
	}
}

// Start is called by the application layer to append a new command to the log.
// It returns the index and term of the new entry, and true if the operation is successful.
func (rf *Raft) Start(command []byte) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return -1, -1, false
	}

	index := rf.getLastLogIndex() + 1
	term := rf.currentTerm

	rf.log = append(rf.log, LogEntry{
		Term:    term,
		Command: command,
	})

	rf.persist()

	rf.updateCommitIndex()

	return index, term, true
}

// --- RPC Handlers ---

func (rf *Raft) RequestVote(ctx context.Context, args *raftpb.RequestVoteArgs) (*raftpb.RequestVoteReply, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply := &raftpb.RequestVoteReply{}

	// Check if the candidate's term is greater or equal to the current term.
	if int(args.CurrentTerm) < rf.currentTerm {
		reply.Term = int64(rf.currentTerm)
		reply.VoteGranted = false
		return reply, nil
	}

	// If the candidate's term is greater, call becomeFollower()
	//  to update the current term and votedFor.
	if int(args.CurrentTerm) > rf.currentTerm {
		rf.becomeFollower(int(args.CurrentTerm))
	}

	reply.Term = int64(rf.currentTerm)

	upToDate := (int(args.LastLogTerm) > rf.getLastLogTerm()) ||
		(int(args.LastLogTerm) == rf.getLastLogTerm() && int(args.LastLogIndex) >= rf.getLastLogIndex())

	if upToDate && (rf.votedFor == -1 || rf.votedFor == int(args.CandidateId)) {
		rf.received = true
		rf.votedFor = int(args.CandidateId)
		reply.VoteGranted = true
		rf.persist()
	} else {
		reply.VoteGranted = false
	}

	return reply, nil
}

func (rf *Raft) AppendEntries(ctx context.Context, args *raftpb.AppendEntriesArgs) (*raftpb.AppendEntriesReply, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply := &raftpb.AppendEntriesReply{}

	if int(args.Term) < rf.currentTerm {
		reply.Term = int64(rf.currentTerm)
		reply.Success = false
		return reply, nil
	}

	rf.received = true
	if int(args.Term) > rf.currentTerm || rf.state != Follower {
		rf.becomeFollower(int(args.Term))
	}
	reply.Term = int64(rf.currentTerm)

	// Check PrevLogIndex
	if int(args.PrevLogIndex) > rf.getLastLogIndex() {
		reply.Success = false
		reply.ConflictTerm = -1
		reply.ConflictLen = int64(len(rf.log))
		return reply, nil
	}

	// Check term mismatch
	if rf.getLogTerm(int(args.PrevLogIndex)) != int(args.PrevLogTerm) {
		reply.Success = false
		reply.ConflictTerm = int64(rf.getLogTerm(int(args.PrevLogIndex)))
		reply.ConflictIndex = int64(rf.findConflictIndex(int(args.PrevLogIndex), int(reply.ConflictTerm)))
		return reply, nil
	}

	// Append entries
	// Note: args.Entries is []*raftpb.LogEntry, need to convert to []LogEntry
	entries := make([]LogEntry, len(args.Entries))
	for i, e := range args.Entries {
		entries[i] = LogEntry{Term: int(e.Term), Command: e.Command}
	}

	rf.log = append(rf.log[:int(args.PrevLogIndex)+1-rf.lastIncludedIndex], entries...)
	rf.persist()

	if int(args.LeaderCommit) > rf.commitIndex {
		rf.commitIndex = min(int(args.LeaderCommit), rf.getLastLogIndex())
	}

	reply.Success = true
	return reply, nil
}

func (rf *Raft) InstallSnapshot(ctx context.Context, args *raftpb.InstallSnapshotArgs) (*raftpb.InstallSnapshotReply, error) {
	rf.mu.Lock()
	// Snapshot logic to be implemented if needed
	// TODO: IMPLEMENT THIS
	// For now just reject or handle minimally
	reply := &raftpb.InstallSnapshotReply{Term: int64(rf.currentTerm)}
	rf.mu.Unlock()
	return reply, nil
}

// --- Internal Helpers ---

func (rf *Raft) becomeFollower(term int) {
	rf.state = Follower
	if term > rf.currentTerm {
		rf.currentTerm = term
		rf.votedFor = -1
	}
	rf.persist()
}

func (rf *Raft) becomeCandidate() {
	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me
	rf.persist()
}

func (rf *Raft) becomeLeader() {
	rf.state = Leader
	for i := range rf.peers {
		rf.nextIndex[i] = rf.getLastLogIndex() + 1
		rf.matchIndex[i] = 0
	}
	rf.matchIndex[rf.me] = rf.getLastLogIndex()
	log.Printf("[Node %d] Became Leader at term %d", rf.me, rf.currentTerm)
}

func (rf *Raft) getLastLogIndex() int {
	return rf.lastIncludedIndex + len(rf.log) - 1
}

func (rf *Raft) getLastLogTerm() int {
	if len(rf.log) == 0 {
		return 0
	}
	return rf.log[len(rf.log)-1].Term
}

func (rf *Raft) getLogTerm(index int) int {
	if index == rf.lastIncludedIndex {
		return rf.lastIncludedTerm
	}
	idx := index - rf.lastIncludedIndex
	if idx < 0 || idx >= len(rf.log) {
		return -1
	}
	return rf.log[idx].Term
}

func (rf *Raft) findConflictIndex(conflictIndex int, conflictTerm int) int {
	for i := conflictIndex - 1; i >= rf.lastIncludedIndex; i-- {
		if rf.log[i-rf.lastIncludedIndex].Term != conflictTerm {
			break
		}
		conflictIndex = i
	}
	return conflictIndex
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- Senders ---

func (rf *Raft) sendRequestVote(server int, args *raftpb.RequestVoteArgs) (*raftpb.RequestVoteReply, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reply, err := rf.peers[server].RequestVote(ctx, args)
	if err != nil {
		return nil, false
	}
	return reply, true
}

func (rf *Raft) sendAppendEntries(server int, args *raftpb.AppendEntriesArgs) (*raftpb.AppendEntriesReply, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reply, err := rf.peers[server].AppendEntries(ctx, args)
	if err != nil {
		return nil, false
	}
	return reply, true
}

// --- Tickers ---

func (rf *Raft) startElection() {
	args := &raftpb.RequestVoteArgs{
		CandidateId:  int64(rf.me),
		CurrentTerm:  int64(rf.currentTerm),
		LastLogIndex: int64(rf.getLastLogIndex()),
		LastLogTerm:  int64(rf.getLastLogTerm()),
	}

	var votes int32 = 1

	// When there is only one node, it will become leader immediately.
	if votes > int32(len(rf.peers)/2) {
		rf.becomeLeader()
		rf.broadcastHeartbeats()
		return
	}

	for i := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			reply, ok := rf.sendRequestVote(server, args)
			if !ok {
				return
			}
			rf.mu.Lock()
			defer rf.mu.Unlock()

			if reply.Term > int64(rf.currentTerm) {
				rf.becomeFollower(int(reply.Term))
				return
			}

			if rf.state != Candidate || int(args.CurrentTerm) != rf.currentTerm {
				return
			}

			if reply.VoteGranted {
				atomic.AddInt32(&votes, 1)
				if atomic.LoadInt32(&votes) > int32(len(rf.peers)/2) {
					rf.becomeLeader()
					// Immediately send heartbeat
					rf.broadcastHeartbeats()
				}
			}
		}(i)
	}
}

func (rf *Raft) broadcastHeartbeats() {
	for i := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			rf.mu.Lock()
			if rf.state != Leader {
				rf.mu.Unlock()
				return
			}

			nextIdx := rf.nextIndex[server]
			// Simplify: assume no snapshot for now
			// TODO: IMPLEMENT THIS

			entries := make([]*raftpb.LogEntry, 0)
			if nextIdx <= rf.getLastLogIndex() {
				// Convert log entries to protobuf format
				for _, e := range rf.log[nextIdx-rf.lastIncludedIndex:] {
					entries = append(entries, &raftpb.LogEntry{
						Term:    int64(e.Term),
						Command: e.Command,
					})
				}
			}

			prevLogIndex := nextIdx - 1
			prevLogTerm := rf.getLogTerm(prevLogIndex)

			args := &raftpb.AppendEntriesArgs{
				Term:         int64(rf.currentTerm),
				LeaderId:     int64(rf.me),
				PrevLogIndex: int64(prevLogIndex),
				PrevLogTerm:  int64(prevLogTerm),
				Entries:      entries,
				LeaderCommit: int64(rf.commitIndex),
			}
			rf.mu.Unlock()

			reply, ok := rf.sendAppendEntries(server, args)
			if !ok {
				return
			}

			rf.mu.Lock()
			defer rf.mu.Unlock()

			if reply.Term > int64(rf.currentTerm) {
				rf.becomeFollower(int(reply.Term))
				return
			}

			if rf.state != Leader {
				return
			}

			if reply.Success {
				rf.matchIndex[server] = int(args.PrevLogIndex) + len(args.Entries)
				rf.nextIndex[server] = rf.matchIndex[server] + 1
				rf.updateCommitIndex()
			} else {
				// Simple backoff for now, or implement conflict logic
				if reply.ConflictIndex != 0 {
					rf.nextIndex[server] = int(reply.ConflictIndex)
				} else {
					rf.nextIndex[server] = max(1, rf.nextIndex[server]-1)
				}
			}
		}(i)
	}
}

func (rf *Raft) updateCommitIndex() {
	// Check if majority matches
	matchIndexCopy := make([]int, len(rf.matchIndex))
	copy(matchIndexCopy, rf.matchIndex)
	matchIndexCopy[rf.me] = rf.getLastLogIndex()
	sort.Ints(matchIndexCopy)

	N := matchIndexCopy[len(matchIndexCopy)/2]
	if N > rf.commitIndex && rf.getLogTerm(N) == rf.currentTerm {
		rf.commitIndex = N
	}
}

// ticker is a goroutine that periodically checks if the current node is timeout.
// For leader node, it sends heartbeats periodically.
// For not-leader node, it checks the received flag and starts election if timeout.
func (rf *Raft) ticker() {
	for !rf.killed() {
		rf.mu.Lock()
		if rf.state != Leader {
			// For not-leader node, check election timeout
			if !rf.received {
				rf.becomeCandidate()
				rf.startElection()
			}
			rf.received = false // Reset received flag
		} else {
			// For Leader node, sends heartbeats
			rf.broadcastHeartbeats()
		}
		rf.mu.Unlock()

		// Reset random election timeout，150-300ms
		ms := 150 + rand.Intn(150)
		if rf.state == Leader {
			ms = 50 // Heartbeat interval
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

// applier is a goroutine that applies the committed log entries to the state machine.
func (rf *Raft) applier() {
	for !rf.killed() {
		rf.mu.Lock()
		for rf.lastApplied < rf.commitIndex {
			rf.lastApplied++
			msg := ApplyMsg{
				CommandValid: true,
				Command:      rf.log[rf.lastApplied-rf.lastIncludedIndex].Command,
				CommandIndex: rf.lastApplied,
			}
			rf.mu.Unlock()
			rf.applyChan <- msg
			rf.mu.Lock()
		}
		rf.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Factory ---

func Make(peersAddr []string, me int, persister Storage, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = make([]raftpb.RaftServiceClient, len(peersAddr))
	rf.peerConns = make([]*grpc.ClientConn, len(peersAddr))
	rf.persister = persister
	rf.me = me
	rf.applyChan = applyCh
	rf.state = Follower
	rf.votedFor = -1
	rf.nextIndex = make([]int, len(peersAddr))
	rf.matchIndex = make([]int, len(peersAddr))

	// Initialize log with dummy node if needed, or load from persist
	rf.log = []LogEntry{{Term: 0, Command: nil}} // Dummy

	// Read persistent state from persister.
	rf.readPersist(persister.ReadRaftState())
	// If log empty after readPersist (first run), ensure dummy exists
	if len(rf.log) == 0 {
		rf.log = []LogEntry{{Term: 0, Command: nil}}
	}

	// Connect to peers
	for i, addr := range peersAddr {
		if i == me {
			continue
		}

		// Retry connection asynchronously in background, and no need for credentials in development environment.
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Printf("Error creating client for peer %d: %v", i, err)
		}
		rf.peerConns[i] = conn
		rf.peers[i] = raftpb.NewRaftServiceClient(conn)
	}

	go rf.ticker()
	go rf.applier()

	return rf
}

func (rf *Raft) killed() bool {
	return atomic.LoadInt32(&rf.dead) == 1
}

// ====== gRPC Server, only for testing ======

func (rf *Raft) StartServer(port string) error {
	lis, err := net.Listen("tcp", port)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	raftpb.RegisterRaftServiceServer(s, rf)
	rf.grpcServer = s
	log.Printf("Raft node %d listening on %s", rf.me, port)
	return s.Serve(lis)
}

func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	if rf.grpcServer != nil {
		rf.grpcServer.Stop()
	}
	for _, conn := range rf.peerConns {
		if conn != nil {
			conn.Close()
		}
	}
}
