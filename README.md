# Kin — Privacy-First P2P Chat

**Kin** is a terminal-first, privacy-first P2P chat application that runs on both **Linux desktop** and **Android via Termux** (ARM64). It works completely without any central server.

- **Tor identity** — your permanent `.onion` address, no registration, no phone number
- **LAN discovery** — automatic peer discovery over local Wi-Fi with zero configuration
- **DHT resolution** — find peers by their libp2p Peer ID via IPFS public gateways
- **Web UI** — optional browser interface accessible from any device on the same network
- **Bundled Tor** — no `pkg install tor` needed; Tor binary is embedded in the app

```
┌─────────────────────────────────────────────────────────────────┐
│ ● online   Kin                                          [+]     │
├──────────────┬──────────────────────────────────────────────────┤
│ Search       │                                                  │
│ ┌──────────┐ │              Select a conversation to start      │
│ │ Alice    │ │                                                  │
│ │ Bob(LAN) │ │                   [Create Contact]               │
│ └──────────┘ │                                                  │
│              │                                                  │
├──────────────┴──────────────────────────────────────────────────┤
│ TOR ONION: abc123xyz…   PEER ID: 12D3KooW…      ⚙  Connected  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        TRANSPORT LAYERS                         │
│                                                                 │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────┐  │
│  │  Tor Hidden  │    │  libp2p P2P  │    │   LAN Subnet     │  │
│  │   Service    │    │  (TCP+QUIC)  │    │   Broadcast UDP  │  │
│  │  (Internet)  │    │  (Internet)  │    │   port 19192     │  │
│  └──────┬───────┘    └──────┬───────┘    └────────┬─────────┘  │
│         │                  │                     │             │
│         └──────────────────┴─────────────────────┘             │
│                            │                                   │
│                    ┌───────▼────────┐                          │
│                    │   P2P Engine   │                          │
│                    │  (internal/p2p)│                          │
│                    └───────┬────────┘                          │
│                            │                                   │
│              ┌─────────────┼─────────────┐                     │
│              ▼             ▼             ▼                     │
│         ┌────────┐   ┌──────────┐  ┌─────────┐               │
│         │  TUI   │   │  Web UI  │  │  Store  │               │
│         │ Bubble │   │ http/ws  │  │  BBolt  │               │
│         │  Tea   │   │  :8080   │  │  DB     │               │
│         └────────┘   └──────────┘  └─────────┘               │
└─────────────────────────────────────────────────────────────────┘
```

| Layer | Implementation |
|---|---|
| **Identity** | Tor v3 onion address (Ed25519, permanent, no server) |
| **LAN Discovery** | Multi-interface subnet UDP broadcast on port 19192 |
| **P2P Transport** | libp2p over TCP + QUIC-v1 with DHT bootstrapping |
| **Internet Relay** | Tor hidden service + libp2p circuit relay |
| **Storage** | BBolt embedded key-value (contacts, chat history, keys) |
| **TUI** | BubbleTea + Bubbles + Lipgloss |
| **Web UI** | Vanilla HTML/CSS/JS served over HTTP with WebSocket push |

---

## Installation

### Termux (Android ARM64)

```bash
# Install Go (Tor is bundled — no separate install needed)
pkg update && pkg install golang git

# Clone and build
git clone <repo> && cd Kin
go build -ldflags "-checklinkname=0" -o kin .

# Install system-wide
cp kin $PREFIX/bin/kin

# Run
kin
# or with Web UI:
kin --web
```

### Linux Desktop (amd64)

```bash
git clone <repo> && cd Kin
go build -o kin .

# Run TUI
./kin

# Run Web UI (accessible at http://127.0.0.1:8080)
./kin --web
```

### Cross-compile (PC → Android)

```bash
# Build ARM64 binary on PC
make arm64            # produces kin-arm64

# Copy to Android via SSH (Termux SSHD)
scp -P 8022 kin-arm64 user@<phone-ip>:~/kin
ssh -p 8022 user@<phone-ip> "cp ~/kin \$PREFIX/bin/kin && chmod +x \$PREFIX/bin/kin"
```

---

## Running Modes

### TUI Mode (default)

```bash
kin
```

Full terminal UI with contacts list, chat view, and keyboard shortcuts.

| Key | Action |
|---|---|
| `↑/↓` or `j/k` | Navigate contacts |
| `Enter` | Send message |
| `Ctrl+N` | Add contact |
| `Ctrl+P` | Connect to selected peer |
| `Ctrl+S` | Settings / copy your onion address |
| `Ctrl+Q` | Quit |
| `Ctrl+D` | Insert newline in message |

### Web UI Mode

```bash
kin --web
```

Starts a headless backend and serves the Web UI at `http://127.0.0.1:8080`.

**Accessing from another device on your LAN:**
```
http://<your-ip>:8080
```
e.g. `http://10.42.0.1:8080` from a phone connected to your PC's hotspot.

---

## P2P Networking

### LAN Discovery (Automatic)

Kin automatically announces itself on the local network using **subnet UDP broadcast** every 10 seconds.

- Sends to all local subnet broadcast addresses (e.g. `10.42.0.255:19192`)
- Falls back to parsing `ifconfig` output on Android where `/proc/net` is restricted
- PC discovers phone, phone discovers PC — **zero configuration required**
- Newly discovered peers appear automatically in the contacts list

**Required for PC ↔ Phone discovery (Fedora/firewalld):**
```bash
# Open the discovery port in the hotspot zone
sudo firewall-cmd --zone=nm-shared --add-port=19192/udp

# Also open libp2p P2P connection ports (TCP + UDP)
sudo firewall-cmd --zone=nm-shared --add-port=1024-65535/tcp
sudo firewall-cmd --zone=nm-shared --add-port=1024-65535/udp

# Make permanent
sudo firewall-cmd --runtime-to-permanent
```

**For Ubuntu/ufw:**
```bash
sudo ufw allow 19192/udp
sudo ufw allow proto tcp from 10.42.0.0/24
sudo ufw allow proto udp from 10.42.0.0/24
```

### DHT Resolution (Global)

Find a peer anywhere on the internet by their **libp2p Peer ID**:

1. In the Web UI → Settings → "Libp2p Peer ID to Resolve" field
2. Enter the full Peer ID (e.g. `12D3KooWR3fMkpV2AQoZ...`)
3. Kin queries IPFS public gateways (`ipfs.io`, `dweb.link`, `gateway.ipfs.io`) to resolve multiaddresses
4. Dials the peer directly

### Tor Hidden Service (Internet)

Your `.onion` address is shown in the sidebar. Share it with contacts who are not on your LAN. Kin will route messages through the Tor network automatically.

If Tor fails to start (e.g. address already in use), Kin continues to operate over LAN and DHT — Tor failure is non-fatal.

---

## Bundled Tor

The `assets/bin/` directory holds prebuilt Tor binaries embedded via `//go:embed`:

```
assets/
  assets.go              ← go:embed declarations
  bin/
    tor-android-arm64    ← Termux aarch64 (NDK-built, Android 7+)
    tor-linux-amd64      ← Linux desktop x86_64
```

On first launch, Kin extracts the correct binary to `~/.kin/tor/bin/tor`, writes a `torrc`, and starts Tor. On subsequent runs the binary size is compared — re-extraction is skipped if unchanged.

### Updating bundled Tor

```bash
# Fetch latest Tor from packages.termux.dev and update assets/bin/
make update-tor

# Rebuild
make arm64
```

---

## Data & Privacy

All data is stored locally in `~/.kin/`:

```
~/.kin/
  kin.db                          ← BBolt database (contacts, messages, keys)
  tor/
    torrc                         ← Tor configuration
    bin/tor                       ← Extracted Tor binary
    hidden_service/
      hostname                    ← Your .onion address
      private_key                 ← Your permanent identity key (Ed25519)
```

- **No central server** — signaling is peer-to-peer
- **Permanent identity** — your onion address never changes
- **Encrypted transport** — Tor provides end-to-end encryption for onion routing
- **Database mode** `0600` — only readable by your user

---

## Build Reference

```bash
make build        # Build for current host (amd64)
make arm64        # Build for Android ARM64 (Termux)
make install      # arm64 → $PREFIX/bin/kin  (run inside Termux)
make update-tor   # Refresh bundled Tor binaries from Termux repo
make fmt          # gofmt
make vet          # go vet
make clean        # Remove built binaries
```

---

## Packages Used

- [`github.com/charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea) — TUI framework
- [`github.com/charmbracelet/bubbles`](https://github.com/charmbracelet/bubbles) — TUI components
- [`github.com/charmbracelet/lipgloss`](https://github.com/charmbracelet/lipgloss) — TUI styling
- [`github.com/libp2p/go-libp2p`](https://github.com/libp2p/go-libp2p) — P2P networking
- [`github.com/multiformats/go-multiaddr`](https://github.com/multiformats/go-multiaddr) — Multiaddr parsing
- [`go.etcd.io/bbolt`](https://github.com/etcd-io/bbolt) — Embedded key-value storage

Tor management, LAN broadcast, DHT resolution, and all networking glue code is written from scratch.
