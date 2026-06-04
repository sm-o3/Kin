# Kin Network Setup Guide

This guide explains how to configure your system for reliable LAN discovery and P2P
connectivity between Android (Termux) and Linux desktop.

---

## How LAN Discovery Works

Kin uses **subnet UDP broadcast** on port `19192` for automatic local peer discovery.

Every 10 seconds each running Kin instance:
1. Broadcasts a JSON announcement to all local subnet broadcast addresses (e.g. `10.42.0.255:19192`)
2. Listens on `:19192` for announcements from other peers
3. When a new peer is heard, automatically adds them to the contacts list and dials them

### Why Android Needs Special Handling

On Android 10+ (Termux), SELinux restricts access to `/proc/net/dev`, which blocks standard
Go runtime APIs like `net.Interfaces()` and `net.InterfaceAddrs()`. This causes two problems:

| Problem | Effect |
|---|---|
| `net.InterfaceAddrs()` blocked | libp2p only binds to `127.0.0.1` (loopback), making it unreachable from other devices |
| `net.Interfaces()` blocked | `getBroadcastAddrs()` falls back to `255.255.255.255`, which Android routes over cellular data — never over Wi-Fi |

**Kin's fix:** Both `getLocalIPs()` and `getBroadcastAddrs()` fall back to parsing `ifconfig`
output when standard APIs are blocked. This correctly resolves:
- The device's Wi-Fi IP (e.g. `10.42.0.35`) → passed explicitly to `libp2p.New()`
- The subnet broadcast address (e.g. `10.42.0.255`) → used for LAN announcements

---

## PC (Linux) Firewall Configuration

On Fedora/RHEL with `firewalld`, the hotspot interface (`wlp3s0`) is assigned to the
`nm-shared` zone. This zone blocks all incoming traffic by default except DHCP, DNS, and SSH.

**Check your zone:**
```bash
firewall-cmd --get-active-zones
# Expected output:
# nm-shared
#   interfaces: wlp3s0
```

**Open required ports:**
```bash
# Discovery broadcast (required for phone → PC detection)
sudo firewall-cmd --zone=nm-shared --add-port=19192/udp

# libp2p P2P connections (required for actual chat over LAN)
sudo firewall-cmd --zone=nm-shared --add-port=1024-65535/tcp
sudo firewall-cmd --zone=nm-shared --add-port=1024-65535/udp

# Persist across reboots
sudo firewall-cmd --runtime-to-permanent
```

**For Ubuntu / Debian with ufw:**
```bash
# Replace 10.42.0.0/24 with your hotspot subnet
sudo ufw allow 19192/udp
sudo ufw allow proto tcp from 10.42.0.0/24
sudo ufw allow proto udp from 10.42.0.0/24
sudo ufw reload
```

**For iptables directly:**
```bash
# Replace wlp3s0 and 10.42.0.0/24 with your values
sudo iptables -A INPUT -i wlp3s0 -p udp --dport 19192 -j ACCEPT
sudo iptables -A INPUT -i wlp3s0 -p tcp --dport 1024:65535 -j ACCEPT
sudo iptables -A INPUT -i wlp3s0 -p udp --dport 1024:65535 -j ACCEPT
```

---

## Typical Setup: PC Hotspot + Android Phone

```
PC (Linux)          Android (Termux)
10.42.0.1     ←→   10.42.0.35
  wlp3s0              wlan0
  kin --web           kin --web
  :8080               :8080
```

**Step-by-step:**

1. Enable Wi-Fi hotspot on your PC (NetworkManager → Hotspot)
2. Connect your Android phone to the PC's hotspot
3. Open firewall ports on the PC (see above)
4. Start Kin on the PC: `./kin --web`
5. Start Kin on Android via Termux: `kin --web`
6. Within 10 seconds both devices appear in each other's contacts list

**Access the Android Web UI from the PC:**
```
http://10.42.0.35:8080
```

**Access the PC Web UI from Android:**
```
http://10.42.0.1:8080
```

---

## Verifying Discovery

### Check if LAN broadcast is being received (PC)

Stop `kin` and run:
```bash
python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(('', 19192))
s.settimeout(15)
print('Listening on :19192 for 15s...')
try:
    data, addr = s.recvfrom(65535)
    print('Received from', addr)
    print(data.decode())
except TimeoutError:
    print('No packets received — check firewall or phone broadcast')
"
```

If no packets arrive within 15 seconds, the firewall is blocking them.

### Check if libp2p is binding to the correct interface (Android)

Look at the startup log:
```
[libp2p] Listening on: /ip4/10.42.0.35/tcp/37003/p2p/12D3KooW...
```

If you only see `127.0.0.1` and no `10.42.0.x` address, `getLocalIPs()` didn't find the
Wi-Fi interface via `ifconfig`. Check that `ifconfig` is available in Termux:
```bash
which ifconfig
# Should output: /system/bin/ifconfig or /data/data/com.termux/files/usr/bin/ifconfig
```

### Check active connections (PC)

```bash
ss -lupn | grep 19192   # Should show kin listening
```

---

## DHT Resolution (Global Internet)

To connect to a peer not on your LAN:

1. They share their **libp2p Peer ID** (shown in Web UI sidebar as `PEER ID: 12D3KooW...`)
2. You enter it in Web UI → Settings panel → "Libp2p Peer ID to Resolve"
3. Kin queries multiple IPFS public gateways to resolve their multiaddresses:
   - `https://ipfs.io/api/v0/dht/findpeer?arg=<peerID>`
   - `https://dweb.link/api/v0/dht/findpeer?arg=<peerID>`
   - `https://gateway.ipfs.io/api/v0/dht/findpeer?arg=<peerID>`
4. Dials the resolved addresses directly

> **Note:** DHT resolution requires the target peer to be reachable on the public internet
> and to have bootstrapped to the IPFS DHT. Peers only on a private LAN are not resolvable
> via DHT — use LAN discovery for those.

---

## Tor (Optional, for Internet Privacy)

When Tor bootstraps successfully, your `.onion` address is shown in the sidebar.
You can share this address with anyone — they can message you through Tor from anywhere.

**Tor failure is non-fatal:** If Tor fails to start (e.g. port 9950 already in use by
another Tor instance), Kin logs the warning and continues with LAN + DHT only.

**If Tor port is already in use:**
```bash
# Find the existing Tor process
ps aux | grep tor

# Kin's own Tor uses port 9950 — if something else has it, kill it:
pkill -f "tor -f.*torrc"
```

---

## Clipboard in Web UI

When accessing the Web UI over HTTP (not localhost), the browser may restrict the
`navigator.clipboard` API (secure context only).

Kin works around this automatically: clicking the TOR ONION or PEER ID boxes uses a
`textarea`-based fallback that works in all contexts, including `http://10.42.0.x:8080`
accessed from a phone.
