package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Wangch29/IkunMessenger/kvraft"
	"github.com/Wangch29/IkunMessenger/raft"
)

// Raft nodes addresses
var raftPeers = []string{
	"127.0.0.1:25000",
	"127.0.0.1:25001",
	"127.0.0.1:25002",
}

// KV Server addresses (for Clerk to connect to)
var kvPeers = []string{
	"127.0.0.1:6000",
	"127.0.0.1:6001",
	"127.0.0.1:6002",
}

func main() {
	var me int
	flag.IntVar(&me, "me", 0, "the id of the node (0, 1, 2)")
	flag.Parse()

	if me < 0 || me >= len(raftPeers) {
		log.Fatalf("Invalid me: %d", me)
	}

	// 1. Start Raft
	applyCh := make(chan raft.ApplyMsg)
	rf := raft.Make(raftPeers, me, raft.NewMemoryStorage(), applyCh)

	// Start Raft gRPC server
	go func() {
		// Parse port from address
		parts := strings.Split(raftPeers[me], ":")
		if len(parts) != 2 {
			log.Fatalf("Invalid raft address: %s", raftPeers[me])
		}
		err := rf.StartServer(":" + parts[1])
		if err != nil {
			log.Fatalf("Raft server failed: %v", err)
		}
	}()

	// 2. Start KV Server
	kv := kvraft.NewKVServer(me, rf, applyCh, 1000)
	go func() {
		parts := strings.Split(kvPeers[me], ":")
		if len(parts) != 2 {
			log.Fatalf("Invalid kv address: %s", kvPeers[me])
		}
		err := kv.StartKVServer(":" + parts[1])
		if err != nil {
			log.Fatalf("KV server failed: %v", err)
		}
	}()

	log.Printf("Node %d started. Raft: %s, KV: %s", me, raftPeers[me], kvPeers[me])

	// 3. Create Clerk
	ck := kvraft.MakeClerk(kvPeers, int64(me))

	// 4. REPL
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		parts := strings.Split(input, " ")

		switch parts[0] {
		case "put":
			if len(parts) != 3 {
				fmt.Println("Usage: put <key> <value>")
				continue
			}
			key := parts[1]
			value := parts[2]
			ck.Put(key, value)
			fmt.Println("Put OK")

		case "get":
			if len(parts) != 2 {
				fmt.Println("Usage: get <key>")
				continue
			}
			key := parts[1]
			value := ck.Get(key)
			fmt.Printf("Value: %s\n", value)

		case "exit":
			os.Exit(0)

		default:
			fmt.Println("Unknown command. Available: put, get, exit")
		}
	}
}
