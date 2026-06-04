package webserver

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"kin/internal/store"
)

//go:embed static
var staticFiles embed.FS

// ---------------------------------------------------------------------------
// Callbacks
// ---------------------------------------------------------------------------

type Callbacks struct {
	GetMyOnion           func() string
	GetMyPeerID          func() string
	GetContacts          func() []*store.Contact
	GetMessages          func(peerID string) []*store.Message
	GetMessagesPaginated func(peerID string, offset, limit int) []*store.Message
	SendMessage          func(peerID, body string) error
	SendEphemeralMessage func(peerID, body string) error
	AddContact           func(id, nickname, libp2pID string) error
	DeleteContact        func(id string) error
	ClearChat            func(peerID string) error
	ConnectPeer          func(peerID string)
	GetTorStatus         func() string
	ResolveDHT           func(peerID string) (string, error)
	SaveMessage          func(m *store.Message) error
	DeleteMessage        func(peerID, msgID string) error
	StarMessage          func(peerID, msgID string, starred bool) error
	ClearAllData         func() error
	CompactDB            func() error
	SendFile             func(peerID, filePath, fileName, mimeType string, onProgress func(sent int64)) error
}

// PushEvent is broadcast to all WebSocket clients.
type PushEvent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ProgressEvent is sent to web clients via WebSocket.
type ProgressEvent struct {
	ID       string  `json:"id"`
	From     string  `json:"from"`
	Name     string  `json:"name"`
	Recv     int64   `json:"recv"`
	Total    int64   `json:"total"`
	SpeedKBs float64 `json:"speed_kbs"`
	Done     bool    `json:"done"`
	SavePath string  `json:"save_path,omitempty"`
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

type Server struct {
	cb       Callbacks
	port     int
	mediaDir string

	mu      sync.RWMutex
	clients map[*websocket.Conn]*sync.Mutex
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func New(port int, cb Callbacks) *Server {
	dir := defaultMediaDir()
	_ = os.MkdirAll(filepath.Join(dir, "sent"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "recv"), 0755)

	s := &Server{
		cb:       cb,
		port:     port,
		mediaDir: dir,
		clients:  make(map[*websocket.Conn]*sync.Mutex),
	}
	return s
}

func (s *Server) MediaDir() string {
	return s.mediaDir
}

func defaultMediaDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "android" {
		for _, c := range []string{
			"/storage/emulated/0/Download/Kin",
			filepath.Join(home, "storage", "downloads", "Kin"),
		} {
			if _, err := os.Stat(filepath.Dir(c)); err == nil {
				return c
			}
		}
	}
	return filepath.Join(home, "Downloads", "Kin")
}

// ---------------------------------------------------------------------------
// Inbound message routing (called from App.onMsg)
// ---------------------------------------------------------------------------

// HandleInboundMessage checks if body is a file-transfer protocol message.
// Returns (handled bool, progressEvent *ProgressEvent).
func (s *Server) HandleInboundMessage(from, body string) (bool, *ProgressEvent) {
	switch {
	case strings.HasPrefix(body, "[call-sig:"):
		payload := strings.TrimSuffix(strings.TrimPrefix(body, "[call-sig:"), "]")
		s.Broadcast(PushEvent{Type: "call_signaling", Payload: map[string]interface{}{
			"peer": from,
			"data": payload,
		}})
		return true, nil
	case strings.HasPrefix(body, "[call-stream:"):
		payload := strings.TrimSuffix(strings.TrimPrefix(body, "[call-stream:"), "]")
		s.Broadcast(PushEvent{Type: "call_stream", Payload: map[string]interface{}{
			"peer": from,
			"data": payload,
		}})
		return true, nil
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/contacts", s.cors(s.handleContacts))
	mux.HandleFunc("/api/messages", s.cors(s.handleMessages))
	mux.HandleFunc("/api/messages/star", s.cors(s.handleMessageStar))
	mux.HandleFunc("/api/send", s.cors(s.handleSend))
	mux.HandleFunc("/api/connect", s.cors(s.handleConnect))
	mux.HandleFunc("/api/upload", s.cors(s.handleUpload))
	mux.HandleFunc("/api/db/clear", s.cors(s.handleDBClear))
	mux.HandleFunc("/api/db/compact", s.cors(s.handleDBCompact))
	mux.HandleFunc("/api/dht/resolve", s.cors(s.handleDHTResolve))
	mux.HandleFunc("/api/link-preview", s.cors(s.handleLinkPreview))
	mux.HandleFunc("/api/media/", s.handleMediaServe)

	sub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFiles.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	log.Printf("[webserver] Kin Web UI → http://%s", addr)
	go http.ListenAndServe(addr, mux)
	return nil
}

// ---------------------------------------------------------------------------
// Broadcast helpers
// ---------------------------------------------------------------------------

func (s *Server) Broadcast(ev PushEvent) {
	s.mu.RLock()
	type clientWrite struct {
		conn *websocket.Conn
		mu   *sync.Mutex
	}
	var targets []clientWrite
	for c, m := range s.clients {
		targets = append(targets, clientWrite{conn: c, mu: m})
	}
	s.mu.RUnlock()

	data, _ := json.Marshal(ev)
	for _, target := range targets {
		target.mu.Lock()
		_ = target.conn.WriteMessage(websocket.TextMessage, data)
		target.mu.Unlock()
	}
}

func (s *Server) PushMessage(peerID string, msgs []*store.Message) {
	s.Broadcast(PushEvent{Type: "message", Payload: map[string]interface{}{"peer": peerID, "messages": msgs}})
}
func (s *Server) PushLog(line string) {
	s.Broadcast(PushEvent{Type: "log", Payload: line})
}
func (s *Server) PushContactsUpdate() {
	s.Broadcast(PushEvent{Type: "contacts_updated", Payload: s.cb.GetContacts()})
}
func (s *Server) PushIdentity(onion, peerID string) {
	s.Broadcast(PushEvent{Type: "identity", Payload: map[string]string{"onion": onion, "peer_id": peerID}})
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	connMu := &sync.Mutex{}
	s.mu.Lock()
	s.clients[conn] = connMu
	s.mu.Unlock()
	
	peerID := ""
	if s.cb.GetMyPeerID != nil {
		peerID = s.cb.GetMyPeerID()
	}

	data, _ := json.Marshal(PushEvent{Type: "snapshot", Payload: map[string]interface{}{
		"me": s.cb.GetMyOnion(), "peer_id": peerID, "contacts": s.cb.GetContacts(),
	}})
	connMu.Lock()
	_ = conn.WriteMessage(websocket.TextMessage, data)
	connMu.Unlock()
	defer func() {
		s.mu.Lock(); delete(s.clients, conn); s.mu.Unlock(); conn.Close()
	}()
	for { if _, _, err := conn.ReadMessage(); err != nil { return } }
}

// ---------------------------------------------------------------------------
// CORS + helpers
// ---------------------------------------------------------------------------

func (s *Server) cors(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions { return }
		h(w, r)
	}
}
func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v)
}
func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json"); w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// REST handlers
// ---------------------------------------------------------------------------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"onion": s.cb.GetMyOnion()})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	tor := "offline"
	if s.cb.GetTorStatus != nil { tor = s.cb.GetTorStatus() }
	jsonOK(w, map[string]string{"tor": tor})
}

func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, s.cb.GetContacts())
	case http.MethodPost:
		var req struct {
			ID       string `json:"id"`
			Nickname string `json:"nickname"`
			Libp2pID string `json:"libp2p_id"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
			jsonErr(w, "id required", http.StatusBadRequest); return
		}
		if err := s.cb.AddContact(req.ID, req.Nickname, req.Libp2pID); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		go s.PushContactsUpdate()
		jsonOK(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" { jsonErr(w, "id required", http.StatusBadRequest); return }
		if err := s.cb.DeleteContact(id); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		go s.PushContactsUpdate()
		jsonOK(w, map[string]bool{"ok": true})
	}
}

func (s *Server) handleDHTResolve(w http.ResponseWriter, r *http.Request) {
	peerID := r.URL.Query().Get("peer_id")
	if peerID == "" {
		jsonErr(w, "peer_id required", http.StatusBadRequest)
		return
	}
	if s.cb.ResolveDHT == nil {
		jsonErr(w, "dht resolution callback not registered", http.StatusNotImplemented)
		return
	}
	onion, err := s.cb.ResolveDHT(peerID)
	if err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"onion": onion})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	peer := r.URL.Query().Get("peer")
	if peer == "" { jsonErr(w, "peer required", http.StatusBadRequest); return }
	switch r.Method {
	case http.MethodGet:
		offsetStr := r.URL.Query().Get("offset")
		limitStr := r.URL.Query().Get("limit")
		if offsetStr != "" && limitStr != "" && s.cb.GetMessagesPaginated != nil {
			var offset, limit int
			fmt.Sscanf(offsetStr, "%d", &offset)
			fmt.Sscanf(limitStr, "%d", &limit)
			msgs := s.cb.GetMessagesPaginated(peer, offset, limit)
			if msgs == nil { msgs = []*store.Message{} }
			jsonOK(w, msgs)
			return
		}
		msgs := s.cb.GetMessages(peer)
		if msgs == nil { msgs = []*store.Message{} }
		jsonOK(w, msgs)
	case http.MethodDelete:
		msgID := r.URL.Query().Get("id")
		if msgID != "" {
			if s.cb.DeleteMessage != nil {
				if err := s.cb.DeleteMessage(peer, msgID); err != nil {
					jsonErr(w, err.Error(), http.StatusInternalServerError); return
				}
				s.Broadcast(PushEvent{Type: "message_deleted", Payload: map[string]string{"peer": peer, "id": msgID}})
				jsonOK(w, map[string]bool{"ok": true})
				return
			}
		}
		if err := s.cb.ClearChat(peer); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		s.Broadcast(PushEvent{Type: "chat_cleared", Payload: map[string]string{"peer": peer}})
		jsonOK(w, map[string]bool{"ok": true})
	}
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	var req struct {
		PeerID string `json:"peer_id"`
		Body   string `json:"body"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.PeerID == "" {
		jsonErr(w, "peer_id required", http.StatusBadRequest); return
	}

	if strings.HasPrefix(req.Body, "[call-sig:") || strings.HasPrefix(req.Body, "[call-stream:") {
		if s.cb.SendEphemeralMessage != nil {
			if err := s.cb.SendEphemeralMessage(req.PeerID, req.Body); err != nil {
				jsonErr(w, err.Error(), http.StatusInternalServerError); return
			}
		} else {
			if err := s.cb.SendMessage(req.PeerID, req.Body); err != nil {
				jsonErr(w, err.Error(), http.StatusInternalServerError); return
			}
		}
		jsonOK(w, map[string]bool{"ok": true})
		return
	}

	if err := s.cb.SendMessage(req.PeerID, req.Body); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError); return
	}
	go s.PushMessage(req.PeerID, s.cb.GetMessages(req.PeerID))
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	var req struct{ PeerID string `json:"peer_id"` }
	json.NewDecoder(r.Body).Decode(&req)
	if req.PeerID != "" { go s.cb.ConnectPeer(req.PeerID) }
	jsonOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Chunked file upload — reads file, sends chunks via message channel
// ---------------------------------------------------------------------------

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	peer := r.URL.Query().Get("peer")
	if peer == "" { jsonErr(w, "peer required", http.StatusBadRequest); return }

	// 500 MB max
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		jsonErr(w, "multipart error", http.StatusBadRequest); return
	}
	file, header, err := r.FormFile("file")
	if err != nil { jsonErr(w, "file required", http.StatusBadRequest); return }
	defer file.Close()

	// Save locally first
	safeName := filepath.Base(header.Filename)
	ext      := strings.ToLower(filepath.Ext(safeName))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" { mimeType = "application/octet-stream" }

	sentDir  := filepath.Join(s.mediaDir, "sent")
	_ = os.MkdirAll(sentDir, 0755)
	localName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), safeName)
	localPath := filepath.Join(sentDir, localName)

	// Write to disk via standard streaming copy (no massive buffer allocation!)
	out, err := os.Create(localPath)
	if err != nil {
		jsonErr(w, "save error", http.StatusInternalServerError); return
	}
	fileSize, err := io.Copy(out, file)
	out.Close()
	if err != nil {
		jsonErr(w, "save error", http.StatusInternalServerError); return
	}

	startTime := time.Now()

	// Send in background, push progress events to UI
	go func() {
		if s.cb.SendFile == nil {
			log.Printf("[filetransfer] error: SendFile callback not registered")
			return
		}
		err := s.cb.SendFile(peer, localPath, safeName, mimeType, func(bytesSent int64) {
			elapsed := time.Since(startTime).Seconds()
			speed := float64(bytesSent) / 1024 / elapsed
			if elapsed < 0.01 { speed = 0 }
			s.Broadcast(PushEvent{Type: "file_upload_progress", Payload: map[string]interface{}{
				"name":      safeName,
				"peer":      peer,
				"sent":      bytesSent,
				"total":     fileSize,
				"speed_kbs": speed,
				"done":      bytesSent >= fileSize,
			}})
		})

		if err != nil {
			log.Printf("[filetransfer] send error: %v", err)
			return
		}

		// Save display message for sender
		displayMsg := &store.Message{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
			From:      "me",
			To:        peer,
			Body:      fmt.Sprintf("[media-sent:sent/%s]", localName),
			MediaName: safeName,
			MediaType: mimeType,
			Timestamp: time.Now(),
			Read:      true,
		}
		if s.cb.SaveMessage != nil { _ = s.cb.SaveMessage(displayMsg) }
		s.PushMessage(peer, s.cb.GetMessages(peer))
	}()

	jsonOK(w, map[string]interface{}{
		"ok": true, "name": safeName,
	})
}

// ---------------------------------------------------------------------------
// Media file serving
// ---------------------------------------------------------------------------

func (s *Server) handleMediaServe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	rel := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/api/media/"))
	if strings.Contains(rel, "..") { jsonErr(w, "bad path", http.StatusBadRequest); return }
	full := filepath.Join(s.mediaDir, rel)
	if _, err := os.Stat(full); err != nil { http.NotFound(w, r); return }
	name := filepath.Base(full)
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" { ct = "application/octet-stream" }
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	w.Header().Set("Content-Type", ct)
	http.ServeFile(w, r, full)
}

func (s *Server) handleMessageStar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	var req struct {
		PeerID  string `json:"peer_id"`
		MsgID   string `json:"msg_id"`
		Starred bool   `json:"starred"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.PeerID == "" || req.MsgID == "" {
		jsonErr(w, "peer_id and msg_id required", http.StatusBadRequest); return
	}
	if s.cb.StarMessage != nil {
		if err := s.cb.StarMessage(req.PeerID, req.MsgID, req.Starred); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		s.Broadcast(PushEvent{Type: "message_starred", Payload: map[string]interface{}{
			"peer":    req.PeerID,
			"id":      req.MsgID,
			"starred": req.Starred,
		}})
		jsonOK(w, map[string]bool{"ok": true})
		return
	}
	jsonErr(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleDBClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	if s.cb.ClearAllData != nil {
		if err := s.cb.ClearAllData(); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		s.Broadcast(PushEvent{Type: "db_cleared", Payload: map[string]bool{"ok": true}})
		jsonOK(w, map[string]bool{"ok": true})
		return
	}
	jsonErr(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleDBCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.NotFound(w, r); return }
	if s.cb.CompactDB != nil {
		if err := s.cb.CompactDB(); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError); return
		}
		jsonOK(w, map[string]bool{"ok": true})
		return
	}
	jsonErr(w, "not implemented", http.StatusNotImplemented)
}

// ── Link Preview & Metadata Scraper ────────────────────────────────────────

type LinkMetadata struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	URL         string `json:"url"`
	Icon        string `json:"icon"`
	IsYouTube   bool   `json:"is_youtube"`
	YouTubeID   string `json:"youtube_id"`
	IsImage     bool   `json:"is_image"`
}

func (s *Server) handleLinkPreview(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		jsonErr(w, "url required", http.StatusBadRequest)
		return
	}
	meta := s.fetchLinkPreview(targetURL)
	jsonOK(w, meta)
}

func (s *Server) fetchLinkPreview(targetURL string) LinkMetadata {
	meta := LinkMetadata{
		URL: targetURL,
	}

	ytID := extractYouTubeID(targetURL)
	if ytID != "" {
		meta.IsYouTube = true
		meta.YouTubeID = ytID
	}

	lowerURL := strings.ToLower(targetURL)
	if strings.HasSuffix(lowerURL, ".jpg") || strings.HasSuffix(lowerURL, ".jpeg") ||
		strings.HasSuffix(lowerURL, ".png") || strings.HasSuffix(lowerURL, ".gif") ||
		strings.HasSuffix(lowerURL, ".webp") || strings.HasSuffix(lowerURL, ".svg") {
		meta.IsImage = true
		return meta
	}

	meta.Icon = getFaviconURL(targetURL)

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return meta
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return meta
	}
	defer resp.Body.Close()

	cType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(cType, "image/") {
		meta.IsImage = true
		return meta
	}

	bodyBytes := make([]byte, 1024*1024)
	n, _ := io.ReadFull(resp.Body, bodyBytes)
	bodyStr := string(bodyBytes[:n])

	title, desc, img := parseHTMLMetadata(bodyStr)
	meta.Title = title
	meta.Description = desc
	meta.Image = img

	if meta.Title == "" {
		if parsed, err := url.Parse(targetURL); err == nil {
			meta.Title = parsed.Host
		} else {
			meta.Title = targetURL
		}
	}

	return meta
}

func extractYouTubeID(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if strings.Contains(parsed.Host, "youtube.com") {
		if parsed.Path == "/watch" {
			return parsed.Query().Get("v")
		}
		parts := strings.Split(parsed.Path, "/")
		if len(parts) >= 3 && (parts[1] == "embed" || parts[1] == "v" || parts[1] == "shorts") {
			return parts[2]
		}
	} else if strings.Contains(parsed.Host, "youtu.be") {
		parts := strings.Split(parsed.Path, "/")
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}

func getFaviconURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("https://www.google.com/s2/favicons?sz=64&domain=%s", parsed.Host)
}

func parseHTMLMetadata(htmlContent string) (title, desc, image string) {
	ogTitleReg := regexp.MustCompile(`(?i)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	if m := ogTitleReg.FindStringSubmatch(htmlContent); len(m) > 1 {
		title = m[1]
	}
	if title == "" {
		twTitleReg := regexp.MustCompile(`(?i)<meta\s+[^>]*name=["']twitter:title["'][^>]*content=["']([^"']+)["']`)
		if m := twTitleReg.FindStringSubmatch(htmlContent); len(m) > 1 {
			title = m[1]
		}
	}
	if title == "" {
		titleReg := regexp.MustCompile(`(?i)<title>(.*?)</title>`)
		if m := titleReg.FindStringSubmatch(htmlContent); len(m) > 1 {
			title = m[1]
		}
	}

	ogDescReg := regexp.MustCompile(`(?i)<meta\s+[^>]*property=["']og:description["'][^>]*content=["']([^"']+)["']`)
	if m := ogDescReg.FindStringSubmatch(htmlContent); len(m) > 1 {
		desc = m[1]
	}
	if desc == "" {
		twDescReg := regexp.MustCompile(`(?i)<meta\s+[^>]*name=["']twitter:description["'][^>]*content=["']([^"']+)["']`)
		if m := twDescReg.FindStringSubmatch(htmlContent); len(m) > 1 {
			desc = m[1]
		}
	}
	if desc == "" {
		descReg := regexp.MustCompile(`(?i)<meta\s+[^>]*name=["']description["'][^>]*content=["']([^"']+)["']`)
		if m := descReg.FindStringSubmatch(htmlContent); len(m) > 1 {
			desc = m[1]
		}
	}

	ogImgReg := regexp.MustCompile(`(?i)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)
	if m := ogImgReg.FindStringSubmatch(htmlContent); len(m) > 1 {
		image = m[1]
	}
	if image == "" {
		twImgReg := regexp.MustCompile(`(?i)<meta\s+[^>]*name=["']twitter:image["'][^>]*content=["']([^"']+)["']`)
		if m := twImgReg.FindStringSubmatch(htmlContent); len(m) > 1 {
			image = m[1]
		}
	}

	title = unescapeHTMLSimple(title)
	desc = unescapeHTMLSimple(desc)

	return title, desc, image
}

func unescapeHTMLSimple(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}
