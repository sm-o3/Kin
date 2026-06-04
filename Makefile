.PHONY: all build arm64 update-tor clean fmt vet sync

# Set PHONE_IP to your Android device's IP (used by `make sync`)
# Override PHONE_PASS on the command line or environment (e.g., make sync PHONE_PASS=yourpassword)
PHONE_IP     ?= 10.42.0.35
PHONE_PORT   ?= 8022
PHONE_PASS   ?= your_phone_password

TOR_VERSION  := 0.4.9.8
TOR_BASE_URL := https://packages.termux.dev/apt/termux-main/pool/main/t/tor

## Build for current host (amd64)
all: build

build:
	go build -o kin .

## Build for Termux aarch64 (Xiaomi 14 / Android ARM64)
arm64:
	GOOS=android GOARCH=arm64 go build -ldflags "-checklinkname=0" -o kin-arm64 .

## Update embedded Tor binaries from Termux package repository
update-tor:
	@echo "Fetching Tor $(TOR_VERSION) binaries from Termux repo…"
	@mkdir -p /tmp/kin-tor-update
	@# aarch64
	@curl -L -o /tmp/kin-tor-update/aarch64.deb \
	    "$(TOR_BASE_URL)/tor_$(TOR_VERSION)_aarch64.deb"
	@ar -x /tmp/kin-tor-update/aarch64.deb --output /tmp/kin-tor-update/aarch64/
	@mkdir -p /tmp/kin-tor-update/aarch64-extract
	@tar -xJf /tmp/kin-tor-update/aarch64/data.tar.xz \
	    -C /tmp/kin-tor-update/aarch64-extract \
	    --wildcards '*/bin/tor' 2>/dev/null; true
	@find /tmp/kin-tor-update/aarch64-extract -name 'tor' -type f \
	    -exec cp {} assets/bin/tor-android-arm64 \;
	@chmod 0755 assets/bin/tor-android-arm64
	@# x86_64
	@curl -L -o /tmp/kin-tor-update/x86_64.deb \
	    "$(TOR_BASE_URL)/tor_$(TOR_VERSION)_x86_64.deb"
	@ar -x /tmp/kin-tor-update/x86_64.deb --output /tmp/kin-tor-update/x86_64/
	@mkdir -p /tmp/kin-tor-update/x86_64-extract
	@tar -xJf /tmp/kin-tor-update/x86_64/data.tar.xz \
	    -C /tmp/kin-tor-update/x86_64-extract \
	    --wildcards '*/bin/tor' 2>/dev/null; true
	@find /tmp/kin-tor-update/x86_64-extract -name 'tor' -type f \
	    -exec cp {} assets/bin/tor-linux-amd64 \;
	@chmod 0755 assets/bin/tor-linux-amd64
	@rm -rf /tmp/kin-tor-update
	@echo "Done. Tor binaries updated in assets/bin/"
	@ls -lh assets/bin/

## Install kin into PATH (Termux — run this inside Termux on the phone)
install: arm64
	cp kin-arm64 $$PREFIX/bin/kin
	@echo "Installed to $$PREFIX/bin/kin"

## Deploy kin-arm64 to Android phone via SSH (requires sshpass)
## Usage: make sync  or  make sync PHONE_IP=192.168.1.42
sync: arm64
	@echo "Syncing to $(PHONE_IP):$(PHONE_PORT)..."
	-sshpass -p '$(PHONE_PASS)' ssh -p $(PHONE_PORT) \
	    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	    user@$(PHONE_IP) "pkill -f kin"
	sshpass -p '$(PHONE_PASS)' scp -P $(PHONE_PORT) \
	    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	    kin-arm64 user@$(PHONE_IP):~/kin
	sshpass -p '$(PHONE_PASS)' ssh -p $(PHONE_PORT) \
	    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	    user@$(PHONE_IP) "cp ~/kin \$$PREFIX/bin/kin && chmod +x \$$PREFIX/bin/kin"
	@echo "Done. Run 'kin --web' on the phone."

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -f kin kin-arm64
