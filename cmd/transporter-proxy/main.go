package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	mode             = flag.String("mode", "buffer", "Mode: buffer, tap, passthrough")
	targetIP         = flag.String("target-ip", "", "Target node IP for tap mode")
	appPort          = flag.Int("app-port", 80, "Application port")
	managementPort   = flag.Int("management-port", 50053, "Management port for control plane")
	targetPort       = flag.Int("target-port", 50052, "Target port for incoming connections")
	sourcePodIP      = flag.String("source-pod-ip", "", "Source pod IP for tap mode")
	targetNodeIP     = flag.String("target-node-ip", "", "Target node IP for tap mode")
	bufferSize       = flag.Int("buffer-size", 64*1024*1024, "Maximum buffer size in bytes")
	handshakeTimeout = flag.Int("handshake-timeout", 5000, "Handshake timeout in milliseconds")
)

type TCPBuffer struct {
	mu       sync.Mutex
	data     []byte
	closed   bool
	notEmpty chan struct{}
}

func NewTCPBuffer() *TCPBuffer {
	return &TCPBuffer{
		notEmpty: make(chan struct{}, 1),
	}
}

func (b *TCPBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return 0, fmt.Errorf("buffer closed")
	}

	if len(b.data)+len(p) > *bufferSize {
		return 0, fmt.Errorf("buffer overflow")
	}

	b.data = append(b.data, p...)
	select {
	case b.notEmpty <- struct{}{}:
	default:
	}

	return len(p), nil
}

func (b *TCPBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.data) == 0 && !b.closed {
		b.mu.Unlock()
		<-b.notEmpty
		b.mu.Lock()
	}

	if len(b.data) == 0 && b.closed {
		return 0, io.EOF
	}

	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *TCPBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	close(b.notEmpty)
	return nil
}

type BufferProxy struct {
	listener     net.Listener
	appConn      net.Conn
	buffer       *TCPBuffer
	appPort      int
	targetIP     string
	targetPort   int
	mu           sync.Mutex
	running      bool
	stats        BufferStats
	handoverDone bool
}

type BufferStats struct {
	BytesReceived      int64
	BytesForwarded     int64
	ConnectionsHandled int64
	HandoversSucceeded int64
}

func NewBufferProxy(appPort int, targetIP string, targetPort int) *BufferProxy {
	return &BufferProxy{
		buffer:     NewTCPBuffer(),
		appPort:    appPort,
		targetIP:   targetIP,
		targetPort: targetPort,
		running:    true,
	}
}

func (p *BufferProxy) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *targetPort))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	p.listener = ln

	go p.connectToApp(ctx)
	go p.drainBufferToApp(ctx)

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

	go p.startManagementServer()

	return nil
}

func (p *BufferProxy) connectToApp(ctx context.Context) {
	for p.running {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.appPort), time.Duration(*handshakeTimeout)*time.Millisecond)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		p.mu.Lock()
		if p.appConn != nil {
			p.appConn.Close()
		}
		p.appConn = conn
		p.mu.Unlock()
		break
	}
}

func (p *BufferProxy) drainBufferToApp(ctx context.Context) {
	for p.running {
		buf := make([]byte, 32*1024)
		n, err := p.buffer.Read(buf)
		if err != nil {
			if err == io.EOF {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			break
		}

		p.mu.Lock()
		if p.appConn == nil {
			p.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}

		_, err = p.appConn.Write(buf[:n])
		p.mu.Unlock()

		if err != nil {
			p.connectToApp(ctx)
			continue
		}

		p.stats.BytesForwarded += int64(n)
	}
}

func (p *BufferProxy) handleConnection(conn net.Conn) {
	defer conn.Close()
	p.stats.ConnectionsHandled++

	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			p.buffer.Write(buf[:n])
			p.stats.BytesReceived += int64(n)
		}
		if err != nil {
			break
		}
	}
}

func (p *BufferProxy) SignalHandover() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.appConn != nil {
		p.appConn.Close()
		p.appConn = nil
	}

	time.Sleep(100 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.appPort), time.Duration(*handshakeTimeout)*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to connect to app: %w", err)
	}

	p.appConn = conn
	p.handoverDone = true
	p.stats.HandoversSucceeded++

	return nil
}

func (p *BufferProxy) Transparentize() error {
	p.running = false
	p.buffer.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.listener != nil {
		p.listener.Close()
	}

	return nil
}

func (p *BufferProxy) startManagementServer() {
	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "BytesReceived: %d\nBytesForwarded: %d\nConnectionsHandled: %d\nHandoversSucceeded: %d\n",
			p.stats.BytesReceived, p.stats.BytesForwarded, p.stats.ConnectionsHandled, p.stats.HandoversSucceeded)
	})

	http.HandleFunc("/handover", func(w http.ResponseWriter, r *http.Request) {
		if err := p.SignalHandover(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Handover completed"))
	})

	http.HandleFunc("/transparentize", func(w http.ResponseWriter, r *http.Request) {
		if err := p.Transparentize(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Transparentized"))
	})

	addr := fmt.Sprintf(":%d", *managementPort)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Management server error: %v\n", err)
	}
}

func (p *BufferProxy) Stop() error {
	p.running = false
	p.buffer.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.listener != nil {
		p.listener.Close()
	}
	if p.appConn != nil {
		p.appConn.Close()
	}

	return nil
}

type TCTap struct {
	ifaceName  string
	sourceIP   string
	targetIP   string
	targetPort int
	appPort    int
	listener   net.Listener
	localProxy *BufferProxy
	running    bool
}

func NewTCTap(ifaceName, sourceIP, targetIP string, targetPort, appPort int) *TCTap {
	return &TCTap{
		ifaceName:  ifaceName,
		sourceIP:   sourceIP,
		targetIP:   targetIP,
		targetPort: targetPort,
		appPort:    appPort,
		running:    true,
	}
}

func (t *TCTap) Start(ctx context.Context) error {
	t.localProxy = NewBufferProxy(t.appPort, "", 0)
	if err := t.localProxy.Start(ctx); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", t.targetPort))
	if err != nil {
		return err
	}
	t.listener = ln

	go func() {
		for t.running {
			conn, err := ln.Accept()
			if err != nil {
				if t.running {
					continue
				}
				break
			}
			go t.forwardToTarget(conn)
		}
	}()

	if err := t.setupTCFilter(); err != nil {
		return err
	}

	return nil
}

func (t *TCTap) forwardToTarget(conn net.Conn) {
	defer conn.Close()

	targetConn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", t.targetIP, t.targetPort))
	if err != nil {
		return
	}
	defer targetConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(targetConn, conn)
		targetConn.Close()
	}()

	go func() {
		defer wg.Done()
		io.Copy(conn, targetConn)
		conn.Close()
	}()

	wg.Wait()
}

func (t *TCTap) setupTCFilter() error {
	script := fmt.Sprintf(`
tc qdisc add dev %s handle ffff: clsact 2>/dev/null || true
tc filter add dev %s protocol ip pref 100 handle ffff: bpf da obj tap_kern.o section tc/ingress
tc filter add dev %s protocol ip pref 100 handle ffff: bpf da obj tap_kern.o section tc/egress
`, t.ifaceName, t.ifaceName, t.ifaceName)

	return runCommand("sh", "-c", script)
}

func (t *TCTap) Stop() error {
	t.running = false

	if t.listener != nil {
		t.listener.Close()
	}

	if t.localProxy != nil {
		t.localProxy.Stop()
	}

	runCommand("tc", "qdisc", "del", "dev", t.ifaceName, "handle", "ffff:", "clsact")

	return nil
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		cancel()
	}()

	switch *mode {
	case "buffer":
		proxy := NewBufferProxy(*appPort, *targetIP, *targetPort)
		if err := proxy.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start buffer proxy: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Buffer proxy started on port %d, app port %d\n", *targetPort, *appPort)

		<-ctx.Done()
		proxy.Stop()

	case "tap":
		if *sourcePodIP == "" || *targetNodeIP == "" {
			fmt.Fprintf(os.Stderr, "source-pod-ip and target-node-ip required for tap mode\n")
			os.Exit(1)
		}

		iface, err := findInterfaceForIP(*sourcePodIP)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to find interface: %v\n", err)
			os.Exit(1)
		}

		tap := NewTCTap(iface, *sourcePodIP, *targetNodeIP, *targetPort, *appPort)
		if err := tap.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start tap: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Tap started on interface %s\n", iface)

		<-ctx.Done()
		tap.Stop()

	case "passthrough":
		fmt.Println("Starting passthrough mode...")
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *appPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to listen: %v\n", err)
			os.Exit(1)
		}

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					break
				}
				go handlePassthrough(conn)
			}
		}()

		<-ctx.Done()
		ln.Close()

	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s\n", *mode)
		os.Exit(1)
	}
}

func findInterfaceForIP(ip string) (string, error) {
	_, err := net.InterfaceByName("eth0")
	if err == nil {
		return "eth0", nil
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.Equal(net.ParseIP(ip)) {
			ifaces, err := net.Interfaces()
			if err != nil {
				return "", err
			}
			for _, i := range ifaces {
				if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
					continue
				}
				addrs, _ := i.Addrs()
				for _, addr := range addrs {
					if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.Equal(net.ParseIP(ip)) {
						return i.Name, nil
					}
				}
			}
		}
	}

	return "eth0", nil
}

func handlePassthrough(conn net.Conn) {
	defer conn.Close()

	appConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", *appPort))
	if err != nil {
		return
	}
	defer appConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(appConn, conn)
		if tcpConn, ok := appConn.(*net.TCPConn); ok {
			tcpConn.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		io.Copy(conn, appConn)
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			tcpConn.CloseWrite()
		}
	}()

	wg.Wait()
}
