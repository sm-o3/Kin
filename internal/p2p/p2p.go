package p2p

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/gologshim"
	"github.com/multiformats/go-multiaddr"

	"kin/internal/store"
)

func init() {
	gologshim.SetDefaultHandler(logging.SlogHandler())
	logging.SetLogLevel("basichost", "fatal")
}

const protocolID = "/kin/chat/1.0.0"
const FileProtocolID = "/kin/file/1.0.0"

type FileHeader struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// Default IPFS/libp2p bootstrap nodes for peer discovery and relay support.
var defaultBootstrapPeers = []string{
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2m2z4wLY1xR73QJ4MHx5tD2wqPjAHkwv3SGg",
	"/ip4/104.248.44.204/tcp/4001/p2p/Qme8g49dzzrf9FRnwNs65Rfs27pmDEgAgFy8nWu9iwyvCZ",
	"/ip4/128.199.219.111/tcp/4001/p2p/QmSoLSaf6aVW16Pri1SZgMhDKhTyysQXkr8maQQ4ayXC7_",
	"/ip4/178.62.158.247/tcp/4001/p2p/QmSoLerGSccXmJvUk67ndM26mawuoj2nwCqCfjhxr17oRj",
	"/ip4/139.178.91.71/tcp/4001/p2p/QmNoll7w2ECUn8fiejieLHxtJTCu8hUMztK_56U2mgpsgD",
}

type ChatMsg struct {
	From string `json:"from"` // sender's onion
	Body string `json:"body"`
}

type Engine struct {
	host       host.Host
	db         *store.DB
	myOnion    string
	onMessage  func(fromOnion, body string)
	onState    func(peerOnion, state string)
	logFn      func(line string)
	OnContactUpdated func()
	onFileStream func(peerPIDStr string, stream io.ReadWriteCloser)

	mu       sync.Mutex
	streams  map[peer.ID]network.Stream
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewEngine(db *store.DB, myOnion string, onMessage func(fromOnion, body string), onState func(peerOnion, state string), logFn func(line string)) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		db:        db,
		myOnion:   myOnion,
		onMessage: onMessage,
		onState:   onState,
		logFn:     logFn,
		streams:   make(map[peer.ID]network.Stream),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func getLocalIPs() []string {
	var localIPs []string
	seen := make(map[string]bool)
	addIP := func(ip string) {
		if ip == "" || ip == "127.0.0.1" {
			return
		}
		if !seen[ip] {
			seen[ip] = true
			localIPs = append(localIPs, ip)
		}
	}

	if ifaddrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range ifaddrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					addIP(ip4.String())
				}
			}
		}
	}

	if len(localIPs) == 0 {
		var binPath string
		candidates := []string{
			"/data/data/com.termux/files/usr/bin/ifconfig",
			"/system/bin/ifconfig",
			"/sbin/ifconfig",
			"/usr/bin/ifconfig",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				binPath = c
				break
			}
		}
		if binPath != "" {
			cmd := exec.Command(binPath)
			out, err := cmd.Output()
			if err == nil {
				re := regexp.MustCompile(`inet\s+([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
				matches := re.FindAllStringSubmatch(string(out), -1)
				for _, m := range matches {
					if len(m) > 1 {
						addIP(m[1])
					}
				}
			}
		}
	}
	return localIPs
}

// Start initializes the libp2p host.
func (e *Engine) Start() error {
	// Retrieve or generate persistent private key
	privBytes := e.db.GetLibp2pKey()
	var priv crypto.PrivKey
	var err error

	if len(privBytes) == 0 {
		e.log("[libp2p] Generating stable P2P identity key...")
		priv, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate p2p key: %w", err)
		}
		marshaled, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return fmt.Errorf("marshal p2p key: %w", err)
		}
		if err := e.db.SaveLibp2pKey(marshaled); err != nil {
			return fmt.Errorf("save p2p key: %w", err)
		}
	} else {
		e.log("[libp2p] Loading stable P2P identity key from DB...")
		priv, err = crypto.UnmarshalPrivateKey(privBytes)
		if err != nil {
			return fmt.Errorf("unmarshal p2p key: %w", err)
		}
	}

	// Create libp2p Host
	// Listen on randomized TCP and UDP ports, explicitly binding to local IPs to bypass interface enumeration blocks in Android/Termux
	listenAddrStrings := []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
	}
	for _, ip := range getLocalIPs() {
		listenAddrStrings = append(listenAddrStrings, fmt.Sprintf("/ip4/%s/tcp/0", ip))
		listenAddrStrings = append(listenAddrStrings, fmt.Sprintf("/ip4/%s/udp/0/quic-v1", ip))
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listenAddrStrings...),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.NATPortMap(),
	)
	if err != nil {
		return fmt.Errorf("create libp2p host: %w", err)
	}
	e.host = h

	e.log(fmt.Sprintf("[libp2p] Host created! PeerID: %s", h.ID().String()))
	for _, addr := range h.Addrs() {
		e.log(fmt.Sprintf("[libp2p] Listening on: %s/p2p/%s", addr.String(), h.ID().String()))
	}

	// Setup incoming stream handler
	h.SetStreamHandler(protocolID, e.handleStream)
	h.SetStreamHandler(FileProtocolID, e.handleFileStream)

	// Async bootstrap to connect to DHT and circuit relays
	go e.bootstrap()

	// Start Local LAN Discovery (multicast UDP)
	e.startLanDiscovery()

	return nil
}

func (e *Engine) log(line string) {
	if e.logFn != nil {
		e.logFn(line)
	}
}

// GetMyInfo returns the host Peer ID and local Multiaddresses.
func (e *Engine) GetMyInfo() (string, []string) {
	if e.host == nil {
		return "", nil
	}
	var addrs []string
	var tcpPort, udpPort string

	for _, addr := range e.host.Addrs() {
		addrs = append(addrs, addr.String())
		// Extract bound ports
		if tcp, err := addr.ValueForProtocol(multiaddr.P_TCP); err == nil {
			tcpPort = tcp
		}
		if udp, err := addr.ValueForProtocol(multiaddr.P_UDP); err == nil {
			udpPort = udp
		}
	}

	localIPs := getLocalIPs()

	// Append local multiaddresses for each IP
	for _, ipStr := range localIPs {
		hasIP := false
		for _, existing := range addrs {
			if strings.Contains(existing, "/ip4/"+ipStr) {
				hasIP = true
				break
			}
		}
		if !hasIP {
			if tcpPort != "" {
				addrs = append(addrs, fmt.Sprintf("/ip4/%s/tcp/%s", ipStr, tcpPort))
			}
			if udpPort != "" {
				addrs = append(addrs, fmt.Sprintf("/ip4/%s/udp/%s/quic-v1", ipStr, udpPort))
			}
		}
	}

	return e.host.ID().String(), addrs
}

// bootstrap connects to public bootstrap peers to find relay nodes and join the network DHT.
func (e *Engine) bootstrap() {
	e.log("[libp2p] Bootstrapping discovery & relay connectivity...")
	var wg sync.WaitGroup
	for _, addrStr := range defaultBootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(info peer.AddrInfo) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
			defer cancel()
			if err := e.host.Connect(ctx, info); err != nil {
				// Quietly ignore failed bootstrap connections
				return
			}
			e.log(fmt.Sprintf("[libp2p] Connected to bootstrap peer: %s", info.ID.String()[:8]))
		}(*ai)
	}
	wg.Wait()
	e.log("[libp2p] Bootstrap routine completed.")
}

// handleStream processes an incoming libp2p stream.
func (e *Engine) handleStream(s network.Stream) {
	remotePID := s.Conn().RemotePeer()
	e.log(fmt.Sprintf("[libp2p] Inbound stream established from %s", remotePID.String()[:8]))

	e.mu.Lock()
	if old, ok := e.streams[remotePID]; ok {
		old.Reset()
	}
	e.streams[remotePID] = s
	e.mu.Unlock()

	// Update peer status to connected if we know the contact
	contact, err := e.db.GetContactByLibp2pID(remotePID.String())
	if err == nil && contact != nil {
		if e.onState != nil {
			e.onState(contact.ID, "connected")
		}
	}

	go e.readLoop(remotePID, s)
}

func (e *Engine) readLoop(pid peer.ID, s network.Stream) {
	defer func() {
		s.Reset()
		e.mu.Lock()
		if curr, ok := e.streams[pid]; ok && curr == s {
			delete(e.streams, pid)
		}
		e.mu.Unlock()

		// Update peer status to failed/disconnected if we know the contact
		contact, err := e.db.GetContactByLibp2pID(pid.String())
		if err == nil && contact != nil {
			if e.onState != nil {
				e.onState(contact.ID, "disconnected")
			}
		}
	}()

	dec := json.NewDecoder(s)
	for {
		var msg ChatMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if e.onMessage != nil {
			e.onMessage(msg.From, msg.Body)
		}
	}
}

// Connect adds a peer's multiaddresses and initiates a connection.
func (e *Engine) Connect(peerIDStr string, addrs []string) (string, error) {
	e.log(fmt.Sprintf("[libp2p] Resolving multiaddresses for peer %s...", peerIDStr[:min(8, len(peerIDStr))]))
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid peer ID: %w", err)
	}

	// Close any existing stale/dead connection to the peer before dialing
	if e.host != nil {
		_ = e.host.Network().ClosePeer(pid)
	}

	var mas []multiaddr.Multiaddr
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		mas = append(mas, ma)
	}

	if len(mas) == 0 {
		return "", fmt.Errorf("no valid multiaddresses provided")
	}

	e.host.Peerstore().AddAddrs(pid, mas, peerstore.PermanentAddrTTL)

	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()

	e.log(fmt.Sprintf("[libp2p] Dialing libp2p peer %s...", pid.String()[:8]))
	if err := e.host.Connect(ctx, peer.AddrInfo{ID: pid}); err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}

	e.log(fmt.Sprintf("[libp2p] Successfully connected to peer %s", pid.String()[:8]))

	// Open or retrieve chat stream
	e.mu.Lock()
	s, exists := e.streams[pid]
	e.mu.Unlock()

	if !exists {
		sCtx, sCancel := context.WithTimeout(e.ctx, 15*time.Second)
		s, err = e.host.NewStream(sCtx, pid, protocolID)
		sCancel()
		if err != nil {
			return "", fmt.Errorf("failed to open stream: %w", err)
		}
		e.mu.Lock()
		e.streams[pid] = s
		e.mu.Unlock()
		go e.readLoop(pid, s)
	}

	return pid.String(), nil
}

// Send sends a chat message to a peer over libp2p.
func (e *Engine) Send(peerIDStr string, body string) error {
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer ID: %w", err)
	}

	e.mu.Lock()
	s, exists := e.streams[pid]
	e.mu.Unlock()

	if !exists {
		// Try to connect using peerstore addresses
		e.log(fmt.Sprintf("[libp2p] Re-establishing stream to peer %s...", pid.String()[:8]))
		sCtx, sCancel := context.WithTimeout(e.ctx, 15*time.Second)
		s, err = e.host.NewStream(sCtx, pid, protocolID)
		sCancel()
		if err != nil {
			return fmt.Errorf("stream offline: %w", err)
		}
		e.mu.Lock()
		e.streams[pid] = s
		e.mu.Unlock()
		go e.readLoop(pid, s)
	}

	msg := ChatMsg{
		From: e.myOnion,
		Body: body,
	}

	enc := json.NewEncoder(s)
	if err := enc.Encode(msg); err != nil {
		s.Reset()
		e.mu.Lock()
		delete(e.streams, pid)
		e.mu.Unlock()
		return fmt.Errorf("encode failed: %w", err)
	}

	return nil
}

// Close shuts down the engine and its host.
func (e *Engine) Close() error {
	e.cancel()
	e.mu.Lock()
	for _, s := range e.streams {
		s.Reset()
	}
	e.streams = make(map[peer.ID]network.Stream)
	e.mu.Unlock()

	if e.host != nil {
		return e.host.Close()
	}
	return nil
}

// IsConnected checks if we have an active stream or connection to the peer.
func (e *Engine) IsConnected(peerIDStr string) bool {
	if e.host == nil || peerIDStr == "" {
		return false
	}
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return false
	}
	e.mu.Lock()
	_, exists := e.streams[pid]
	e.mu.Unlock()
	if exists {
		return true
	}
	return len(e.host.Network().ConnsToPeer(pid)) > 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── LAN Discovery (multicast UDP) ──────────────────────────────────────────

const lanMulticastAddr = "239.255.255.250:19192"

type LanAnnouncement struct {
	Onion    string   `json:"onion"`
	PeerID   string   `json:"peer_id"`
	Addrs    []string `json:"addrs"`
	Nickname string   `json:"nickname"`
}

func (e *Engine) startLanDiscovery() {
	go e.lanBroadcastLoop()
	go e.lanListenLoop()
}

func getBroadcastAddrs() []string {
	var list []string
	seen := make(map[string]bool)
	addAddr := func(addr string) {
		if addr == "" {
			return
		}
		if !seen[addr] {
			seen[addr] = true
			list = append(list, addr)
		}
	}

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if !ok || ipNet.IP.IsLoopback() {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil {
					continue
				}
				// Calculate broadcast IP
				ip := make(net.IP, len(ip4))
				for i := range ip4 {
					ip[i] = ip4[i] | ^ipNet.Mask[i]
				}
				addAddr(fmt.Sprintf("%s:19192", ip.String()))
			}
		}
	}

	if len(list) == 0 {
		candidates := []string{
			"/data/data/com.termux/files/usr/bin/ifconfig",
			"/system/bin/ifconfig",
			"/sbin/ifconfig",
			"/usr/bin/ifconfig",
		}
		var binPath string
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				binPath = c
				break
			}
		}
		if binPath != "" {
			cmd := exec.Command(binPath)
			out, err := cmd.Output()
			if err == nil {
				// Parse both 'broadcast 10.42.0.255' and 'Bcast:10.42.0.255'
				re := regexp.MustCompile(`(?:broadcast|Bcast:)\s*([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
				matches := re.FindAllStringSubmatch(string(out), -1)
				for _, m := range matches {
					if len(m) > 1 {
						addAddr(fmt.Sprintf("%s:19192", m[1]))
					}
				}
			}
		}
	}

	addAddr("255.255.255.255:19192")
	return list
}

func (e *Engine) lanBroadcastLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			pid, addrs := e.GetMyInfo()
			if pid == "" {
				continue
			}

			nickname := "Kin Peer"
			myContact, err := e.db.GetContactByLibp2pID(pid)
			if err == nil && myContact != nil && myContact.Nickname != "" {
				nickname = myContact.Nickname
			}

			ann := LanAnnouncement{
				Onion:    e.myOnion,
				PeerID:   pid,
				Addrs:    addrs,
				Nickname: nickname,
			}

			data, err := json.Marshal(ann)
			if err != nil {
				continue
			}

			broadcastAddrs := getBroadcastAddrs()
			conn, err := net.ListenPacket("udp4", ":0")
			if err != nil {
				e.log(fmt.Sprintf("[lan] failed to create broadcast socket: %v", err))
				continue
			}
			for _, bAddrStr := range broadcastAddrs {
				addr, err := net.ResolveUDPAddr("udp4", bAddrStr)
				if err != nil {
					continue
				}
				_, err = conn.WriteTo(data, addr)
				if err != nil {
					e.log(fmt.Sprintf("[lan] broadcast write to %s failed: %v", bAddrStr, err))
				}
			}
			conn.Close()
		}
	}
}

func (e *Engine) lanListenLoop() {
	conn, err := net.ListenPacket("udp4", ":19192")
	if err != nil {
		e.log(fmt.Sprintf("[lan] UDP listen failed on port 19192: %v. LAN discovery disabled.", err))
		return
	}
	defer conn.Close()

	buf := make([]byte, 65535)

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				continue
			}

			var ann LanAnnouncement
			if err := json.Unmarshal(buf[:n], &ann); err != nil {
				continue
			}

			myPID, _ := e.GetMyInfo()
			if ann.PeerID == myPID || ann.PeerID == "" {
				continue
			}

			e.handleDiscoveredPeer(ann)
		}
	}
}

func (e *Engine) handleDiscoveredPeer(ann LanAnnouncement) {
	contact, err := e.db.GetContactByLibp2pID(ann.PeerID)
	if err != nil || contact == nil {
		if ann.Onion != "" {
			c, err := e.db.GetContact(ann.Onion)
			if err == nil {
				contact = c
			}
		}
		if contact != nil {
			contact.Libp2pID = ann.PeerID
			_ = e.db.SaveContact(contact)
			e.log(fmt.Sprintf("[lan] Associated Peer ID %s with contact %s", ann.PeerID[:8], contact.Nickname))
		} else {
			// Find by nickname fallback
			contacts, err := e.db.AllContacts()
			found := false
			if err == nil {
				for _, c := range contacts {
					if c.Nickname == ann.Nickname && (c.Libp2pID == "" || c.ID == "") {
						contact = c
						contact.Libp2pID = ann.PeerID
						if ann.Onion != "" {
							_ = e.db.DeleteContact(c.ID)
							contact.ID = ann.Onion
						}
						_ = e.db.SaveContact(contact)
						e.log(fmt.Sprintf("[lan] Restored contact %s using LAN announcement", c.Nickname))
						found = true
						break
					}
				}
			}
			if !found {
				// Create a new automatic contact!
				id := ann.Onion
				if id == "" {
					id = ann.PeerID
				}
				contact = &store.Contact{
					ID:       id,
					Nickname: ann.Nickname + " (LAN)",
					Libp2pID: ann.PeerID,
				}
				_ = e.db.SaveContact(contact)
				e.log(fmt.Sprintf("[lan] Automatically added discovered local peer %s to contacts", contact.Nickname))
			}
		}
	} else {
		if ann.Onion != "" && contact.ID != ann.Onion {
			oldOnion := contact.ID
			_ = e.db.DeleteContact(oldOnion)
			contact.ID = ann.Onion
			_ = e.db.SaveContact(contact)
			e.log(fmt.Sprintf("[lan] Updated/Restored onion address for contact %s to %s", contact.Nickname, ann.Onion))
		}
	}

	// Trigger the contact updated callback to push updates to the UI
	if e.OnContactUpdated != nil {
		e.OnContactUpdated()
	}

	if e.host != nil && ann.PeerID != "" {
		pid, err := peer.Decode(ann.PeerID)
		if err == nil {
			var mas []multiaddr.Multiaddr
			for _, a := range ann.Addrs {
				ma, err := multiaddr.NewMultiaddr(a)
				if err == nil {
					mas = append(mas, ma)
				}
			}
			if len(mas) > 0 {
				e.host.Peerstore().AddAddrs(pid, mas, peerstore.TempAddrTTL)
				if !e.IsConnected(ann.PeerID) {
					e.log(fmt.Sprintf("[lan] Discovered peer %s (%s) on LAN. Connecting...", ann.Nickname, ann.PeerID[:8]))
					go func() {
						_, _ = e.Connect(ann.PeerID, ann.Addrs)
					}()
				}
			}
		}
	}
}

// ── DHT Resolution ────────────────────────────────────────────────────────

func (e *Engine) ResolvePeerDHT(peerID string) ([]string, error) {
	if peerID == "" {
		return nil, fmt.Errorf("empty peer ID")
	}

	gateways := []string{
		"https://ipfs.io/api/v0/dht/findpeer?arg=%s",
		"https://dweb.link/api/v0/dht/findpeer?arg=%s",
		"https://gateway.ipfs.io/api/v0/dht/findpeer?arg=%s",
	}

	var lastErr error
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	for _, gwPattern := range gateways {
		url := fmt.Sprintf(gwPattern, peerID)
		e.log(fmt.Sprintf("[dht] Querying gateway: %s", url))
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			e.log(fmt.Sprintf("[dht] Gateway failed: %v", err))
			continue
		}
		
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP status %d", resp.StatusCode)
			e.log(fmt.Sprintf("[dht] Gateway returned bad status: %d", resp.StatusCode))
			continue
		}

		dec := json.NewDecoder(resp.Body)
		var addrs []string
		for {
			var entry struct {
				Type      int `json:"Type"`
				Responses []struct {
					ID    string   `json:"ID"`
					Addrs []string `json:"Addrs"`
				} `json:"Responses"`
			}
			if err := dec.Decode(&entry); err != nil {
				if err == io.EOF {
					break
				}
				continue
			}

			if entry.Type == 2 { // FinalPeer response
				for _, r := range entry.Responses {
					if r.ID == peerID {
						addrs = append(addrs, r.Addrs...)
					}
				}
			}
		}
		resp.Body.Close()

		if len(addrs) > 0 {
			e.log(fmt.Sprintf("[dht] Successfully resolved Peer ID %s to %d addresses", peerID[:8], len(addrs)))
			return addrs, nil
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("dht resolution failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no addresses resolved in DHT for Peer ID: %s", peerID)
}

func ExtractOnionFromAddrs(addrs []string) string {
	for _, addr := range addrs {
		if strings.Contains(addr, "/onion3/") {
			re := regexp.MustCompile(`([a-z2-7]{56})\.onion`)
			if m := re.FindStringSubmatch(addr); len(m) > 1 {
				return m[1] + ".onion"
			}
			re2 := regexp.MustCompile(`/onion3/([a-z2-7]{56})`)
			if m := re2.FindStringSubmatch(addr); len(m) > 1 {
				return m[1] + ".onion"
			}
		}
	}
	return ""
}

func (e *Engine) SetOnFileStream(cb func(peerPIDStr string, stream io.ReadWriteCloser)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onFileStream = cb
}

func (e *Engine) handleFileStream(s network.Stream) {
	remotePID := s.Conn().RemotePeer().String()
	e.log(fmt.Sprintf("[libp2p] Inbound file stream established from %s", remotePID[:min(8, len(remotePID))]))
	e.mu.Lock()
	cb := e.onFileStream
	e.mu.Unlock()
	if cb != nil {
		cb(remotePID, s)
	} else {
		s.Reset()
	}
}

func (e *Engine) GetHost() host.Host {
	return e.host
}

func (e *Engine) OpenFileStream(ctx context.Context, peerPIDStr string) (io.ReadWriteCloser, error) {
	pid, err := peer.Decode(peerPIDStr)
	if err != nil {
		return nil, err
	}
	return e.host.NewStream(ctx, pid, FileProtocolID)
}

func CopyWithProgress(dst io.Writer, src io.Reader, totalSize int64, onProgress func(bytesWritten int64)) (int64, error) {
	bufSize := 32 * 1024
	if totalSize > 10*1024*1024 { // > 10MB
		if totalSize <= 100*1024*1024 { // <= 100MB
			bufSize = 128 * 1024
		} else if totalSize <= 1024*1024*1024 { // <= 1GB
			bufSize = 512 * 1024
		} else if totalSize <= 10*1024*1024*1024 { // <= 10GB
			bufSize = 1024 * 1024
		} else { // > 10GB up to 100GB+
			bufSize = 4 * 1024 * 1024
		}
	}

	buf := make([]byte, bufSize)
	var written int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = fmt.Errorf("invalid write result")
				}
			}
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
			if onProgress != nil {
				onProgress(written)
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			return written, er
		}
	}
	return written, nil
}

