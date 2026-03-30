package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

var (
	ifaceName  = flag.String("interface", "eth0", "Network interface")
	sourceIP   = flag.String("source-ip", "", "Source pod IP to intercept")
	targetIP   = flag.String("target-ip", "", "Target node IP for redirection")
	targetPort = flag.Int("target-port", 50052, "Target sidecar port")
	appPort    = flag.Int("app-port", 80, "Application port")
	remove     = flag.Bool("remove", false, "Remove the iptables rules")
	debug      = flag.Bool("debug", false, "Enable debug output")
)

type PacketInterceptor struct {
	sourceIP    string
	targetIP    string
	targetPort  int
	appPort     int
	running     bool
	listener    net.Listener
	connTracker map[string]*net.TCPConn
	mu          sync.Mutex
}

func NewPacketInterceptor(sourceIP, targetIP string, targetPort, appPort int) *PacketInterceptor {
	return &PacketInterceptor{
		sourceIP:    sourceIP,
		targetIP:    targetIP,
		targetPort:  targetPort,
		appPort:     appPort,
		running:     true,
		connTracker: make(map[string]*net.TCPConn),
	}
}

func (p *PacketInterceptor) SetupIPTables() error {
	if p.sourceIP == "" || p.targetIP == "" {
		return fmt.Errorf("source-ip and target-ip required")
	}

	script := fmt.Sprintf(`
# Clear existing rules for this source IP
iptables -t nat -D PREROUTING -p tcp -d %s --dport %d -j REDIRECT --to-port %d 2>/dev/null || true
iptables -t nat -D OUTPUT -p tcp -d %s --dport %d -j REDIRECT --to-port %d 2>/dev/null || true

# Redirect incoming traffic to our intercept port
iptables -t nat -A PREROUTING -p tcp -d %s --dport %d -j REDIRECT --to-port %d
iptables -t nat -A OUTPUT -p tcp -d %s --dport %d -j REDIRECT --to-port %d
`,
		p.sourceIP, p.appPort, p.targetPort,
		p.sourceIP, p.appPort, p.targetPort,
		p.sourceIP, p.appPort, p.targetPort,
		p.sourceIP, p.appPort, p.targetPort)

	if *debug {
		log.Printf("Setting up iptables rules:\n%s", script)
	}

	return runShell(script)
}

func (p *PacketInterceptor) RemoveIPTables() error {
	script := fmt.Sprintf(`
iptables -t nat -D PREROUTING -p tcp -d %s --dport %d -j REDIRECT --to-port %d 2>/dev/null || true
iptables -t nat -D OUTPUT -p tcp -d %s --dport %d -j REDIRECT --to-port %d 2>/dev/null || true
`, p.sourceIP, p.appPort, p.targetPort,
		p.sourceIP, p.appPort, p.targetPort)

	return runShell(script)
}

func (p *PacketInterceptor) Start() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p.targetPort))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", p.targetPort, err)
	}
	p.listener = ln

	log.Printf("Packet interceptor started, listening on port %d", p.targetPort)
	log.Printf("Intercepting traffic to %s, forwarding to %s:%d",
		p.sourceIP, p.targetIP, p.targetPort)

	go func() {
		for p.running {
			conn, err := ln.Accept()
			if err != nil {
				if p.running {
					continue
				}
				break
			}
			go p.handleConnection(conn)
		}
	}()

	return nil
}

func (p *PacketInterceptor) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	targetAddr := fmt.Sprintf("%s:%d", p.targetIP, p.targetPort)
	targetConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		if *debug {
			log.Printf("Failed to connect to target %s: %v", targetAddr, err)
		}
		return
	}
	defer targetConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		n, _ := io.Copy(targetConn, clientConn)
		if *debug {
			log.Printf("Forwarded %d bytes client->target", n)
		}
		targetConn.Close()
	}()

	go func() {
		defer wg.Done()
		n, _ := io.Copy(clientConn, targetConn)
		if *debug {
			log.Printf("Forwarded %d bytes target->client", n)
		}
		clientConn.Close()
	}()

	wg.Wait()
}

func (p *PacketInterceptor) Stop() error {
	p.running = false

	if p.listener != nil {
		p.listener.Close()
	}

	p.mu.Lock()
	for _, conn := range p.connTracker {
		conn.Close()
	}
	p.mu.Unlock()

	return p.RemoveIPTables()
}

func runShell(script string) error {
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	flag.Parse()

	if *sourceIP == "" || *targetIP == "" {
		fmt.Println("Usage: interceptor --source-ip <IP> --target-ip <IP> [--app-port <port>] [--target-port <port>] [--remove] [--debug]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	interceptor := NewPacketInterceptor(*sourceIP, *targetIP, *targetPort, *appPort)

	if *remove {
		if err := interceptor.RemoveIPTables(); err != nil {
			log.Printf("Warning: %v", err)
		}
		log.Println("IPTables rules removed")
		return
	}

	if err := interceptor.SetupIPTables(); err != nil {
		log.Fatalf("Failed to setup iptables: %v", err)
	}

	if err := interceptor.Start(); err != nil {
		log.Fatalf("Failed to start interceptor: %v", err)
	}

	log.Println("Packet interceptor running. Press Ctrl+C to stop...")

	ch := make(chan os.Signal, 1)
	<-ch

	log.Println("Shutting down...")
	interceptor.Stop()
}

func parseIPPort(s string) (string, int, error) {
	parts := make([]string, 0)
	current := ""
	for _, c := range s {
		if c == ':' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	parts = append(parts, current)

	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address: %s", s)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, err
	}
	return parts[0], port, nil
}
