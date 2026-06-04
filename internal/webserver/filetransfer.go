package webserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const chunkSize = 32 * 1024 // 32 KB per chunk

// ---------------------------------------------------------------------------
// Transfer metadata (header message payload)
// ---------------------------------------------------------------------------

type FileHeader struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Chunks int    `json:"chunks"`
}

// ---------------------------------------------------------------------------
// Pending receive — assembles inbound chunks
// ---------------------------------------------------------------------------

type pendingRecv struct {
	Header    FileHeader
	From      string
	Chunks    map[int][]byte
	StartedAt time.Time
}

func (p *pendingRecv) bytesReceived() int64 {
	var n int64
	for _, b := range p.Chunks {
		n += int64(len(b))
	}
	return n
}

func (p *pendingRecv) speedKBs() float64 {
	elapsed := time.Since(p.StartedAt).Seconds()
	if elapsed < 0.01 {
		return 0
	}
	return float64(p.bytesReceived()) / 1024 / elapsed
}

func (p *pendingRecv) complete() bool {
	return len(p.Chunks) == p.Header.Chunks
}

// ---------------------------------------------------------------------------
// FileTransferManager
// ---------------------------------------------------------------------------

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

// OnComplete is called when a file is fully received.
type OnComplete func(from, localPath, origName, mimeType string)

type FileTransferManager struct {
	mu       sync.Mutex
	pending  map[string]*pendingRecv
	mediaDir string
	onDone   OnComplete
}

func NewFileTransferManager(mediaDir string, onDone OnComplete) *FileTransferManager {
	return &FileTransferManager{
		pending:  make(map[string]*pendingRecv),
		mediaDir: mediaDir,
		onDone:   onDone,
	}
}

// ---------------------------------------------------------------------------
// Parsing incoming messages
// ---------------------------------------------------------------------------

// HandleStart processes a [fstart:ID:JSON] message.
// Returns the transfer ID and a progress event for the UI, or nil if not a fstart.
func (m *FileTransferManager) HandleStart(from, body string) (string, *ProgressEvent) {
	// [fstart:ID:JSON_HEADER]
	if len(body) < 8 || body[:8] != "[fstart:" {
		return "", nil
	}
	inner := body[8 : len(body)-1] // strip "[fstart:" and "]"
	colon := indexOf(inner, ':')
	if colon < 0 {
		return "", nil
	}
	id := inner[:colon]
	var hdr FileHeader
	if err := json.Unmarshal([]byte(inner[colon+1:]), &hdr); err != nil {
		return "", nil
	}
	m.mu.Lock()
	m.pending[id] = &pendingRecv{
		Header:    hdr,
		From:      from,
		Chunks:    make(map[int][]byte),
		StartedAt: time.Now(),
	}
	m.mu.Unlock()
	return id, &ProgressEvent{
		ID: id, From: from, Name: hdr.Name,
		Recv: 0, Total: hdr.Size,
	}
}

// HandleChunk processes a [fchunk:ID:INDEX:B64] message.
// Returns progress event or nil.
func (m *FileTransferManager) HandleChunk(from, body string) (string, *ProgressEvent) {
	if len(body) < 8 || body[:8] != "[fchunk:" {
		return "", nil
	}
	inner := body[8 : len(body)-1]

	// split: ID:INDEX:B64
	c1 := indexOf(inner, ':')
	if c1 < 0 { return "", nil }
	id := inner[:c1]
	rest := inner[c1+1:]

	c2 := indexOf(rest, ':')
	if c2 < 0 { return "", nil }
	var idx int
	fmt.Sscanf(rest[:c2], "%d", &idx)
	b64data := rest[c2+1:]

	data, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil { return "", nil }

	m.mu.Lock()
	p, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return "", nil
	}
	p.Chunks[idx] = data
	recv   := p.bytesReceived()
	speed  := p.speedKBs()
	total  := p.Header.Size
	name   := p.Header.Name
	done   := p.complete()
	mime   := p.Header.Mime
	m.mu.Unlock()

	ev := &ProgressEvent{
		ID: id, From: from, Name: name,
		Recv: recv, Total: total, SpeedKBs: speed,
	}

	if done {
		savePath := m.assemble(id)
		ev.Done     = true
		ev.SavePath = savePath
		if m.onDone != nil && savePath != "" {
			go m.onDone(from, savePath, name, mime)
		}
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}
	return id, ev
}

// HandleEnd processes [fend:ID] — triggers assembly if chunks arrived out of order.
func (m *FileTransferManager) HandleEnd(body string) (string, string) {
	if len(body) < 7 || body[:7] != "[fend:]" && body[:6] != "[fend:" {
		return "", ""
	}
	id := body[6 : len(body)-1]
	m.mu.Lock()
	p, ok := m.pending[id]
	if !ok { m.mu.Unlock(); return id, "" }
	done := p.complete()
	name := p.Header.Name
	mime := p.Header.Mime
	from := p.From
	m.mu.Unlock()

	if done {
		savePath := m.assemble(id)
		if m.onDone != nil && savePath != "" {
			go m.onDone(from, savePath, name, mime)
		}
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return id, savePath
	}
	return id, ""
}

// MissingChunks returns the indices of chunks not yet received for resume.
func (m *FileTransferManager) MissingChunks(id string) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok { return nil }
	var missing []int
	for i := 0; i < p.Header.Chunks; i++ {
		if _, has := p.Chunks[i]; !has {
			missing = append(missing, i)
		}
	}
	return missing
}

// ---------------------------------------------------------------------------
// Assembly
// ---------------------------------------------------------------------------

func (m *FileTransferManager) assemble(id string) string {
	m.mu.Lock()
	p, ok := m.pending[id]
	if !ok { m.mu.Unlock(); return "" }
	header := p.Header
	chunks := p.Chunks
	m.mu.Unlock()

	recvDir := filepath.Join(m.mediaDir, "recv")
	_ = os.MkdirAll(recvDir, 0755)
	safeName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(header.Name))
	dest := filepath.Join(recvDir, safeName)

	f, err := os.Create(dest)
	if err != nil { return "" }
	defer f.Close()

	for i := 0; i < header.Chunks; i++ {
		chunk, ok := chunks[i]
		if !ok { continue } // should not happen if complete() is true
		f.Write(chunk)
	}
	return filepath.Join("recv", safeName)
}

// ---------------------------------------------------------------------------
// Sender side — build wire messages from a file
// ---------------------------------------------------------------------------

// BuildTransferMessages reads a file and returns the sequence of messages
// to send over the P2P channel, plus the local save path.
func BuildTransferMessages(filePath, origName, mimeType string) ([]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil { return nil, err }

	total     := len(data)
	numChunks := (total + chunkSize - 1) / chunkSize
	if numChunks == 0 { numChunks = 1 }

	id := fmt.Sprintf("ft%d", time.Now().UnixNano())

	hdr, _ := json.Marshal(FileHeader{
		Name: origName, Mime: mimeType,
		Size: int64(total), Chunks: numChunks,
	})

	msgs := make([]string, 0, numChunks+2)
	msgs = append(msgs, fmt.Sprintf("[fstart:%s:%s]", id, string(hdr)))

	for i := 0; i < numChunks; i++ {
		lo := i * chunkSize
		hi := lo + chunkSize
		if hi > total { hi = total }
		b64 := base64.StdEncoding.EncodeToString(data[lo:hi])
		msgs = append(msgs, fmt.Sprintf("[fchunk:%s:%d:%s]", id, i, b64))
	}

	msgs = append(msgs, fmt.Sprintf("[fend:%s]", id))
	return msgs, nil
}

// ---------------------------------------------------------------------------
// Util
// ---------------------------------------------------------------------------

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c { return i }
	}
	return -1
}
