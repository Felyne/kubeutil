package wsterminal

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Felyne/kubeutil/webshell"
)

var upgrader = func() websocket.Upgrader {
	upgrader := websocket.Upgrader{}
	upgrader.ReadBufferSize = 256
	upgrader.WriteBufferSize = 2048
	upgrader.HandshakeTimeout = time.Second * 2
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}
	return upgrader
}()

// TerminalSession implements PtyHandler
type TerminalSession struct {
	wsConn      *websocket.Conn
	sizeChan    chan remotecommand.TerminalSize
	doneChan    chan struct{}
	readTimeout time.Duration
}

// NewTerminalSessionWs create TerminalSession
func NewTerminalSessionWs(conn *websocket.Conn, readTimeout time.Duration) *TerminalSession {
	return &TerminalSession{
		wsConn:      conn,
		sizeChan:    make(chan remotecommand.TerminalSize),
		doneChan:    make(chan struct{}),
		readTimeout: readTimeout,
	}
}

// NewTerminalSession create TerminalSession
func NewTerminalSession(w http.ResponseWriter, r *http.Request, responseHeader http.Header, readTimeout time.Duration) (*TerminalSession, error) {
	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4096)

	session := &TerminalSession{
		wsConn:      conn,
		sizeChan:    make(chan remotecommand.TerminalSize),
		doneChan:    make(chan struct{}),
		readTimeout: readTimeout,
	}

	return session, nil
}

// Done done, must call Done() before connection close, or Next() would not exits.
func (t *TerminalSession) Done() {
	close(t.doneChan)
}

// Next called in a loop from remotecommand as long as the process is running
func (t *TerminalSession) Next() *remotecommand.TerminalSize {
	select {
	case size := <-t.sizeChan:
		return &size
	case <-t.doneChan:
		return nil
	}
}

// Read called in a loop from remotecommand as long as the process is running
func (t *TerminalSession) Read(p []byte) (int, error) {
	if err := t.wsConn.SetReadDeadline(time.Now().Add(t.readTimeout)); err != nil {
		return 0, err
	}
	// it will block
	_, message, err := t.wsConn.ReadMessage()
	if err != nil {
		// nginx做代理的话，如果没有数据流动，默认60s就关闭与后端的连接
		//if websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
		//}
		log.Printf("read message err: %v", err)
		return copy(p, webshell.EndOfTransmission), err
	}
	var msg webshell.TerminalMessage
	if err := json.Unmarshal([]byte(message), &msg); err != nil {
		log.Printf("read parse message err: %v", err)
		// return 0, nil
		return copy(p, webshell.EndOfTransmission), err
	}
	switch msg.Operation {
	case "stdin":
		return copy(p, msg.Data), nil
	case "resize":
		t.sizeChan <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}
		return 0, nil
	case "ping":
		return 0, nil
	default:
		log.Printf("unknown message type '%s'", msg.Operation)
		// return 0, nil
		return copy(p, webshell.EndOfTransmission), fmt.Errorf("unknown message type '%s'", msg.Operation)
	}
}

// Write called from remotecommand whenever there is any output
func (t *TerminalSession) Write(p []byte) (int, error) {
	content := removeInvalidUTF8(string(p))
	msg, err := json.Marshal(webshell.TerminalMessage{
		Operation: "stdout",
		Data:      content,
	})
	if err != nil {
		log.Printf("write parse message err: %v", err)
		return 0, err
	}
	if err := t.wsConn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Printf("write message err: %v", err)
		return 0, err
	}
	return len(p), nil
}

// Close close session
func (t *TerminalSession) Close() error {
	return t.wsConn.Close()
}

func removeInvalidUTF8(str string) string {
	if utf8.ValidString(str) {
		return str
	}
	resRune := make([]rune, 0, len(str))
	for index, _rune := range str {
		if _rune == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(str[index:])
			if size == 1 {
				continue
			}
		}
		resRune = append(resRune, _rune)
	}
	return string(resRune)
}
