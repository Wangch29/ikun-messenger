package cmd

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/Wangch29/ikun-messenger/api/impb"
	"github.com/Wangch29/ikun-messenger/config"
	"github.com/Wangch29/ikun-messenger/im"
	"github.com/Wangch29/ikun-messenger/kvraft"
	"github.com/Wangch29/ikun-messenger/raft"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var serverMe int

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the ikun messenger server",
	Long:  "Start the ikun messenger server",
	Run:   runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.Flags().IntVarP(&serverMe, "me", "m", 0, "Node ID (0, 1, 2)")
}

func runServer(cmd *cobra.Command, args []string) {
	if len(config.Global.Nodes) == 0 {
		slog.Error("No nodes found in config")
		return
	}

	if serverMe < 0 || serverMe >= len(config.Global.Nodes) {
		slog.Error("Invalid node ID", "id", serverMe, "total_nodes", len(config.Global.Nodes))
		return
	}

	var raftPeers []string
	var kvPeers []string
	for _, node := range config.Global.Nodes {
		raftPeers = append(raftPeers, node.RaftAddr())
		kvPeers = append(kvPeers, node.KVAddr())
	}

	myConfig := config.Global.Nodes[serverMe]

	applyCh := make(chan raft.ApplyMsg)
	rf := raft.Make(raftPeers, serverMe, raft.NewMemoryStorage(), applyCh)

	// Start Raft Server
	go func() {
		if err := rf.StartServer(fmt.Sprintf(":%d", myConfig.RaftPort)); err != nil {
			slog.Error("Failed to start raft server", "err", err)
			return
		}
	}()

	// Start KV Server
	kv := kvraft.NewKVServer(serverMe, rf, applyCh, 1000)
	go func() {
		if err := kv.StartKVServer(fmt.Sprintf(":%d", myConfig.KVPort)); err != nil {
			slog.Error("Failed to start kv server", "err", err)
			return
		}
	}()

	// Start IM gRPC Server
	ck := kvraft.MakeClerk(kvPeers, int64(serverMe))
	imServer := im.NewIMServer(serverMe, ck, myConfig.IMHttpAddr())

	go func() {
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", myConfig.IMGrpcPort))
		if err != nil {
			slog.Error("Failed to listen", "err", err)
			return
		}
		slog.Info("IM gRPC Server listening", "addr", myConfig.IMGrpcAddr())
		s := grpc.NewServer()
		impb.RegisterIMServiceServer(s, imServer)
		if err := s.Serve(lis); err != nil {
			slog.Error("Failed to serve", "err", err)
		}
	}()

	slog.Info("Node started",
		"id", serverMe,
		"raft_addr", myConfig.RaftAddr(),
		"kv_addr", myConfig.KVAddr(),
		"im_http", myConfig.IMHttpAddr(),
		"im_grpc", myConfig.IMGrpcAddr(),
	)

	// Start IM HTTP Server (Blocking)
	if err := imServer.Start(fmt.Sprintf(":%d", myConfig.IMHttpPort)); err != nil {
		slog.Error("Failed to start im server", "err", err)
		return
	}
}
