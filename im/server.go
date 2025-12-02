package im

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wangch29/ikun-messenger/api/impb"
	"github.com/Wangch29/ikun-messenger/config"
	"github.com/Wangch29/ikun-messenger/kvraft"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type IMServer struct {
	router   *gin.Engine
	clerk    *kvraft.Clerk
	gateway  *Gateway
	nodeID   int
	nodeAddr string

	impb.UnimplementedIMServiceServer
}

func NewIMServer(nodeID int, clerk *kvraft.Clerk, nodeAddr string) *IMServer {
	s := &IMServer{
		router:   gin.Default(),
		clerk:    clerk,
		nodeID:   nodeID,
		nodeAddr: nodeAddr,
	}

	s.gateway = s.setupGateway()

	s.setupRoutes() // TODO: a separate router.go file?
	return s
}

func (s *IMServer) setupGateway() *Gateway {
	gateway := New()

	gateway.OnMessage = func(from string, msg []byte) {
		var clientMsg ClientMessage
		if err := json.Unmarshal(msg, &clientMsg); err != nil {
			slog.Error("Failed to unmarshal message", "error", err)
			return
		}

		switch clientMsg.Type {
		case "private":
			s.handlePrivateMessage(from, clientMsg)
		case "broadcast":
			s.handleBroadcastMessage(from, clientMsg)
		default:
			slog.Error("Unknown message type", "type", clientMsg.Type)
		}
	}

	gateway.OnConnect = func(userID string) {
		routeKey := "route:" + userID
		s.clerk.Put(routeKey, s.nodeAddr)
		slog.Info("User connected", "user_id", userID, "node_addr", s.nodeAddr)
	}

	gateway.OnDisconnect = func(userID string) {
		routeKey := "route:" + userID
		s.clerk.Put(routeKey, "")
		slog.Info("User disconnected", "user_id", userID)
	}

	return gateway
}

func (s *IMServer) handlePrivateMessage(from string, msg ClientMessage) {
	slog.Info("Handling private message", "from", from, "to", msg.To, "content", msg.Content)

	targetNode := s.clerk.Get("route:" + msg.To)

	slog.Info("Target node", "target_node", targetNode)

	if targetNode == "" {
		slog.Error("Target node not found", "user_id", msg.To)
		return
	}

	if targetNode == s.nodeAddr {
		// Send to the local user.
		slog.Info("Sending private message to local user", "from", from, "to", msg.To, "content", msg.Content)
		s.gateway.SendToUser(msg.To, ServerMessage{
			Type:    "msg",
			From:    from,
			Content: msg.Content,
		})
	} else {
		// Forward to the target node.
		slog.Info("Forwarding private message to target node", "from", from, "to", msg.To, "content", msg.Content)
		go s.forwardPrivateMessage(targetNode, from, msg)
	}
}

func (s *IMServer) forwardPrivateMessage(targetNodeAddr string, from string, msg ClientMessage) {
	// Find target grpc address from config
	var targetGrpcAddr string
	for _, node := range config.Global.Nodes {
		if node.IMHttpAddr() == targetNodeAddr {
			targetGrpcAddr = node.IMGrpcAddr()
			break
		}
	}

	if targetGrpcAddr == "" {
		slog.Error("Target grpc address not found", "http_addr", targetNodeAddr)
		return
	}

	// Connect to the target node.
	// TODO: implement connection pooling.
	conn, err := grpc.NewClient(targetGrpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Error("Failed to connect to peer", "addr", targetGrpcAddr, "err", err)
		return
	}
	defer conn.Close()

	client := impb.NewIMServiceClient(conn)

	// send RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := client.Forward(ctx, &impb.ForwardRequest{
		From:    from,
		To:      msg.To,
		Content: msg.Content,
	})

	if err != nil {
		slog.Error("Forward RPC failed", "err", err)
	} else if !reply.Success {
		slog.Error("Forward failed", "reason", reply.Err)
	} else {
		slog.Info("Message forwarded successfully", "to", targetGrpcAddr)
	}
}

func (s *IMServer) handleBroadcastMessage(from string, msg ClientMessage) {
	// Broadcast to local users.
	s.gateway.BroadcastLocal(
		ServerMessage{
			Type:    "broadcast",
			From:    from,
			Content: msg.Content,
		})

	// Broadcast to other nodes.
	for _, node := range config.Global.Nodes {
		if node.ID == s.nodeID {
			continue
		}
		go s.sendBroadcastToPeer(node.IMGrpcAddr(), from, msg)
	}
}

func (s *IMServer) sendBroadcastToPeer(targetAddr string, from string, msg ClientMessage) {
	// Connect to the target node.
	// TODO: implement connection pooling.
	conn, err := grpc.NewClient(targetAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Error("Failed to connect to peer", "addr", targetAddr, "err", err)
		return
	}
	defer conn.Close()

	client := impb.NewIMServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := client.Broadcast(ctx, &impb.BroadcastRequest{
		From:    from,
		Content: msg.Content,
	})

	if err != nil {
		slog.Error("Broadcast RPC failed", "err", err)
	} else if !reply.Success {
		slog.Error("Broadcast failed", "reason", err)
	} else {
		slog.Info("Message broadcasted successfully", "to", targetAddr)
	}
}

func (s *IMServer) Broadcast(ctx context.Context, req *impb.BroadcastRequest) (*impb.BroadcastReply, error) {
	slog.Info("Receive broadcast message ", "from", req.From)

	s.gateway.BroadcastLocal(ServerMessage{
		Type:    "broadcast",
		From:    req.From,
		Content: req.Content,
	})

	return &impb.BroadcastReply{
		Success: true,
	}, nil
}

func (s *IMServer) setupRoutes() {
	s.router.GET("/ws", s.gateway.HandleWebSocket)

	api := s.router.Group("/api")
	{
		api.POST("/login", s.handleLogin) // TODO: a separate handler.go file?
		api.POST("/logout", s.handleLogout)
		api.POST("/send", s.handleSend)
		api.GET("/online", s.handleGetOnlineUsers)
	}
}

func (s *IMServer) Start(port string) error {
	slog.Info("IM Server started", "node_id", s.nodeID, "port", port)
	return s.router.Run(port)
}

func (s *IMServer) handleLogin(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, LoginResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	if req.UserID == "" {
		c.JSON(http.StatusBadRequest, LoginResponse{
			Success: false,
			Message: "user_id is required",
		})
		return
	}

	routeKey := "route:" + req.UserID
	s.clerk.Put(routeKey, s.nodeAddr)

	slog.Info("Login successful", "user_id", req.UserID, "node_addr", s.nodeAddr)

	c.JSON(http.StatusOK, LoginResponse{
		Success: true,
		Message: "Login successful",
	})
}

func (s *IMServer) handleLogout(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, LoginResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	routeKey := "route:" + req.UserID
	s.clerk.Put(routeKey, "")

	slog.Info("Logout successful", "user_id", req.UserID)

	c.JSON(http.StatusOK, LoginResponse{
		Success: true,
		Message: "Logout successful",
	})
}

func (s *IMServer) handleSend(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "send - not implemented"})
}

func (s *IMServer) handleGetOnlineUsers(c *gin.Context) {
	userID := c.Query("user_id")

	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id is required"})
		return
	}

	routeKey := "route:" + userID
	location := s.clerk.Get(routeKey)

	c.JSON(http.StatusOK, QueryUserResponse{
		UserID:   userID,
		Location: location,
		Online:   location != "",
	})
}

func (s *IMServer) Forward(ctx context.Context, req *impb.ForwardRequest) (*impb.ForwardReply, error) {
	slog.Info("Forward message", "from", req.From, "to", req.To, "content", req.Content)

	ok := s.gateway.SendToUser(req.To, ServerMessage{
		Type:    "msg",
		From:    req.From,
		Content: req.Content,
	})

	if ok {
		return &impb.ForwardReply{
			Success: true,
			Err:     "",
		}, nil
	} else {
		return &impb.ForwardReply{
			Success: false,
			Err:     "User not connected locally",
		}, nil
	}
}
