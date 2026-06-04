// Package tor manages the embedded Tor process and hidden service identity.
//
// The tor binary is embedded directly in the Go binary (via assets package)
// and extracted to ~/.kin/bin/tor at first run — no `pkg install tor` needed.
//
// Architecture:
//   - TorManager extracts the bundled tor binary for the current arch.
//   - Writes a torrc into dataDir, starts tor as a subprocess.
//   - The v3 hidden service key is persisted in dataDir/hidden_service/.
//   - The .onion address is the permanent Kin identity ("ken address").
//   - A TCP signaling server listens on the hidden service port to
//     exchange SDP (ufrag/pwd/candidates) with peers.
package tor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"kin/assets"
)

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager controls an embedded Tor process.
type Manager struct {
	mu           sync.Mutex
	dataDir      string
	binPath      string // extracted binary path
	socksPort    int
	hiddenPort   int
	onionAddr    string
	cmd          *exec.Cmd
	bootstrapped chan struct{}
}

// LogHandler receives tor log lines.
type LogHandler func(line string)

// NewManager creates (but does not start) a Tor manager.
//
//	dataDir:    directory for Tor state and hidden service key (~/.kin/tor).
//	socksPort:  SOCKS5 port tor will bind (9050 is standard).
//	hiddenPort: local TCP port the signaling server listens on.
func NewManager(dataDir string, socksPort, hiddenPort int) *Manager {
	return &Manager{
		dataDir:      dataDir,
		socksPort:    socksPort,
		hiddenPort:   hiddenPort,
		bootstrapped: make(chan struct{}),
	}
}

// Start extracts the embedded tor binary (if needed), writes torrc, and
// launches tor as a child process.
func (m *Manager) Start(logFn LogHandler) error {
	if err := os.MkdirAll(m.dataDir, 0700); err != nil {
		return fmt.Errorf("tor: mkdir data: %w", err)
	}
	hsDir := filepath.Join(m.dataDir, "hidden_service")
	if err := os.MkdirAll(hsDir, 0700); err != nil {
		return fmt.Errorf("tor: mkdir hs: %w", err)
	}
	// Try system tor first
	var binPath string
	var err error
	if runtime.GOOS == "android" {
		termuxTor := "/data/data/com.termux/files/usr/bin/tor"
		if _, err := os.Stat(termuxTor); err == nil {
			binPath = termuxTor
		}
	}
	if binPath == "" {
		binPath, err = exec.LookPath("tor")
	}

	if err == nil && binPath != "" {
		m.binPath = binPath
	} else {
		// Fallback to extracting embedded binary
		binDir := filepath.Join(m.dataDir, "bin")
		if err := os.MkdirAll(binDir, 0700); err != nil {
			return fmt.Errorf("tor: mkdir bin: %w", err)
		}
		binPath, err = m.extractBinary(binDir)
		if err != nil {
			return fmt.Errorf("tor: extract binary: %w", err)
		}
		m.binPath = binPath
	}

	// Write torrc
	torrc := filepath.Join(m.dataDir, "torrc")
	if err := m.writeTorrc(torrc, hsDir); err != nil {
		return fmt.Errorf("tor: write torrc: %w", err)
	}

	cmd := exec.Command(binPath, "-f", torrc)
	m.cmd = cmd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tor: start binary %s: %w", binPath, err)
	}

	go m.readLogs(stdout, logFn)
	go m.pollHostname()
	return nil
}

// extractBinary writes the platform-appropriate embedded tor binary to disk.
// It is idempotent — skips extraction if the file already exists and is
// non-empty (i.e. from a previous run).
func (m *Manager) extractBinary(binDir string) (string, error) {
	name := torBinaryName()
	binPath := filepath.Join(binDir, name)

	data := embeddedTorBytes()
	if data == nil {
		return "", fmt.Errorf("no embedded tor binary for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Check if already extracted and same size
	if info, err := os.Stat(binPath); err == nil && info.Size() == int64(len(data)) {
		return binPath, nil // already up to date
	}

	if err := os.WriteFile(binPath, data, 0755); err != nil {
		return "", fmt.Errorf("write binary: %w", err)
	}
	return binPath, nil
}

// torBinaryName returns the filename for the current platform.
func torBinaryName() string {
	return fmt.Sprintf("tor-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// embeddedTorBytes returns the embedded bytes for the current GOOS/GOARCH.
func embeddedTorBytes() []byte {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/arm64", "android/arm64":
		return assets.TorArm64
	case "linux/amd64":
		return assets.TorAmd64
	}
	return nil
}

func (m *Manager) writeTorrc(path, hsDir string) error {
	content := fmt.Sprintf(`SocksPort %d
DataDirectory %s
HiddenServiceDir %s
HiddenServicePort %d 127.0.0.1:%d
Log notice stdout
`, m.socksPort, m.dataDir, hsDir, m.hiddenPort, m.hiddenPort)
	return os.WriteFile(path, []byte(content), 0600)
}

func (m *Manager) readLogs(r io.Reader, logFn LogHandler) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if logFn != nil {
			logFn(line)
		}
		if strings.Contains(line, "Bootstrapped 100%") {
			m.mu.Lock()
			addr, _ := m.readOnionAddr()
			m.onionAddr = addr
			select {
			case <-m.bootstrapped:
			default:
				close(m.bootstrapped)
			}
			m.mu.Unlock()
		}
	}
}

// pollHostname reads the hostname file every 2 s until available.
// This handles the case where the 100% log line was missed.
func (m *Manager) pollHostname() {
	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)
		m.mu.Lock()
		if m.onionAddr != "" {
			m.mu.Unlock()
			return
		}
		addr, err := m.readOnionAddr()
		if err == nil && addr != "" {
			m.onionAddr = addr
			select {
			case <-m.bootstrapped:
			default:
				close(m.bootstrapped)
			}
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func (m *Manager) readOnionAddr() (string, error) {
	path := filepath.Join(m.dataDir, "hidden_service", "hostname")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// WaitBootstrap blocks until Tor is fully bootstrapped or timeout expires.
func (m *Manager) WaitBootstrap(timeout time.Duration) error {
	select {
	case <-m.bootstrapped:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("tor: timed out waiting for bootstrap (check torrc / permissions)")
	}
}

// OnionAddr returns the .onion address once available.
func (m *Manager) OnionAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onionAddr
}

// Proxy returns the SOCKS5 proxy address ("127.0.0.1:port").
func (m *Manager) Proxy() string {
	return fmt.Sprintf("127.0.0.1:%d", m.socksPort)
}

// BinPath returns the path to the extracted tor binary.
func (m *Manager) BinPath() string {
	return m.binPath
}

// Stop terminates the tor process.
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// ---------------------------------------------------------------------------
// Signaling Server (listens on hidden service port)
// ---------------------------------------------------------------------------

// SDPOffer is the JSON envelope exchanged via the hidden service.
type SDPOffer struct {
	Type       string   `json:"type,omitempty"` // "offer", "answer", "chat"
	Ufrag      string   `json:"ufrag,omitempty"`
	Pwd        string   `json:"pwd,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	From       string   `json:"from,omitempty"`
	Body       string   `json:"body,omitempty"`
	// libp2p fields
	PeerID     string   `json:"peer_id,omitempty"`
	Addrs      []string `json:"addrs,omitempty"`
}

// SignalingServer listens for incoming JSON SDP offers on hiddenPort.
type SignalingServer struct {
	port           int
	onOffer        func(offer SDPOffer, conn net.Conn)
	onFileTransfer func(offer SDPOffer, buffered io.Reader, conn net.Conn)
	l              net.Listener
}

// NewSignalingServer creates a signaling server.
func NewSignalingServer(port int, onOffer func(offer SDPOffer, conn net.Conn)) *SignalingServer {
	return &SignalingServer{port: port, onOffer: onOffer}
}

func (s *SignalingServer) SetOnFileTransfer(cb func(offer SDPOffer, buffered io.Reader, conn net.Conn)) {
	s.onFileTransfer = cb
}

// Start begins accepting connections on 127.0.0.1:port.
func (s *SignalingServer) Start() error {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	s.l = l
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return nil
}

func (s *SignalingServer) handle(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	dec := json.NewDecoder(conn)
	var offer SDPOffer
	if err := dec.Decode(&offer); err != nil {
		conn.Close()
		return
	}
	conn.SetDeadline(time.Time{})

	if offer.Type == "file_transfer_init" {
		if s.onFileTransfer != nil {
			s.onFileTransfer(offer, dec.Buffered(), conn)
		} else {
			conn.Close()
		}
		return
	}

	if s.onOffer != nil {
		s.onOffer(offer, conn)
	} else {
		conn.Close()
	}
}

// Stop closes the listener.
func (s *SignalingServer) Stop() {
	if s.l != nil {
		s.l.Close()
	}
}

// ---------------------------------------------------------------------------
// Signaling Client (dials .onion via SOCKS5)
// ---------------------------------------------------------------------------

// Dial connects to a peer's hidden service through the Tor SOCKS5 proxy,
// sends an SDP offer, and waits for the peer's answer.
func Dial(socksProxy, onionAddr string, port int, offer SDPOffer, timeout time.Duration) (SDPOffer, error) {
	proxyConn, err := net.DialTimeout("tcp", socksProxy, timeout)
	if err != nil {
		return SDPOffer{}, fmt.Errorf("tor: dial proxy %s: %w", socksProxy, err)
	}
	target := fmt.Sprintf("%s:%d", onionAddr, port)
	if err := Socks5Connect(proxyConn, target, timeout); err != nil {
		proxyConn.Close()
		return SDPOffer{}, fmt.Errorf("tor: socks5 connect %s: %w", target, err)
	}
	proxyConn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(proxyConn).Encode(offer); err != nil {
		proxyConn.Close()
		return SDPOffer{}, err
	}
	var answer SDPOffer
	if err := json.NewDecoder(proxyConn).Decode(&answer); err != nil {
		proxyConn.Close()
		return SDPOffer{}, err
	}
	proxyConn.Close()
	return answer, nil
}

// ---------------------------------------------------------------------------
// Minimal built-in SOCKS5 client (RFC 1928) — no external library.
// ---------------------------------------------------------------------------

// Socks5Connect connects a TCP socket to a target address through SOCKS5.
func Socks5Connect(conn net.Conn, target string, timeout time.Duration) error {
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})

	// Greeting: VER=5, NMETHODS=1, METHOD=0x00 (no auth)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		return fmt.Errorf("socks5: server requires auth (got 0x%02x)", resp[1])
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	// CONNECT request: VER=5 CMD=1 RSV=0 ATYP=3 (domain)
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port&0xff))
	if _, err := conn.Write(req); err != nil {
		return err
	}

	// Response header: VER REP RSV ATYP
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[1] != 0x00 {
		return fmt.Errorf("socks5: CONNECT failed (REP=0x%02x)", hdr[1])
	}
	// Drain bound address field
	switch hdr[3] {
	case 0x01: // IPv4
		io.ReadFull(conn, make([]byte, 4+2))
	case 0x03: // domain
		ln := make([]byte, 1)
		io.ReadFull(conn, ln)
		io.ReadFull(conn, make([]byte, int(ln[0])+2))
	case 0x04: // IPv6
		io.ReadFull(conn, make([]byte, 16+2))
	}
	return nil
}

// SendChatMessage connects to the peer's hidden service via Tor SOCKS5, sends a chat message, and returns.
func SendChatMessage(socksProxy, onionAddr string, port int, fromOnion, body string, timeout time.Duration) error {
	proxyConn, err := net.DialTimeout("tcp", socksProxy, timeout)
	if err != nil {
		return fmt.Errorf("tor: dial proxy %s: %w", socksProxy, err)
	}
	defer proxyConn.Close()
	target := fmt.Sprintf("%s:%d", onionAddr, port)
	if err := Socks5Connect(proxyConn, target, timeout); err != nil {
		return fmt.Errorf("tor: socks5 connect %s: %w", target, err)
	}
	proxyConn.SetDeadline(time.Now().Add(timeout))
	msg := SDPOffer{
		Type: "chat",
		From: fromOnion,
		Body: body,
	}
	if err := json.NewEncoder(proxyConn).Encode(msg); err != nil {
		return err
	}
	return nil
}
