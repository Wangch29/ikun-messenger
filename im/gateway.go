package im

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Allow cross-origin WebSocket.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Gateway struct {
	// userID -> *websocket.Conn
	conns map[string]*websocket.Conn
	mu    sync.RWMutex

	// Callback functions.
	OnMessage    func(from string, msg []byte)
	OnConnect    func(userID string)
	OnDisconnect func(userID string)
	AuthFunc     func(userID string) (bool, string)
}

func New() *Gateway {
	return &Gateway{
		conns: make(map[string]*websocket.Conn),
	}
}

// HandleWebSocket Handle WebSocket requests.
// Path: /ws?user_id=abc
func (g *Gateway) HandleWebSocket(c *gin.Context) {
	userID := c.Query("user_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id required"})
		return
	}
	if g.AuthFunc != nil {
		ok, reason := g.AuthFunc(userID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": reason})
			return
		}
	}

	// Upgrade HTTP to WebSocket.
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Info("Failed to upgrade WS", "error", err)
		return
	}

	g.addConn(userID, conn)
	defer g.removeConn(userID)

	// Keep reading messages from the client.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}

		if g.OnMessage != nil {
			go g.OnMessage(userID, data)
		}
	}
}

// SendToUser send message to the user.
func (g *Gateway) SendToUser(userID string, msg interface{}) bool {
	g.mu.RLock()
	conn, ok := g.conns[userID]
	g.mu.RUnlock()

	if !ok {
		return false
	}

	// TODO: Note that WriteJSON is not concurrent safe.
	// Need to add lock if concurrent writing is needed.
	err := conn.WriteJSON(msg)
	if err != nil {
		slog.Info("Failed to send to user", "user_id", userID, "error", err)
		conn.Close()
		return false
	}
	return true
}

// BroadcastLocal broadcast message to all local connections.
func (g *Gateway) BroadcastLocal(msg interface{}) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for uid, conn := range g.conns {
		err := conn.WriteJSON(msg)
		if err != nil {
			slog.Error("Failed to broadcast to user", "user_id", uid, "error", err)
			conn.Close()
		}
	}
}

func (g *Gateway) addConn(userID string, conn *websocket.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if old, ok := g.conns[userID]; ok {
		old.Close()
	}
	g.conns[userID] = conn

	// Trigger the on connect callback.
	if g.OnConnect != nil {
		go g.OnConnect(userID)
	}

	slog.Info("User connected", "user_id", userID)
}

func (g *Gateway) removeConn(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if conn, ok := g.conns[userID]; ok {
		conn.Close()
		delete(g.conns, userID)

		// Trigger the on disconnect callback.
		if g.OnDisconnect != nil {
			go g.OnDisconnect(userID)
		}
	}

	slog.Info("User disconnected", "user_id", userID)
}
