// Spike 2: PTY inside Docker container via OrbStack
// Tests: spawn a shell inside a Docker container, attach to it with PTY, stream over websocket

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var dockerClient *client.Client

func main() {
	var err error
	dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer dockerClient.Close()

	// Verify Docker connection
	ctx := context.Background()
	info, err := dockerClient.Info(ctx)
	if err != nil {
		log.Fatalf("Failed to connect to Docker: %v", err)
	}
	fmt.Printf("Connected to Docker: %s\n", info.Name)

	http.HandleFunc("/", serveHTML)
	http.HandleFunc("/ws", handleWebSocket)

	fmt.Println("Starting server on :8081")
	fmt.Println("Open http://localhost:8081 in browser")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func serveHTML(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html>
<head>
    <title>Docker PTY Spike</title>
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
    <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
</head>
<body>
    <h1>Docker PTY Spike (OrbStack)</h1>
    <p>This spawns a shell inside an Alpine container</p>
    <div id="terminal" style="width: 800px; height: 400px;"></div>
    <script>
        const term = new Terminal();
        const fitAddon = new FitAddon.FitAddon();
        term.loadAddon(fitAddon);
        term.open(document.getElementById('terminal'));
        fitAddon.fit();

        const ws = new WebSocket('ws://localhost:8081/ws');
        
        ws.onopen = () => {
            console.log('WebSocket connected');
            term.write('Connecting to Docker container...\r\n');
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

	ctx := context.Background()

	// Create container
	resp, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image:        "alpine:latest",
		Cmd:          []string{"/bin/sh"},
		Tty:          true,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}, nil, nil, nil, "")
	if err != nil {
		log.Printf("Failed to create container: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Failed to create container: %v\r\n", err)))
		return
	}
	containerID := resp.ID
	log.Printf("Created container: %s", containerID[:12])

	// Start container
	if err := dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		log.Printf("Failed to start container: %v", err)
		dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		return
	}
	log.Printf("Started container: %s", containerID[:12])

	// Cleanup on exit
	defer func() {
		log.Printf("Cleaning up container: %s", containerID[:12])
		dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
		dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	}()

	// Attach to container
	attachResp, err := dockerClient.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		log.Printf("Failed to attach to container: %v", err)
		return
	}
	defer attachResp.Close()

	// Container -> WebSocket
	go func() {
		// For TTY, output is raw (no multiplexing)
		buf := make([]byte, 1024)
		for {
			n, err := attachResp.Reader.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("Container read error: %v", err)
				}
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		}
	}()

	// WebSocket -> Container
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			return
		}
		if _, err := attachResp.Conn.Write(msg); err != nil {
			log.Printf("Container write error: %v", err)
			return
		}
	}
}


