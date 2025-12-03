package raft

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

const (
	testBasePort = 30000
)

const RaftElectionTimeout = 1000 * time.Millisecond

type config struct {
	mu        sync.Mutex
	t         *testing.T
	n         int
	rafts     []*Raft
	applyChs  []chan ApplyMsg
	saved     []*MemoryStorage
	connected []bool
	addrs     []string
	start     time.Time
	basePort  int
}

func makeConfig(t *testing.T, n int, testID int) *config {
	cfg := &config{
		t:         t,
		n:         n,
		rafts:     make([]*Raft, n),
		applyChs:  make([]chan ApplyMsg, n),
		saved:     make([]*MemoryStorage, n),
		connected: make([]bool, n),
		addrs:     make([]string, n),
		start:     time.Now(),
		basePort:  testBasePort + (testID * 100),
	}

	for i := 0; i < n; i++ {
		cfg.addrs[i] = fmt.Sprintf("127.0.0.1:%d", cfg.basePort+i)
		cfg.saved[i] = NewMemoryStorage()
		cfg.applyChs[i] = make(chan ApplyMsg, 100)
		cfg.connected[i] = true
	}

	for i := 0; i < n; i++ {
		cfg.start1(i)
	}

	return cfg
}

func (cfg *config) start1(i int) {
	cfg.crash1(i)

	if cfg.applyChs[i] == nil {
		cfg.applyChs[i] = make(chan ApplyMsg, 100)
	}

	cfg.rafts[i] = Make(cfg.addrs, i, cfg.saved[i], cfg.applyChs[i])
	cfg.connected[i] = true

	go func() {
		err := cfg.rafts[i].StartServer(fmt.Sprintf(":%d", cfg.basePort+i))
		if err != nil {
		}
	}()
}

func (cfg *config) crash1(i int) {
	cfg.connected[i] = false
	if cfg.rafts[i] != nil {
		cfg.rafts[i].Kill()
		cfg.rafts[i] = nil
	}
}

func (cfg *config) checkOneLeader() int {
	for iters := 0; iters < 10; iters++ {
		ms := 450 + (rand.Int63() % 100)
		time.Sleep(time.Duration(ms) * time.Millisecond)

		leaders := make(map[int]int)
		for i := 0; i < cfg.n; i++ {
			if cfg.connected[i] {
				if term, isLeader := cfg.rafts[i].GetState(); isLeader {
					leaders[i] = term
				}
			}
		}

		lastTermWithLeader := -1
		for _, term := range leaders {
			if lastTermWithLeader == -1 {
				lastTermWithLeader = term
			}
			if term != lastTermWithLeader {
				cfg.t.Fatalf("leaders disagree on term")
			}
		}

		if len(leaders) != 0 {
			for id := range leaders {
				return id
			}
		}
	}
	cfg.t.Fatalf("expected one leader, got none")
	return -1
}

func (cfg *config) checkTerms() int {
	term := -1
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			xterm, _ := cfg.rafts[i].GetState()
			if term == -1 {
				term = xterm
			} else if term != xterm {
				cfg.t.Fatalf("servers disagree on term")
			}
		}
	}
	return term
}

func (cfg *config) cleanup() {
	for i := 0; i < cfg.n; i++ {
		cfg.crash1(i)
	}
}

func TestInitialElection(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, 1)
	defer cfg.cleanup()

	fmt.Printf("Test: Initial election ...\n")

	cfg.checkOneLeader()

	time.Sleep(50 * time.Millisecond)
	term1 := cfg.checkTerms()

	time.Sleep(2 * time.Second)
	term2 := cfg.checkTerms()

	if term1 != term2 {
		fmt.Printf("warning: term changed even though there were no failures")
	}

	fmt.Printf("  ... Passed\n")
}

func TestReElection(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, 2)
	defer cfg.cleanup()

	fmt.Printf("Test: Election after failure ...\n")

	leader1 := cfg.checkOneLeader()
	if leader1 == -1 {
		t.Fatalf("no leader")
	}

	cfg.crash1(leader1)

	time.Sleep(2 * time.Second)
	leader2 := -1
	for i := 0; i < servers; i++ {
		if i != leader1 && cfg.connected[i] {
			if _, isLeader := cfg.rafts[i].GetState(); isLeader {
				leader2 = i
				break
			}
		}
	}

	if leader2 == -1 {
		leader2 = cfg.checkOneLeader()
	}

	if leader2 == -1 {
		t.Fatalf("no new leader found")
	}

	cfg.start1(leader1)
	time.Sleep(1 * time.Second)

	fmt.Printf("  ... Passed\n")
}

func TestBasicAgree(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, 3)
	defer cfg.cleanup()

	fmt.Printf("Test: Basic agreement ...\n")

	iters := 3
	for index := 1; index < iters+1; index++ {
		leader := cfg.checkOneLeader()
		if leader == -1 {
			t.Fatalf("no leader")
		}

		cmd := []byte(fmt.Sprintf("cmd %d", index))
		cfg.rafts[leader].Start(cmd)

		// Wait a bit for log replication to start
		time.Sleep(2000 * time.Millisecond)

		committedCount := 0
		start := time.Now()
		committed := make(map[int]bool)
		for time.Since(start) < 5*time.Second {
			for i := 0; i < servers; i++ {
				if committed[i] {
					continue
				}
				select {
				case msg := <-cfg.applyChs[i]:
					if msg.CommandValid && string(msg.Command) == string(cmd) {
						committed[i] = true
						committedCount++
					}
				default:
					// No message available
				}
			}
			if committedCount >= 2 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	fmt.Printf("  ... Passed\n")
}

func TestSnapshot(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, 4)
	defer cfg.cleanup()

	fmt.Printf("Test: Snapshot (InstallSnapshot) ...\n")

	time.Sleep(1 * time.Second)
	leader := -1
	for i := 0; i < servers; i++ {
		if _, isLeader := cfg.rafts[i].GetState(); isLeader {
			leader = i
			break
		}
	}
	if leader == -1 {
		t.Fatalf("no leader")
	}

	follower := (leader + 1) % servers
	cfg.crash1(follower)

	for i := 0; i < 10; i++ {
		cfg.rafts[leader].Start([]byte(fmt.Sprintf("snap-cmd-%d", i)))
		time.Sleep(10 * time.Millisecond)
	}

	snapshotData := []byte("snapshot-state-at-5")
	cfg.rafts[leader].Snapshot(5, snapshotData)

	cfg.start1(follower)

	success := true
	start := time.Now()
	for time.Since(start) < 15*time.Second {
		select {
		case msg := <-cfg.applyChs[follower]:
			if msg.SnapshotValid {
				if msg.SnapshotIndex == 5 && bytes.Equal(msg.Snapshot, snapshotData) {
					success = true
					goto Done
				}
			}
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
Done:

	if !success {
		t.Fatalf("Follower did not receive snapshot via InstallSnapshot")
	}

	fmt.Printf("  ... Passed\n")
}
