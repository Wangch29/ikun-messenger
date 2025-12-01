package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Wangch29/ikun-messenger/im"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var (
	clientServerAddr string
	clientUserId     string
)

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Start the ikun messenger client",
	Run:   runClient,
}

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.Flags().StringVarP(&clientServerAddr, "server", "s", "localhost:8080", "Server address (e.g., localhost:8080)")
	clientCmd.Flags().StringVarP(&clientUserId, "user", "u", "", "User Name (required)")
	clientCmd.MarkFlagRequired("user")
}

func runClient(cmd *cobra.Command, args []string) {
	u := url.URL{
		Scheme:   "ws",
		Host:     clientServerAddr,
		Path:     "/ws",
		RawQuery: "user_id=" + clientUserId,
	}
	slog.Info("Connecting to ", "url", u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		slog.Error("Dial error", "error", err)
		return
	}
	defer conn.Close()

	fmt.Println("Connected! Type your message and press Enter.")
	fmt.Println("Commands:")
	fmt.Println("  <msg>           - Broadcast message")
	fmt.Println("  @<user> <msg>   - Private message")
	fmt.Println("  exit            - Quit")

	done := make(chan struct{})

	// Receive Loop
	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				slog.Error("Read error", "error", err)
				return
			}

			var serverMsg im.ServerMessage
			if err := json.Unmarshal(message, &serverMsg); err != nil {
				// If not JSON, print raw
				fmt.Printf("\n[Raw]: %s\n> ", string(message))
				continue
			}

			// Pretty print
			timestamp := time.Now().Format("15:04:05")
			switch serverMsg.Type {
			case "broadcast":
				fmt.Printf("\n[%s] [Broadcast] %s: %s\n> ", timestamp, serverMsg.From, serverMsg.Content)
			case "msg":
				fmt.Printf("\n[%s] [Private] %s: %s\n> ", timestamp, serverMsg.From, serverMsg.Content)
			default:
				fmt.Printf("\n[%s] [%s] %s: %s\n> ", timestamp, serverMsg.Type, serverMsg.From, serverMsg.Content)
			}
		}
	}()

	// Send Loop
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")

	// Handle Ctrl+C
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	go func() {
		for scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				fmt.Print("> ")
				continue
			}

			if text == "exit" {
				close(done)
				return
			}

			var clientMsg im.ClientMessage

			// Parse command
			if strings.HasPrefix(text, "@") {
				// Private message
				parts := strings.SplitN(text, " ", 2)
				if len(parts) < 2 {
					slog.Info("Invalid format. Usage: @<user> <msg>")
					fmt.Print("> ")
					continue
				}
				targetUser := strings.TrimPrefix(parts[0], "@")
				content := parts[1]

				clientMsg = im.ClientMessage{
					Type:    "private",
					To:      targetUser,
					Content: content,
				}
			} else {
				// Broadcast
				clientMsg = im.ClientMessage{
					Type:    "broadcast",
					Content: text,
				}
			}

			// Send
			err := conn.WriteJSON(clientMsg)
			if err != nil {
				slog.Error("Write error", "error", err)
				return
			}
			fmt.Print("> ")
		}
	}()

	// Wait for exit signal
	select {
	case <-done:
	case <-interrupt:
		slog.Info("Interrupt received, closing connection...")
		// Send Close Message
		err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		if err != nil {
			slog.Error("Write close error", "error", err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}
