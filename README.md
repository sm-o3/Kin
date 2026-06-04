# Kin — Privacy-First P2P Chat

**Kin** is a serverless, privacy-first P2P chat and file-sharing application designed for **Android (via Termux)**, **Linux**, and **Windows**. It operates entirely peer-to-peer using Tor hidden services and local area network (LAN) broadcasts.

- **Tor identity** — your permanent `.onion` address, no registration, no phone number.
- **LAN discovery** — automatic peer discovery over local Wi-Fi with zero configuration.
- **DHT resolution** — find peers by their libp2p Peer ID via IPFS public gateways.
- **Web UI** — beautiful and responsive browser interface served locally on port `8080`.
- **Bundled Tor** — no manual Tor installation needed; Tor is embedded directly in the app.

---

## Installation

### Termux (Android) & Linux (Debian, Ubuntu, Arch, Fedora)

Install Kin with a single shell command (requires `curl`):

```bash
curl -fsSL https://raw.githubusercontent.com/sm-o3/Kin/main/install.sh | bash
```

This installer automatically installs Go and Git, clones the source, builds the binary, and registers `kin` in your local environment.

### Windows (PowerShell)

Install Kin on Windows with a single PowerShell command (requires PowerShell 5+ or 7+ running as Administrator/User):

```powershell
iwr -useb https://raw.githubusercontent.com/sm-o3/Kin/main/install.ps1 | iex
```

This installer automatically checks for Git and Go (installs them via `winget` if missing), builds `kin.exe`, and adds it to your User PATH.

---

## Running Kin

Start the Kin application by running:

```bash
kin
```

Once started, the Web UI will be automatically hosted locally:

👉 **http://127.0.0.1:8080**

### Accessing from another device on your LAN
To chat from a phone or tablet on the same Wi-Fi network, navigate to:
```
http://<your-computer-ip>:8080
```
*(e.g., `http://192.168.1.42:8080`)*

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
│              ┌─────────────┴─────────────┐                     │
│              ▼                           ▼                     │
│         ┌──────────┐                ┌─────────┐                │
│         │  Web UI  │                │  Store  │                │
│         │ http/ws  │                │  BBolt  │                │
│         │  :8080   │                │  DB     │                │
│         └──────────┘                └─────────┘                │
└─────────────────────────────────────────────────────────────────┘
```

| Layer | Implementation |
|---|---|
| **Identity** | Tor v3 onion address (Ed25519, permanent, no server) |
| **LAN Discovery** | Multi-interface subnet UDP broadcast on port 19192 |
| **P2P Transport** | libp2p over TCP + QUIC-v1 with DHT bootstrapping |
| **Internet Relay** | Tor hidden service + libp2p circuit relay |
| **Storage** | BBolt embedded key-value (contacts, chat history, keys) |
| **Web UI** | Vanilla HTML/CSS/JS served over HTTP with WebSocket push |

---

## P2P Networking

### LAN Discovery (Automatic)
Kin automatically announces itself on the local network using **subnet UDP broadcast** every 10 seconds on port `19192`. Newly discovered peers on your local Wi-Fi automatically populate in the contacts list.

*Note: For PC ↔ Phone discovery, ensure port `19192/udp` and ports `1024-65535/tcp` are allowed in your firewalls (e.g., firewalld/ufw).*

### DHT Resolution (Global)
Find a peer anywhere on the internet by their **libp2p Peer ID** under **Settings** → **Libp2p Peer ID to Resolve**. Kin queries IPFS public gateways to resolve multiaddresses and dials the peer.

### Tor Hidden Service (Internet)
Share your `.onion` address with contacts who are not on your LAN. Kin automatically routes messages securely through the Tor network.

---

## Bundled Tor

Prebuilt Tor binaries are embedded in the Kin binary using Go's `//go:embed` feature:

```
assets/
  assets.go              ← go:embed declarations
  bin/
    tor-android-arm64    ← Termux aarch64 (Android ARM64)
    tor-linux-amd64      ← Linux desktop x86_64
```

On first launch, Kin extracts the binary to `~/.kin/tor/bin/tor`, writes a `torrc`, and starts Tor.

### Updating bundled Tor

```bash
make update-tor   # Refresh bundled Tor binaries from Termux repositories
```

---

## Data & Privacy

All data is stored locally in your home directory under `~/.kin/`:

- **`kin.db`** — BBolt database storing contacts, message logs, and P2P identities (file permissions restricted to `0600`).
- **`tor/hidden_service/private_key`** — Your permanent Tor Ed25519 identity key.

---

## Build Reference

If you prefer to compile manually:

```bash
make build        # Build for current host (amd64)
make arm64        # Build for Android ARM64 (Termux)
make install      # Compile and install to Termux bin path
make clean        # Remove built binaries
```

