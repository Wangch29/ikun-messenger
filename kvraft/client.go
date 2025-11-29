package kvraft

import (
	"context"
	"log"
	"time"

	"github.com/Wangch29/IkunMessenger/api/kvpb"
	"github.com/bwmarrin/snowflake"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Clerk struct {
	servers       []string
	leaderId      int
	clientId      int64
	reqId         int64
	snowflakeNode *snowflake.Node
}

func MakeClerk(servers []string, nodeId int64) *Clerk {
	snowflakeNode, _ := snowflake.NewNode(nodeId)
	ck := new(Clerk)
	ck.servers = servers
	ck.leaderId = 0
	ck.clientId = snowflakeNode.Generate().Int64()
	ck.reqId = 0
	return ck
}

// Get calls the Get RPC.
// It tries the current leader, and if that fails (timeout or wrong leader),
// it tries the next server, indefinitely until success.
func (ck *Clerk) Get(key string) string {
	ck.reqId++
	args := &kvpb.GetArgs{
		Key:       key,
		ClientId:  ck.clientId,
		RequestId: ck.reqId,
	}

	for {
		// Try to connect to the current leader
		serverAddr := ck.servers[ck.leaderId]
		conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			// Connection failed locally, try next
			ck.changeLeader()
			continue
		}

		client := kvpb.NewKVServiceClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second) // 1s timeout

		reply, err := client.Get(ctx, args)
		cancel()
		conn.Close()

		if err == nil {
			if reply.Err == "OK" {
				return reply.Value
			} else if reply.Err == "ErrNoKey" {
				return ""
			}
			// If ErrWrongLeader or other application error, retry
		} else {
			// RPC error (timeout, network, etc)
			log.Printf("Clerk.Get error connecting to %s: %v", serverAddr, err)
		}

		// If we got here, something failed. Pick next leader.
		ck.changeLeader()
		time.Sleep(100 * time.Millisecond)
	}
}

// Put calls the Put RPC.
func (ck *Clerk) Put(key string, value string) {
	ck.reqId++
	args := &kvpb.PutArgs{
		Key:       key,
		Value:     value,
		ClientId:  ck.clientId,
		RequestId: ck.reqId,
	}

	for {
		serverAddr := ck.servers[ck.leaderId]
		conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			ck.changeLeader()
			continue
		}

		client := kvpb.NewKVServiceClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)

		reply, err := client.Put(ctx, args)
		cancel()
		conn.Close() // TODO: implement connection pooling

		if err == nil {
			if reply.Err == "OK" {
				return
			}
			// If ErrWrongLeader, retry
		} else {
			log.Printf("Clerk.Put error connecting to %s: %v", serverAddr, err)
		}

		ck.changeLeader()
		time.Sleep(100 * time.Millisecond)
	}
}

func (ck *Clerk) changeLeader() {
	ck.leaderId = (ck.leaderId + 1) % len(ck.servers)
}
