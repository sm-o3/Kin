#!/bin/sh
set -e

# Kin single-command installer for Linux & Termux
# Sourced from: https://github.com/sm-o3/Kin

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "${BLUE}==============================================${NC}"
echo "${BLUE}          Kin Installer for Linux & Termux     ${NC}"
echo "${BLUE}==============================================${NC}"

# 1. Platform Detection
IS_TERMUX=false
if [ -n "$TERMUX_VERSION" ] || [ -d "/data/data/com.termux" ]; then
    IS_TERMUX=true
    echo "[*] Platform detected: Termux (Android)"
else
    echo "[*] Platform detected: Linux"
fi

# 2. Dependency Checks & Setup
echo "[*] Checking dependencies..."

check_and_install_debian() {
    if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
        echo "[*] Missing dependencies. Installing golang and git..."
        sudo apt-get update && sudo apt-get install -y golang git
    fi
}

check_and_install_fedora() {
    if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
        echo "[*] Missing dependencies. Installing golang and git..."
        sudo dnf install -y golang git
    fi
}

check_and_install_arch() {
    if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
        echo "[*] Missing dependencies. Installing go and git..."
        sudo pacman -Sy --noconfirm go git
    fi
}

if [ "$IS_TERMUX" = true ]; then
    # Termux package setup
    pkg update -y
    pkg install -y golang git
else
    # Linux package manager detection
    if command -v apt-get >/dev/null 2>&1; then
        check_and_install_debian
    elif command -v dnf >/dev/null 2>&1; then
        check_and_install_fedora
    elif command -v pacman >/dev/null 2>&1; then
        check_and_install_arch
    else
        # Fallback to manual check
        if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
            echo "${RED}[!] Error: Please install 'go' (golang) and 'git' manually for your Linux distribution before running this installer.${NC}"
            exit 1
        fi
    fi
fi

# Confirm tools are available
if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
    echo "${RED}[!] Error: Dependency installation failed. Please install 'go' and 'git' manually.${NC}"
    exit 1
fi

# 3. Clone and Build
TEMP_DIR=$(mktemp -d)
echo "[*] Cloning Kin repository to temporary directory..."
git clone https://github.com/sm-o3/Kin.git "$TEMP_DIR"

cd "$TEMP_DIR"

echo "[*] Compiling Kin from source..."
if [ "$IS_TERMUX" = true ]; then
    # Target ARM64 for Android Termux
    GOOS=android GOARCH=arm64 go build -ldflags "-checklinkname=0" -mod=vendor -o kin-binary .
else
    go build -mod=vendor -o kin-binary .
fi

# 4. Install Binary
INSTALL_DIR=""
if [ "$IS_TERMUX" = true ]; then
    INSTALL_DIR="$PREFIX/bin"
    cp kin-binary "$INSTALL_DIR/kin"
    chmod +x "$INSTALL_DIR/kin"
else
    # Install to ~/.local/bin for non-root
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
    cp kin-binary "$INSTALL_DIR/kin"
    chmod +x "$INSTALL_DIR/kin"
fi

# 5. Cleanup
cd "$HOME"
rm -rf "$TEMP_DIR"

echo ""
echo "${GREEN}==============================================${NC}"
echo "${GREEN}  🎉 Kin installed successfully!${NC}"
echo "${GREEN}==============================================${NC}"
echo "[+] Binary path: $INSTALL_DIR/kin"

if [ "$IS_TERMUX" = false ]; then
    # Check if ~/.local/bin is in PATH
    case ":$PATH:" in
        *:$INSTALL_DIR:*) ;;
        *)
            echo "${BLUE}[i] Note: Please add $INSTALL_DIR to your PATH to run 'kin' from anywhere:${NC}"
            echo "    export PATH=\"\$HOME/.local/bin:\$PATH\""
            echo "    (Add this line to your ~/.bashrc or ~/.zshrc)"
            ;;
    esac
fi

echo ""
echo "Run 'kin' to start the application and access the Web UI at http://127.0.0.1:8080"
echo ""
