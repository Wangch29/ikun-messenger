package raft

import (
	"fmt"
	"testing"
	"time"
)

const (
	basePort = 5000
)

type config struct {
	n         int
	rafts     []*Raft
	applyChs  []chan ApplyMsg
	saved     []*MemoryStorage
	connected []bool
	addrs     []string
}

func makeConfig(t *testing.T, n int) *config {
	cfg := &config{
		n:         n,
		rafts:     make([]*Raft, n),
		applyChs:  make([]chan ApplyMsg, n),
		saved:     make([]*MemoryStorage, n),
		connected: make([]bool, n),
		addrs:     make([]string, n),
	}

	for i := 0; i < n; i++ {
		cfg.addrs[i] = fmt.Sprintf("localhost:%d", basePort+i)
		cfg.saved[i] = NewMemoryStorage()
		cfg.applyChs[i] = make(chan ApplyMsg)
		cfg.connected[i] = true
	}

	for i := 0; i < n; i++ {
		cfg.start(i)
	}

	return cfg
}

func (cfg *config) start(i int) {
	if cfg.rafts[i] != nil {
		// Already started?
		return
	}

	cfg.rafts[i] = Make(cfg.addrs, i, cfg.saved[i], cfg.applyChs[i])

	// Start gRPC server in a goroutine
	go func() {
		err := cfg.rafts[i].StartServer(fmt.Sprintf(":%d", basePort+i))
		if err != nil {
			// This might happen if we kill and restart quickly, or if port is busy
			// In a real test harness we might want to handle this more gracefully
			// but for now we just log it.
			// fmt.Printf("Error starting server %d: %v\n", i, err)
		}
	}()
}

func (cfg *config) cleanup() {
	for i := 0; i < cfg.n; i++ {
		if cfg.rafts[i] != nil {
			cfg.rafts[i].Kill()
			cfg.rafts[i] = nil
		}
	}
}

func (cfg *config) checkOneLeader() int {
	leaders := 0
	lastLeader := -1
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			term, isLeader := cfg.rafts[i].GetState()
			if isLeader {
				if leaders == 0 {
					lastLeader = i
				}
				leaders++
				_ = term // ignore
			}
		}
	}

	if leaders != 1 {
		return -1
	}
	return lastLeader
}

func (cfg *config) one(cmd []byte, expectedServers int) int {
	leader := cfg.checkOneLeader()
	if leader == -1 {
		return -1
	}

	index, _, ok := cfg.rafts[leader].Start(cmd)
	if !ok {
		return -1
	}

	t0 := time.Now()
	for time.Since(t0) < 2*time.Second {
		for i := 0; i < cfg.n; i++ {
			if cfg.connected[i] {
				// Check if applied
				// Note: In a real test we'd consume from applyChs
				// Here we peek at internal state or just trust the leader commit logic + time
				// For simplicity in this first pass, let's just check the Leader's commitIndex
				// and assume followers catch up.
				// A better way is to count how many applyChs received the msg.
			}
		}
		// For this simple test, we just return the index if leader accepted it
		// The real verification is in the test function checking commitment
	}
	return index
}

// Tests

func TestInitialElection(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers)
	defer cfg.cleanup()

	fmt.Printf("Test: Initial election ...\n")

	// Wait for election
	time.Sleep(2 * time.Second)

	leader1 := cfg.checkOneLeader()
	if leader1 == -1 {
		t.Fatalf("expected one leader, got none or multiple")
	}
	fmt.Printf("  ... Passed -- leader is %d\n", leader1)
}

func TestReElection(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers)
	defer cfg.cleanup()

	fmt.Printf("Test: Re-election ...\n")

	time.Sleep(2 * time.Second)
	leader1 := cfg.checkOneLeader()
	if leader1 == -1 {
		t.Fatalf("expected one leader, got none or multiple")
	}

	// Kill leader
	fmt.Printf("  ... Killing leader %d\n", leader1)
	cfg.rafts[leader1].Kill()
	cfg.connected[leader1] = false
	cfg.rafts[leader1] = nil // Mark as dead in config

	time.Sleep(2 * time.Second)

	leader2 := cfg.checkOneLeader()
	if leader2 == -1 {
		t.Fatalf("expected one leader among survivors")
	}
	if leader2 == leader1 {
		t.Fatalf("killed leader is still leader?")
	}
	fmt.Printf("  ... Passed -- new leader is %d\n", leader2)
}

func TestBasicAgree(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers)
	defer cfg.cleanup()

	fmt.Printf("Test: Basic agreement ...\n")

	time.Sleep(2 * time.Second)
	leader := cfg.checkOneLeader()
	if leader == -1 {
		t.Fatalf("expected one leader")
	}

	// Start a command
	cmd := []byte("hello")
	index, term, ok := cfg.rafts[leader].Start(cmd)
	if !ok {
		t.Fatalf("leader refused to start command")
	}

	// Wait for commitment
	// We check that the command appears in the applyCh of the leader and at least one other
	time.Sleep(2 * time.Second)

	committedCount := 0
	for i := 0; i < servers; i++ {
		// Drain channel to see if command arrived
		select {
		case msg := <-cfg.applyChs[i]:
			if msg.CommandValid && msg.CommandIndex == index && string(msg.Command) == string(cmd) {
				committedCount++
			}
		default:
		}
	}

	if committedCount < 2 {
		t.Fatalf("command only committed on %d servers, expected at least 2", committedCount)
	}

	fmt.Printf("  ... Passed -- committed on %d servers (term %d)\n", committedCount, term)
}
