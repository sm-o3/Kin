// Package store manages persistent storage using bbolt.
// Stores: contacts, message history, local identity.
package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketContacts = []byte("contacts")
	bucketMessages = []byte("messages")
	bucketIdentity = []byte("identity")
)

// Contact represents a known peer.
type Contact struct {
	ID          string    `json:"id"`        // .onion address (permanent identity)
	Nickname    string    `json:"nickname"`  // display name
	AddedAt     time.Time `json:"added_at"`
	LastSeen    time.Time `json:"last_seen"`
	Libp2pID    string    `json:"libp2p_id,omitempty"`
	Libp2pAddrs []string  `json:"libp2p_addrs,omitempty"`
}

// Message is a single chat message.
type Message struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`        // .onion addr or "me"
	To          string    `json:"to"`          // .onion addr
	Body        string    `json:"body"`
	Timestamp   time.Time `json:"ts"`
	Read        bool      `json:"read"`
	MediaName   string    `json:"media_name,omitempty"` // original filename for media messages
	MediaType   string    `json:"media_type,omitempty"` // MIME type
	ReplyToID   string    `json:"reply_to_id,omitempty"`
	ReplyToBody string    `json:"reply_to_body,omitempty"`
	Starred     bool      `json:"starred,omitempty"`
}

// DB wraps bbolt with typed accessors.
type DB struct {
	bdb *bolt.DB
}

// Open opens (or creates) the database at path.
func Open(path string) (*DB, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketContacts, bucketMessages, bucketIdentity} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{bdb: db}, nil
}

// Close closes the database.
func (d *DB) Close() error {
	return d.bdb.Close()
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// SetIdentity stores the local onion address and nickname.
func (d *DB) SetIdentity(onion, nickname string) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIdentity)
		b.Put([]byte("onion"), []byte(onion))
		b.Put([]byte("nickname"), []byte(nickname))
		return nil
	})
}

// GetIdentity retrieves the local identity.
func (d *DB) GetIdentity() (onion, nickname string) {
	d.bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIdentity)
		onion = string(b.Get([]byte("onion")))
		nickname = string(b.Get([]byte("nickname")))
		return nil
	})
	return
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

// SaveContact upserts a contact.
func (d *DB) SaveContact(c *Contact) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketContacts)
		v, _ := json.Marshal(c)
		return b.Put([]byte(c.ID), v)
	})
}

// GetContact fetches a contact by onion address.
func (d *DB) GetContact(id string) (*Contact, error) {
	var c Contact
	err := d.bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketContacts)
		v := b.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("store: contact not found: %s", id)
		}
		return json.Unmarshal(v, &c)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AllContacts returns all contacts sorted by nickname.
func (d *DB) AllContacts() ([]*Contact, error) {
	var contacts []*Contact
	err := d.bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketContacts)
		return b.ForEach(func(k, v []byte) error {
			if len(k) == 0 || string(k) == "" {
				return nil
			}
			var c Contact
			if err := json.Unmarshal(v, &c); err != nil {
				return nil
			}
			if c.ID == "" {
				return nil
			}
			contacts = append(contacts, &c)
			return nil
		})
	})
	return contacts, err
}

// DeleteContact removes a contact.
func (d *DB) DeleteContact(id string) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketContacts).Delete([]byte(id))
	})
}

// ClearMessages deletes all messages associated with a peer.
func (d *DB) ClearMessages(peerID string) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		c := b.Cursor()
		prefix := []byte(peerID + "/")
		// Since we modify the bucket while iterating, we need to collect keys first
		var keysToDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix); k, _ = c.Next() {
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			keysToDelete = append(keysToDelete, keyCopy)
		}
		for _, k := range keysToDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// key format: "peerID/msgID"
func msgKey(peer, msgID string) []byte {
	return []byte(peer + "/" + msgID)
}

// SaveMessage stores a message.
func (d *DB) SaveMessage(m *Message) error {
	// Parse reply prefix if present
	if strings.HasPrefix(m.Body, "[reply:") {
		idx := strings.Index(m.Body, "]")
		if idx > 7 {
			prefix := m.Body[7:idx]
			parts := strings.SplitN(prefix, ":", 2)
			if len(parts) == 2 {
				m.ReplyToID = parts[0]
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err == nil {
					m.ReplyToBody = string(decoded)
				}
				m.Body = m.Body[idx+1:]
			}
		}
	}

	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		v, _ := json.Marshal(m)
		return b.Put(msgKey(peerFor(m), m.ID), v)
	})
}

func peerFor(m *Message) string {
	if m.From == "me" {
		return m.To
	}
	return m.From
}

// GetMessages returns all messages with a peer, ordered by timestamp.
func (d *DB) GetMessages(peerID string) ([]*Message, error) {
	prefix := []byte(peerID + "/")
	var msgs []*Message
	err := d.bdb.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketMessages).Cursor()
		for k, v := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = c.Next() {
			var m Message
			if err := json.Unmarshal(v, &m); err != nil {
				continue
			}
			msgs = append(msgs, &m)
		}
		return nil
	})
	return msgs, err
}

// MarkRead marks all messages from a peer as read.
func (d *DB) MarkRead(peerID string) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		c := b.Cursor()
		prefix := []byte(peerID + "/")
		for k, v := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = c.Next() {
			var m Message
			if json.Unmarshal(v, &m) != nil {
				continue
			}
			m.Read = true
			updated, _ := json.Marshal(&m)
			b.Put(k, updated)
		}
		return nil
	})
}

// UnreadCount returns the number of unread messages from a peer.
func (d *DB) UnreadCount(peerID string) int {
	count := 0
	d.bdb.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketMessages).Cursor()
		prefix := []byte(peerID + "/")
		for k, v := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = c.Next() {
			var m Message
			if json.Unmarshal(v, &m) != nil {
				continue
			}
			if !m.Read && m.From != "me" {
				count++
			}
		}
		return nil
	})
	return count
}

// SaveLibp2pKey stores the marshaled libp2p private key.
func (d *DB) SaveLibp2pKey(keyBytes []byte) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIdentity)
		return b.Put([]byte("libp2p_privkey"), keyBytes)
	})
}

// GetLibp2pKey retrieves the marshaled libp2p private key.
func (d *DB) GetLibp2pKey() []byte {
	var keyBytes []byte
	d.bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIdentity)
		v := b.Get([]byte("libp2p_privkey"))
		if v != nil {
			keyBytes = make([]byte, len(v))
			copy(keyBytes, v)
		}
		return nil
	})
	return keyBytes
}

// GetContactByLibp2pID finds a contact by their libp2p Peer ID.
func (d *DB) GetContactByLibp2pID(libp2pID string) (*Contact, error) {
	if libp2pID == "" {
		return nil, fmt.Errorf("empty peer ID")
	}
	var match *Contact
	err := d.bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketContacts)
		return b.ForEach(func(k, v []byte) error {
			var c Contact
			if err := json.Unmarshal(v, &c); err == nil {
				if c.Libp2pID == libp2pID {
					match = &c
					return fmt.Errorf("found") // stop iteration
				}
			}
			return nil
		})
	})
	if match != nil {
		return match, nil
	}
	if err != nil && err.Error() == "found" {
		return match, nil
	}
	return nil, fmt.Errorf("contact not found for libp2p ID: %s", libp2pID)
}

// GetMessagesPaginated returns messages with a peer, starting from offset (0 = newest) up to limit, ordered chronologically.
func (d *DB) GetMessagesPaginated(peerID string, offset, limit int) ([]*Message, error) {
	prefix := []byte(peerID + "/")
	var msgs []*Message
	err := d.bdb.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketMessages).Cursor()
		var allKeys [][]byte
		var allVals [][]byte
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			allKeys = append(allKeys, k)
			allVals = append(allVals, v)
		}
		total := len(allKeys)
		if total == 0 {
			return nil
		}
		start := total - offset - limit
		end := total - offset
		if start < 0 {
			start = 0
		}
		if end < 0 {
			end = 0
		}
		if start >= end {
			return nil
		}
		for i := start; i < end; i++ {
			var m Message
			if err := json.Unmarshal(allVals[i], &m); err == nil {
				msgs = append(msgs, &m)
			}
		}
		return nil
	})
	return msgs, err
}

// DeleteMessage deletes a single message.
func (d *DB) DeleteMessage(peer, msgID string) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		return b.Delete(msgKey(peer, msgID))
	})
}

// StarMessage toggles/sets the starred state of a message.
func (d *DB) StarMessage(peer, msgID string, starred bool) error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMessages)
		k := msgKey(peer, msgID)
		v := b.Get(k)
		if v == nil {
			return fmt.Errorf("message not found")
		}
		var m Message
		if err := json.Unmarshal(v, &m); err != nil {
			return err
		}
		m.Starred = starred
		updated, _ := json.Marshal(&m)
		return b.Put(k, updated)
	})
}

// ClearAllData removes all contacts and messages from the database.
func (d *DB) ClearAllData() error {
	return d.bdb.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketContacts); err == nil {
			_, _ = tx.CreateBucketIfNotExists(bucketContacts)
		}
		if err := tx.DeleteBucket(bucketMessages); err == nil {
			_, _ = tx.CreateBucketIfNotExists(bucketMessages)
		}
		return nil
	})
}

// Compact copies the database to another file to compact it and reopens it.
func (d *DB) Compact(dbPath string) error {
	tempPath := dbPath + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer f.Close()

	err = d.bdb.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(f)
		return err
	})
	if err != nil {
		os.Remove(tempPath)
		return err
	}
	f.Close()

	d.bdb.Close()

	if err := os.Rename(tempPath, dbPath); err != nil {
		return err
	}

	newBdb, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	d.bdb = newBdb
	return nil
}
