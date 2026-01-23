// Spike 1: PTY -> WebSocket -> xterm.js
// Tests: spawn a process in a pty, stream output over websocket, render in xterm.js

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	http.HandleFunc("/", serveHTML)
	http.HandleFunc("/ws", handleWebSocket)

	fmt.Println("Starting server on :8080")
	fmt.Println("Open http://localhost:8080 in browser")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func serveHTML(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html>
<head>
    <title>PTY WebSocket Spike</title>
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
    <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
</head>
<body>
    <h1>PTY WebSocket Spike</h1>
    <div id="terminal" style="width: 800px; height: 400px;"></div>
    <script>
        const term = new Terminal();
        const fitAddon = new FitAddon.FitAddon();
        term.loadAddon(fitAddon);
        term.open(document.getElementById('terminal'));
        fitAddon.fit();

        const ws = new WebSocket('ws://localhost:8080/ws');
        
        ws.onopen = () => {
            console.log('WebSocket connected');
            term.write('Connected to PTY\r\n');
        };
        
        ws.onmessage = (event) => {
            term.write(event.data);
        };
        
        ws.onerror = (error) => {
            console.error('WebSocket error:', error);
            term.write('\r\nWebSocket error\r\n');
        };
        
        ws.onclose = () => {
            term.write('\r\nConnection closed\r\n');
        };

        // Send input to PTY
        term.onData((data) => {
            ws.send(data);
        });
    </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Start a shell in a PTY
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	
	cmd := exec.Command(shell)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("Failed to start PTY: %v", err)
		return
	}
	defer ptmx.Close()

	// PTY -> WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				log.Printf("PTY read error: %v", err)
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		}
	}()

	// WebSocket -> PTY
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			return
		}
		if _, err := ptmx.Write(msg); err != nil {
			log.Printf("PTY write error: %v", err)
			return
		}
	}
}
