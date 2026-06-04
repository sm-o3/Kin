package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"kin/internal/p2p"
	"kin/internal/store"
	"kin/internal/tor"
	"kin/internal/webserver"
)

// ---------------------------------------------------------------------------
// App wires all subsystems together.
// ---------------------------------------------------------------------------

const (
	appName        = "kin"
	torSocksPort   = 9950
	signalingPort  = 19191 // local port the hidden service maps to
)

type App struct {
	db         *store.DB
	torMgr     *tor.Manager
	sigServer  *tor.SignalingServer
	p2pEngine  *p2p.Engine
	web        *webserver.Server

	myOnion string
	dataDir string

	testReceiver bool
	testSender   string

	torConns   map[string]net.Conn
	torConnsMu sync.Mutex
}

const webUIPort = 8080

func main() {
	testReceiver := flag.Bool("test-receiver", false, "Run in automated test mode as receiver")
	testSender   := flag.String("test-sender", "", "Run in automated test mode as sender to the specified onion address")
	webPort      := flag.Int("web-port", webUIPort, "Port for the web UI")
	showVersion  := flag.Bool("version", false, "Print version and exit")
	showVersionV := flag.Bool("v", false, "Print version and exit")
	flag.Parse()

	if *showVersion || *showVersionV || (len(os.Args) > 1 && os.Args[1] == "version") {
		fmt.Println("v1.0.1")
		return
	}

	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".kin")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("kin: cannot create data dir: %v", err)
	}

	if *testReceiver {
		runReceiverTest(dataDir)
		return
	}
	if *testSender != "" {
		runSenderTest(dataDir, *testSender)
		return
	}

	// Open DB
	db, err := store.Open(filepath.Join(dataDir, "kin.db"))
	if err != nil {
		log.Fatalf("kin: db: %v", err)
	}
	defer db.Close()

	app := &App{
		db:           db,
		dataDir:      dataDir,
		testReceiver: *testReceiver,
		testSender:   *testSender,
		torConns:     make(map[string]net.Conn),
	}

	// Start web UI
	wcb := webserver.Callbacks{
		GetMyOnion:    func() string { return app.myOnion },
		GetMyPeerID: func() string {
			if app.p2pEngine != nil {
				id, _ := app.p2pEngine.GetMyInfo()
				return id
			}
			return ""
		},
		GetContacts:   app.getContacts,
		GetMessages:          app.getHistory,
		GetMessagesPaginated: app.getHistoryPaginated,
		SendMessage:          app.sendMessage,
		SendEphemeralMessage: app.sendEphemeralMessage,
		AddContact:           app.addContactWithPeerID,
		DeleteContact:        app.deleteContact,
		ClearChat:            app.clearChat,
		ConnectPeer:          app.connectPeer,
		ResolveDHT:           app.resolveOnionFromDHT,
		SaveMessage:          app.db.SaveMessage,
		DeleteMessage: func(peer, msgID string) error {
			return app.db.DeleteMessage(peer, msgID)
		},
		StarMessage: func(peer, msgID string, starred bool) error {
			return app.db.StarMessage(peer, msgID, starred)
		},
		ClearAllData: func() error {
			return app.db.ClearAllData()
		},
		CompactDB: func() error {
			return app.db.Compact(filepath.Join(app.dataDir, "kin.db"))
		},
		SendFile: app.sendFile,
		GetTorStatus: func() string {
			if app.torMgr != nil && app.myOnion != "" {
				return "online"
			}
			return "offline"
		},
	}
	app.web = webserver.New(*webPort, wcb)
	if err := app.web.Start(); err != nil {
		log.Printf("[web] failed to start: %v", err)
	} else {
		log.Printf("[web] UI available at http://127.0.0.1:%d", *webPort)
	}

	// Start Tor in background
	go app.startTor()

	// Wait for SIGINT or SIGTERM and print logs to console
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	log.Println("[web] Running. Press Ctrl+C to stop.")
	<-sigChan
	log.Println("[web] Shutting down Kin...")
}

// ---------------------------------------------------------------------------
// Tor startup
// ---------------------------------------------------------------------------

func (a *App) logMsg(label, line string) {
	formatted := fmt.Sprintf("[%s] %s", label, line)
	log.Println(formatted)
	if a.web != nil {
		go a.web.PushLog(formatted)
	}
}

func (a *App) startTor() {
	torDir := filepath.Join(a.dataDir, "tor")
	mgr := tor.NewManager(torDir, torSocksPort, signalingPort)
	a.torMgr = mgr

	logFn := func(line string) {
		a.logMsg("tor", line)
	}

	torFailed := false
	if err := mgr.Start(logFn); err != nil {
		a.logMsg("error", "tor start: "+err.Error())
		torFailed = true
	}

	if !torFailed {
		// Wait up to 90 s for bootstrap
		if err := mgr.WaitBootstrap(90 * time.Second); err != nil {
			a.logMsg("error", "tor bootstrap failed: "+err.Error())
			torFailed = true
		}
	}

	onion := ""
	if !torFailed {
		onion = mgr.OnionAddr()
		a.myOnion = onion
		a.db.SetIdentity(onion, "")
		a.logMsg("tor", "Bootstrapped successfully! Onion address: "+onion)
		if a.web != nil {
			peerID := ""
			if a.p2pEngine != nil {
				peerID, _ = a.p2pEngine.GetMyInfo()
			}
			a.web.PushIdentity(onion, peerID)
		}
	} else {
		if a.web != nil {
			a.web.PushIdentity("", "")
		}
	}

	// Start libp2p Engine
	onMsg := func(fromOnion, body string) {
		if a.web != nil && (strings.HasPrefix(body, "[call-sig:") ||
			strings.HasPrefix(body, "[call-stream:")) {
			handled, _ := a.web.HandleInboundMessage(fromOnion, body)
			if handled {
				return
			}
		}
		a.db.SaveMessage(&store.Message{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
			From:      fromOnion,
			Body:      body,
			Timestamp: time.Now(),
		})
		log.Printf("[chat] Message received from %s: %s", fromOnion, body)
		if a.web != nil {
			go a.web.PushMessage(fromOnion, a.getHistory(fromOnion))
		}
	}
	onState := func(peerOnion, state string) {
		a.logMsg("p2p", "Connection state with "+truncateOnion(peerOnion)+" changed to: "+state)
	}
	p2pLogFn := func(line string) {
		a.logMsg("libp2p", line)
	}

	p2pE := p2p.NewEngine(a.db, onion, onMsg, onState, p2pLogFn)
	p2pE.OnContactUpdated = func() {
		if a.web != nil {
			a.web.PushContactsUpdate()
		}
	}
	p2pE.SetOnFileStream(func(peerPIDStr string, stream io.ReadWriteCloser) {
		dec := json.NewDecoder(stream)
		var hdr p2p.FileHeader
		if err := dec.Decode(&hdr); err != nil {
			stream.Close()
			return
		}
		var onion string
		contact, err := a.db.GetContactByLibp2pID(peerPIDStr)
		if err == nil && contact != nil {
			onion = contact.ID
		} else {
			onion = peerPIDStr
		}
		fileReader := io.MultiReader(dec.Buffered(), stream)
		go a.handleFileReceive(onion, hdr, fileReader, func() { stream.Close() })
	})
	if err := p2pE.Start(); err != nil {
		a.logMsg("error", "libp2p start failed: "+err.Error())
	} else {
		a.logMsg("libp2p", "libp2p Engine started successfully")
	}
	a.p2pEngine = p2pE
	if a.web != nil {
		peerID, _ := p2pE.GetMyInfo()
		a.web.PushIdentity(a.myOnion, peerID)
	}

	// Start signaling server
	a.startSignalingServer()

	// Automatically establish connection silently in background to all previous contacts
	go func() {
		time.Sleep(3 * time.Second)
		contacts := a.getContacts()
		for _, c := range contacts {
			go a.silentConnectPeer(c.ID)
		}
	}()

	// Start background worker to check and deliver pending queued messages
	go a.pendingMessageWorker()
}

func (a *App) startSignalingServer() {
	srv := tor.NewSignalingServer(signalingPort, func(msg tor.SDPOffer, conn net.Conn) {
		if msg.Type == "chat_stream_init" {
			a.torConnsMu.Lock()
			if old, ok := a.torConns[msg.From]; ok {
				old.Close()
			}
			a.torConns[msg.From] = conn
			a.torConnsMu.Unlock()
			a.logMsg("tor-fallback", "Persistent Tor stream from "+truncateOnion(msg.From)+" established")
			go a.readPersistentTorStream(msg.From, conn)
			return
		}
		if msg.Type == "chat" {
			defer conn.Close()
			// Save and display chat message immediately
			a.db.SaveMessage(&store.Message{
				ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
				From:      msg.From,
				To:        "me",
				Body:      msg.Body,
				Timestamp: time.Now(),
			})
			if a.web != nil {
				go a.web.PushMessage(msg.From, a.getHistory(msg.From))
			}
			return
		}
		a.onOfferReceived(msg, conn)
	})
	srv.SetOnFileTransfer(func(offer tor.SDPOffer, buffered io.Reader, conn net.Conn) {
		var hdr p2p.FileHeader
		if err := json.Unmarshal([]byte(offer.Body), &hdr); err != nil {
			conn.Close()
			return
		}
		fileReader := io.MultiReader(buffered, conn)
		go a.handleFileReceive(offer.From, hdr, fileReader, func() { conn.Close() })
	})
	a.sigServer = srv
	if err := srv.Start(); err != nil {
		log.Printf("[sig] signaling server start error: %v", err)
	}
}

// onOfferReceived handles an incoming offer from a peer dialing our hidden service.
func (a *App) onOfferReceived(offer tor.SDPOffer, conn net.Conn) {
	a.logMsg("sig", fmt.Sprintf("Incoming connection offer from %s (PeerID: %s)", truncateOnion(offer.From), offer.PeerID))
	go a.handleIncomingOffer(offer, conn)
}

func (a *App) handleIncomingOffer(offer tor.SDPOffer, conn net.Conn) {
	if a.p2pEngine == nil {
		return
	}
	// Save/update contact with remote PeerID and Addrs
	contact, err := a.db.GetContact(offer.From)
	if err != nil {
		// Create contact
		contact = &store.Contact{
			ID:          offer.From,
			Nickname:    "",
			AddedAt:     time.Now(),
			Libp2pID:    offer.PeerID,
			Libp2pAddrs: offer.Addrs,
		}
	} else {
		contact.Libp2pID = offer.PeerID
		contact.Libp2pAddrs = offer.Addrs
	}
	a.db.SaveContact(contact)

	// Build our answer
	myID, myAddrs := a.p2pEngine.GetMyInfo()
	answer := tor.SDPOffer{
		Type:   "answer",
		PeerID: myID,
		Addrs:  myAddrs,
		From:   a.myOnion,
	}

	// Send answer back over the same TCP connection
	enc := json.NewEncoder(conn)
	enc.Encode(answer)
	conn.Close()

	// Connect to them via libp2p in the background
	go func() {
		a.logMsg("libp2p", "Connecting back to "+truncateOnion(offer.From)+"...")
		_, err = a.p2pEngine.Connect(offer.PeerID, offer.Addrs)
		if err != nil {
			a.logMsg("libp2p", "Failed to connect back to "+truncateOnion(offer.From)+": "+err.Error())
		} else {
			a.logMsg("libp2p", "Connected back to "+truncateOnion(offer.From))
			a.sendPendingMessages(offer.From)
		}
	}()
}

func (a *App) acceptConnection(offer tor.SDPOffer, conn net.Conn) {
	go a.handleIncomingOffer(offer, conn)
}

func (a *App) rejectConnection(conn net.Conn) {
	if conn != nil {
		conn.Close()
	}
}

func (a *App) sendEphemeralMessage(peerID, body string) error {
	contact, err := a.db.GetContact(peerID)
	if err == nil && contact.Libp2pID != "" {
		if err := a.p2pEngine.Send(contact.Libp2pID, body); err == nil {
			return nil
		}
	}
	if a.torMgr == nil {
		return fmt.Errorf("tor not ready")
	}
	return a.sendTorFallback(peerID, body)
}

func (a *App) sendMessage(peerID, body string) error {
	contact, err := a.db.GetContact(peerID)
	if err == nil && contact.Libp2pID != "" {
		if err := a.p2pEngine.Send(contact.Libp2pID, body); err == nil {
			return a.db.SaveMessage(&store.Message{
				ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
				From:      "me",
				To:        peerID,
				Body:      body,
				Timestamp: time.Now(),
				Read:      true,
			})
		}
		a.logMsg("sig", "libp2p send failed, falling back to Tor...")
	}

	// Fallback to Tor direct message delivery
	if a.torMgr == nil {
		return fmt.Errorf("tor not ready for fallback")
	}

	// Save to DB immediately so it is recorded in the UI history
	msgObj := &store.Message{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		From:      "me",
		To:        peerID,
		Body:      body,
		Timestamp: time.Now(),
		Read:      false,
	}
	if err := a.db.SaveMessage(msgObj); err != nil {
		return err
	}

	// Deliver in background
	go func() {
		err := a.sendTorFallback(peerID, body)
		if err != nil {
			a.logMsg("sig", fmt.Sprintf("Tor send to %s failed: %v", truncateOnion(peerID), err))
			return
		}
		a.logMsg("sig", fmt.Sprintf("Message delivered to %s via Tor", truncateOnion(peerID)))
		
		// Mark as delivered in the database
		msgObj.Read = true
		_ = a.db.SaveMessage(msgObj)
	}()

	return nil
}

func truncateOnion(s string) string {
	if len(s) > 12 {
		return s[:8] + "…" + s[len(s)-4:]
	}
	return s
}

func (a *App) connectPeer(peerID string) {
	if a.torMgr == nil {
		a.logMsg("error", "tor not ready")
		return
	}

	contact, err := a.db.GetContact(peerID)
	if err == nil && contact.Libp2pID != "" && len(contact.Libp2pAddrs) > 0 {
		a.logMsg("libp2p", "Found cached P2P addresses. Connecting directly...")
		go func() {
			a.logMsg("libp2p", "Connecting to "+truncateOnion(peerID)+"...")
			_, err := a.p2pEngine.Connect(contact.Libp2pID, contact.Libp2pAddrs)
			if err == nil {
				a.logMsg("libp2p", "Connected to "+truncateOnion(peerID))
				a.sendPendingMessages(peerID)
				return
			}
			a.logMsg("libp2p", "Direct connection failed, falling back to Tor handshake...")
			a.runTorHandshake(peerID)
		}()
	} else {
		a.runTorHandshake(peerID)
	}
}

func (a *App) runTorHandshake(peerID string) {
	if a.p2pEngine == nil {
		a.logMsg("error", "P2P engine not ready")
		return
	}
	myID, myAddrs := a.p2pEngine.GetMyInfo()
	offer := tor.SDPOffer{
		Type:   "offer",
		PeerID: myID,
		Addrs:  myAddrs,
		From:   a.myOnion,
	}

	// Try automatic Tor dial
	proxy := a.torMgr.Proxy()
	go func() {
		a.logMsg("sig", "Dialing peer "+truncateOnion(peerID)+" via Tor...")
		answer, err := tor.Dial(proxy, peerID, signalingPort, offer, 60*time.Second)
		if err != nil {
			a.logMsg("sig", "Connection failed: "+err.Error())
			return
		}
		a.logMsg("sig", "Peer answered! Connecting via libp2p...")

		// Save/update contact with remote PeerID and Addrs
		contact, err := a.db.GetContact(peerID)
		if err == nil && contact != nil {
			contact.Libp2pID = answer.PeerID
			contact.Libp2pAddrs = answer.Addrs
			a.db.SaveContact(contact)
		}

		a.logMsg("libp2p", "Connecting to "+truncateOnion(peerID)+"...")
		_, err = a.p2pEngine.Connect(answer.PeerID, answer.Addrs)
		if err != nil {
			a.logMsg("libp2p", "Failed to connect to "+truncateOnion(peerID)+": "+err.Error())
		} else {
			a.logMsg("libp2p", "Connected to "+truncateOnion(peerID))
			a.sendPendingMessages(peerID)
		}
	}()
}

func (a *App) pasteOffer(offerJSON string) error {
	var offer tor.SDPOffer
	if err := json.Unmarshal([]byte(offerJSON), &offer); err != nil {
		return fmt.Errorf("invalid offer JSON: %w", err)
	}
	if a.p2pEngine == nil {
		return fmt.Errorf("no active agent — connect first")
	}
	
	// Save/update contact with remote PeerID and Addrs
	contact, err := a.db.GetContact(offer.From)
	if err != nil {
		contact = &store.Contact{
			ID:          offer.From,
			Nickname:    "",
			AddedAt:     time.Now(),
			Libp2pID:    offer.PeerID,
			Libp2pAddrs: offer.Addrs,
		}
	} else {
		contact.Libp2pID = offer.PeerID
		contact.Libp2pAddrs = offer.Addrs
	}
	a.db.SaveContact(contact)

	go func() {
		a.logMsg("libp2p", "Connecting to "+truncateOnion(offer.From)+"...")
		_, err = a.p2pEngine.Connect(offer.PeerID, offer.Addrs)
		if err != nil {
			a.logMsg("libp2p", "Paste offer connect failed: "+err.Error())
		} else {
			a.logMsg("libp2p", "Connected to "+truncateOnion(offer.From))
		}
	}()

	return nil
}

func (a *App) getLocalOffer() string {
	if a.p2pEngine == nil {
		return ""
	}
	myID, myAddrs := a.p2pEngine.GetMyInfo()
	offer := tor.SDPOffer{
		Type:   "offer",
		PeerID: myID,
		Addrs:  myAddrs,
		From:   a.myOnion,
	}
	b, _ := json.Marshal(offer)
	return string(b)
}

func (a *App) addContact(id, nickname string) error {
	return a.addContactWithPeerID(id, nickname, "")
}

func (a *App) addContactWithPeerID(id, nickname, libp2pID string) error {
	c := &store.Contact{
		ID:       id,
		Nickname: nickname,
		Libp2pID: libp2pID,
		AddedAt:  time.Now(),
	}
	return a.db.SaveContact(c)
}

func (a *App) resolveOnionFromDHT(peerID string) (string, error) {
	if a.p2pEngine == nil {
		return "", fmt.Errorf("P2P engine not initialized")
	}
	addrs, err := a.p2pEngine.ResolvePeerDHT(peerID)
	if err != nil {
		return "", err
	}
	onion := p2p.ExtractOnionFromAddrs(addrs)
	if onion == "" {
		return "", fmt.Errorf("resolved addresses do not contain a Tor onion endpoint")
	}
	return onion, nil
}

func (a *App) deleteContact(id string) error {
	return a.db.DeleteContact(id)
}

func (a *App) clearChat(peerID string) error {
	return a.db.ClearMessages(peerID)
}

func (a *App) getHistory(peerID string) []*store.Message {
	msgs, _ := a.db.GetMessages(peerID)
	return msgs
}

func (a *App) getHistoryPaginated(peerID string, offset, limit int) []*store.Message {
	msgs, _ := a.db.GetMessagesPaginated(peerID, offset, limit)
	return msgs
}

func (a *App) getContacts() []*store.Contact {
	contacts, _ := a.db.AllContacts()
	return contacts
}

// ---------------------------------------------------------------------------
// Test routines (modified to use libp2p)
// ---------------------------------------------------------------------------

func runReceiverTest(dataDir string) {
	fmt.Println("TEST_MODE: Receiver starting...")
	
	db, err := store.Open(filepath.Join(dataDir, "kin_test_rec.db"))
	if err != nil {
		log.Fatalf("Test DB open failed: %v", err)
	}
	defer db.Close()
	
	// Start Tor
	torDir := filepath.Join(dataDir, "tor")
	mgr := tor.NewManager(torDir, torSocksPort, signalingPort)
	
	fmt.Println("TEST_MODE: Starting Tor...")
	if err := mgr.Start(func(line string) { fmt.Println("Tor:", line) }); err != nil {
		log.Fatalf("Tor start failed: %v", err)
	}
	
	fmt.Println("TEST_MODE: Waiting for Tor bootstrap...")
	if err := mgr.WaitBootstrap(90 * time.Second); err != nil {
		log.Fatalf("Tor bootstrap failed: %v", err)
	}
	
	onion := mgr.OnionAddr()
	fmt.Printf("TEST_ONION: %s\n", onion)
	
	connectedChan := make(chan bool, 1)

	// Initialize libp2p Engine
	p2pE := p2p.NewEngine(db, onion, func(fromOnion, body string) {
		fmt.Printf("TEST_RECEIVED_CHAT: %s\n", body)
	}, func(peerOnion, state string) {
		if state == "connected" {
			select {
			case connectedChan <- true:
			default:
			}
		}
	}, func(line string) {
		fmt.Println(line)
	})

	if err := p2pE.Start(); err != nil {
		log.Fatalf("libp2p Start failed: %v", err)
	}
	defer p2pE.Close()
	
	srv := tor.NewSignalingServer(signalingPort, func(msg tor.SDPOffer, conn net.Conn) {
		if msg.Type == "chat" {
			fmt.Printf("TEST_RECEIVED_CHAT: %s\n", msg.Body)
			return
		}
		
		fmt.Println("TEST_MODE: Receiver received SDP offer. Initializing libp2p connection...")
		
		// Save/update contact info
		contact := &store.Contact{
			ID:          msg.From,
			Nickname:    "",
			AddedAt:     time.Now(),
			Libp2pID:    msg.PeerID,
			Libp2pAddrs: msg.Addrs,
		}
		db.SaveContact(contact)

		// Answer with our info
		myID, myAddrs := p2pE.GetMyInfo()
		answer := tor.SDPOffer{
			Type:   "answer",
			PeerID: myID,
			Addrs:  myAddrs,
			From:   onion,
		}
		
		enc := json.NewEncoder(conn)
		if err := enc.Encode(answer); err != nil {
			fmt.Printf("TEST_ERROR: encode answer failed: %v\n", err)
			return
		}
		
		fmt.Println("TEST_MODE: Receiver starting connection...")
		go func() {
			_, err := p2pE.Connect(msg.PeerID, msg.Addrs)
			if err != nil {
				fmt.Printf("TEST_ERROR: connect back failed: %v\n", err)
				return
			}
			fmt.Println("TEST_ICE_CONNECTED: libp2p connection established!")
			select {
			case connectedChan <- true:
			default:
			}
		}()
	})
	
	if err := srv.Start(); err != nil {
		log.Fatalf("Signaling server failed: %v", err)
	}
	defer srv.Stop()
	
	// Wait for connection to establish
	select {
	case <-connectedChan:
		fmt.Println("TEST_SUCCESS: ICE connection established!")
		os.Exit(0)
	case <-time.After(120 * time.Second):
		fmt.Println("TEST_FAILED: Timed out waiting for ICE connection")
		os.Exit(1)
	}
}

func runSenderTest(dataDir string, receiverOnion string) {
	fmt.Printf("TEST_MODE: Sender starting to connect to %s...\n", receiverOnion)
	
	db, err := store.Open(filepath.Join(dataDir, "kin_test_snd.db"))
	if err != nil {
		log.Fatalf("Test DB open failed: %v", err)
	}
	defer db.Close()

	// Start Tor
	torDir := filepath.Join(dataDir, "tor")
	mgr := tor.NewManager(torDir, torSocksPort, signalingPort)
	
	fmt.Println("TEST_MODE: Starting Tor...")
	if err := mgr.Start(func(line string) { fmt.Println("Tor:", line) }); err != nil {
		log.Fatalf("Tor start failed: %v", err)
	}
	
	fmt.Println("TEST_MODE: Waiting for Tor bootstrap...")
	if err := mgr.WaitBootstrap(90 * time.Second); err != nil {
		log.Fatalf("Tor bootstrap failed: %v", err)
	}
	
	onion := mgr.OnionAddr()
	connectedChan := make(chan bool, 1)

	// Initialize libp2p Engine
	p2pE := p2p.NewEngine(db, onion, func(fromOnion, body string) {
		fmt.Printf("TEST_RECEIVED_CHAT: %s\n", body)
	}, func(peerOnion, state string) {
		if state == "connected" {
			select {
			case connectedChan <- true:
			default:
			}
		}
	}, func(line string) {
		fmt.Println(line)
	})

	if err := p2pE.Start(); err != nil {
		log.Fatalf("libp2p Start failed: %v", err)
	}
	defer p2pE.Close()
	
	myID, myAddrs := p2pE.GetMyInfo()
	offer := tor.SDPOffer{
		Type:   "offer",
		PeerID: myID,
		Addrs:  myAddrs,
		From:   onion,
	}
	
	fmt.Println("TEST_MODE: Dialing receiver via Tor hidden service to exchange libp2p info...")
	proxy := mgr.Proxy()
	
	answer, err := tor.Dial(proxy, receiverOnion, signalingPort, offer, 90*time.Second)
	if err != nil {
		log.Fatalf("TEST_FAILED: tor.Dial signaling failed: %v", err)
	}
	
	fmt.Println("TEST_MODE: Receiver answer received. Starting libp2p connection...")
	
	// Save/update contact info
	contact := &store.Contact{
		ID:          receiverOnion,
		Nickname:    "",
		AddedAt:     time.Now(),
		Libp2pID:    answer.PeerID,
		Libp2pAddrs: answer.Addrs,
	}
	db.SaveContact(contact)

	go func() {
		_, err := p2pE.Connect(answer.PeerID, answer.Addrs)
		if err != nil {
			fmt.Printf("TEST_ERROR: connect failed: %v\n", err)
			return
		}
		fmt.Println("TEST_ICE_CONNECTED: libp2p connection established!")
		select {
		case connectedChan <- true:
		default:
		}
	}()
	
	// Wait for connection to establish
	select {
	case <-connectedChan:
		fmt.Println("TEST_SUCCESS: Sender ICE connection established!")
		os.Exit(0)
	case <-time.After(120 * time.Second):
		fmt.Println("TEST_FAILED: Sender timed out waiting for connection")
		os.Exit(1)
	}
}

func (a *App) silentConnectPeer(peerID string) {
	contact, err := a.db.GetContact(peerID)
	if err != nil {
		return
	}
	if contact.Libp2pID != "" && len(contact.Libp2pAddrs) > 0 {
		_, err := a.p2pEngine.Connect(contact.Libp2pID, contact.Libp2pAddrs)
		if err == nil {
			a.logMsg("libp2p", fmt.Sprintf("Silently connected to %s", truncateOnion(peerID)))
			a.sendPendingMessages(peerID)
			return
		}
	}
	a.silentTorHandshake(peerID)
}

func (a *App) silentTorHandshake(peerID string) {
	if a.p2pEngine == nil || a.torMgr == nil {
		return
	}
	myID, myAddrs := a.p2pEngine.GetMyInfo()
	offer := tor.SDPOffer{
		Type:   "offer",
		PeerID: myID,
		Addrs:  myAddrs,
		From:   a.myOnion,
	}

	proxy := a.torMgr.Proxy()
	go func() {
		answer, err := tor.Dial(proxy, peerID, signalingPort, offer, 60*time.Second)
		if err != nil {
			return
		}

		// Save/update contact with remote PeerID and Addrs
		contact, err := a.db.GetContact(peerID)
		if err == nil && contact != nil {
			contact.Libp2pID = answer.PeerID
			contact.Libp2pAddrs = answer.Addrs
			a.db.SaveContact(contact)
		}

		_, err = a.p2pEngine.Connect(answer.PeerID, answer.Addrs)
		if err == nil {
			a.logMsg("libp2p", fmt.Sprintf("Silently connected to %s after handshake", truncateOnion(peerID)))
			a.sendPendingMessages(peerID)
		}
	}()
}

func (a *App) sendPendingMessages(peerID string) {
	messages, err := a.db.GetMessages(peerID)
	if err != nil {
		return
	}

	for _, msg := range messages {
		if msg.From == "me" && !msg.Read {
			a.logMsg("queue", fmt.Sprintf("Retrying sending pending message to %s...", truncateOnion(peerID)))
			var delivered bool
			contact, err := a.db.GetContact(peerID)
			if err == nil && contact.Libp2pID != "" {
				if err := a.p2pEngine.Send(contact.Libp2pID, msg.Body); err == nil {
					delivered = true
				}
			}

			if !delivered {
				err := a.sendTorFallback(peerID, msg.Body)
				if err == nil {
					delivered = true
				}
			}

			if delivered {
				msg.Read = true
				_ = a.db.SaveMessage(msg)
				a.logMsg("queue", fmt.Sprintf("Pending message delivered to %s", truncateOnion(peerID)))
			} else {
				a.logMsg("queue", fmt.Sprintf("Pending message delivery to %s failed, will retry later", truncateOnion(peerID)))
			}
		}
	}
}

func (a *App) pendingMessageWorker() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if a.p2pEngine == nil {
				continue
			}
			contacts := a.getContacts()
			for _, c := range contacts {
				messages, err := a.db.GetMessages(c.ID)
				if err != nil {
					continue
				}
				hasPending := false
				for _, m := range messages {
					if m.From == "me" && !m.Read {
						hasPending = true
						break
					}
				}
				if hasPending {
					if c.Libp2pID != "" && a.p2pEngine.IsConnected(c.Libp2pID) {
						go a.sendPendingMessages(c.ID)
					} else {
						go a.silentConnectPeer(c.ID)
					}
				}
			}
		}
	}
}

func (a *App) sendTorFallback(peerID, body string) error {
	a.torConnsMu.Lock()
	conn, exists := a.torConns[peerID]
	a.torConnsMu.Unlock()

	if !exists {
		if a.torMgr == nil {
			return fmt.Errorf("tor not ready")
		}
		proxy := a.torMgr.Proxy()
		a.logMsg("tor-fallback", fmt.Sprintf("Establishing persistent Tor connection to %s...", truncateOnion(peerID)))

		dialTimeout := 30 * time.Second
		proxyConn, err := net.DialTimeout("tcp", proxy, dialTimeout)
		if err != nil {
			return fmt.Errorf("dial Tor proxy: %w", err)
		}

		target := fmt.Sprintf("%s:%d", peerID, signalingPort)
		if err := tor.Socks5Connect(proxyConn, target, dialTimeout); err != nil {
			proxyConn.Close()
			return fmt.Errorf("Tor connect: %w", err)
		}

		// Send handshake init
		initMsg := tor.SDPOffer{
			Type: "chat_stream_init",
			From: a.myOnion,
		}
		if err := json.NewEncoder(proxyConn).Encode(initMsg); err != nil {
			proxyConn.Close()
			return fmt.Errorf("send init: %w", err)
		}

		conn = proxyConn
		a.torConnsMu.Lock()
		a.torConns[peerID] = conn
		a.torConnsMu.Unlock()

		// Start read loop for any incoming messages from them over this connection
		go a.readPersistentTorStream(peerID, conn)
	}

	// Send the message
	msg := tor.SDPOffer{
		Type: "chat",
		From: a.myOnion,
		Body: body,
	}
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	err := json.NewEncoder(conn).Encode(msg)
	conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		a.torConnsMu.Lock()
		delete(a.torConns, peerID)
		a.torConnsMu.Unlock()
		return fmt.Errorf("send failed: %w", err)
	}

	return nil
}

func (a *App) readPersistentTorStream(peerID string, conn net.Conn) {
	defer func() {
		conn.Close()
		a.torConnsMu.Lock()
		if curr, ok := a.torConns[peerID]; ok && curr == conn {
			delete(a.torConns, peerID)
		}
		a.torConnsMu.Unlock()
		a.logMsg("tor-fallback", fmt.Sprintf("Persistent Tor stream from %s closed", truncateOnion(peerID)))
	}()

	dec := json.NewDecoder(conn)
	for {
		var msg tor.SDPOffer
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if msg.Type == "chat" {
			a.db.SaveMessage(&store.Message{
				ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
				From:      msg.From,
				To:        "me",
				Body:      msg.Body,
				Timestamp: time.Now(),
			})
			if a.web != nil {
				go a.web.PushMessage(msg.From, a.getHistory(msg.From))
			}
		}
	}
}

func (a *App) sendFile(peerID, filePath, fileName, mimeType string, onProgress func(sent int64)) error {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	fileSize := fileInfo.Size()
	id := fmt.Sprintf("ft%d", time.Now().UnixNano())

	go func() {
		contact, err := a.db.GetContact(peerID)
		if err == nil && contact.Libp2pID != "" {
			// Proactively attempt to establish direct P2P connection before falling back to Tor
			if !a.p2pEngine.IsConnected(contact.Libp2pID) && len(contact.Libp2pAddrs) > 0 {
				a.logMsg("filetransfer", "Attempting P2P connection to "+contact.Libp2pID[:8]+"...")
				_, _ = a.p2pEngine.Connect(contact.Libp2pID, contact.Libp2pAddrs)
			}

			if a.p2pEngine.IsConnected(contact.Libp2pID) {
				err := a.sendFileLibp2p(contact.Libp2pID, id, filePath, fileName, mimeType, fileSize, onProgress)
				if err == nil {
					return
				}
				a.logMsg("filetransfer", "P2P transfer failed, falling back to Tor: "+err.Error())
			}
		}

		if a.torMgr != nil {
			err := a.sendFileTor(peerID, id, filePath, fileName, mimeType, fileSize, onProgress)
			if err != nil {
				a.logMsg("filetransfer-error", "Tor file transfer failed: "+err.Error())
			}
		} else {
			a.logMsg("filetransfer-error", "No transport available for file transfer")
		}
	}()

	return nil
}

func (a *App) sendFileLibp2p(peerPIDStr, id, filePath, fileName, mimeType string, fileSize int64, onProgress func(sent int64)) error {
	a.logMsg("filetransfer", "Opening libp2p file stream to "+peerPIDStr[:8]+"...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := a.p2pEngine.OpenFileStream(ctx, peerPIDStr)
	if err != nil {
		return err
	}
	defer stream.Close()

	hdr := p2p.FileHeader{
		ID:   id,
		Name: fileName,
		Mime: mimeType,
		Size: fileSize,
	}

	hdrBytes, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if _, err := stream.Write(hdrBytes); err != nil {
		return err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = p2p.CopyWithProgress(stream, f, fileSize, onProgress)
	if err != nil {
		return err
	}

	a.logMsg("filetransfer", "P2P file transfer completed successfully for "+fileName)
	return nil
}

func (a *App) sendFileTor(peerID, id, filePath, fileName, mimeType string, fileSize int64, onProgress func(sent int64)) error {
	a.logMsg("filetransfer", "Establishing Tor connection for file transfer to "+truncateOnion(peerID)+"...")

	proxy := a.torMgr.Proxy()
	dialTimeout := 30 * time.Second
	proxyConn, err := net.DialTimeout("tcp", proxy, dialTimeout)
	if err != nil {
		return fmt.Errorf("dial Tor proxy: %w", err)
	}

	target := fmt.Sprintf("%s:%d", peerID, signalingPort)
	if err := tor.Socks5Connect(proxyConn, target, dialTimeout); err != nil {
		proxyConn.Close()
		return fmt.Errorf("Tor connect: %w", err)
	}

	hdr, _ := json.Marshal(p2p.FileHeader{
		ID:   id,
		Name: fileName,
		Mime: mimeType,
		Size: fileSize,
	})

	initMsg := tor.SDPOffer{
		Type: "file_transfer_init",
		From: a.myOnion,
		Body: string(hdr),
	}

	proxyConn.SetDeadline(time.Now().Add(15 * time.Second))
	initBytes, err := json.Marshal(initMsg)
	if err != nil {
		proxyConn.Close()
		return fmt.Errorf("marshal init: %w", err)
	}
	if _, err := proxyConn.Write(initBytes); err != nil {
		proxyConn.Close()
		return fmt.Errorf("send init: %w", err)
	}
	proxyConn.SetDeadline(time.Time{})

	f, err := os.Open(filePath)
	if err != nil {
		proxyConn.Close()
		return err
	}
	defer f.Close()

	_, err = p2p.CopyWithProgress(proxyConn, f, fileSize, onProgress)
	proxyConn.Close()
	if err != nil {
		return err
	}

	a.logMsg("filetransfer", "Tor file transfer completed successfully for "+fileName)
	return nil
}

func (a *App) handleFileReceive(fromOnion string, hdr p2p.FileHeader, r io.Reader, closeFn func()) {
	if closeFn != nil {
		defer closeFn()
	}

	a.logMsg("filetransfer", fmt.Sprintf("Receiving file '%s' (%d bytes) from %s...", hdr.Name, hdr.Size, truncateOnion(fromOnion)))

	mediaDir := a.web.MediaDir()
	recvDir := filepath.Join(mediaDir, "recv")
	_ = os.MkdirAll(recvDir, 0755)

	safeName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(hdr.Name))
	destPath := filepath.Join(recvDir, safeName)

	f, err := os.Create(destPath)
	if err != nil {
		a.logMsg("filetransfer-error", "Failed to create destination file: "+err.Error())
		return
	}
	defer f.Close()

	startTime := time.Now()

	// Broadcast starting progress event
	a.web.Broadcast(webserver.PushEvent{
		Type: "file_progress",
		Payload: webserver.ProgressEvent{
			ID:    hdr.ID,
			From:  fromOnion,
			Name:  hdr.Name,
			Recv:  0,
			Total: hdr.Size,
		},
	})

	// Copy bytes with progress callback
	_, err = p2p.CopyWithProgress(f, r, hdr.Size, func(bytesWritten int64) {
		elapsed := time.Since(startTime).Seconds()
		var speed float64
		if elapsed > 0.01 {
			speed = float64(bytesWritten) / 1024 / elapsed
		}
		a.web.Broadcast(webserver.PushEvent{
			Type: "file_progress",
			Payload: webserver.ProgressEvent{
				ID:       hdr.ID,
				From:     fromOnion,
				Name:     hdr.Name,
				Recv:     bytesWritten,
				Total:    hdr.Size,
				SpeedKBs: speed,
				Done:     bytesWritten >= hdr.Size,
			},
		})
	})

	if err != nil {
		a.logMsg("filetransfer-error", "File transfer interrupted: "+err.Error())
		os.Remove(destPath) // clean up partial file
		return
	}

	a.logMsg("filetransfer", "File received successfully: "+hdr.Name)

	// Trigger on-complete database insertion and UI reload
	relPath := filepath.Join("recv", safeName)

	// Save message to DB
	displayMsg := &store.Message{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		From:      fromOnion,
		To:        "me",
		Body:      fmt.Sprintf("[media-recv:%s]", relPath),
		MediaName: hdr.Name,
		MediaType: hdr.Mime,
		Timestamp: time.Now(),
	}
	_ = a.db.SaveMessage(displayMsg)

	// Push messages history update
	if a.web != nil {
		a.web.Broadcast(webserver.PushEvent{
			Type: "file_progress",
			Payload: webserver.ProgressEvent{
				ID:    hdr.ID,
				From:  fromOnion,
				Name:  hdr.Name,
				Recv:  hdr.Size,
				Total: hdr.Size,
				Done:  true,
			},
		})
		a.web.PushMessage(fromOnion, a.getHistory(fromOnion))
	}
}

