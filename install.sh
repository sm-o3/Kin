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

# 1. Platform & Arch Detection
IS_TERMUX=false
if [ -n "$TERMUX_VERSION" ] || [ -d "/data/data/com.termux" ]; then
    IS_TERMUX=true
    echo "[*] Platform detected: Termux (Android)"
else
    echo "[*] Platform detected: Linux"
fi

OS_TYPE="linux"
if [ "$IS_TERMUX" = true ]; then
    OS_TYPE="android"
fi

ARCH_TYPE=$(uname -m)
case "$ARCH_TYPE" in
    x86_64|amd64)
        ARCH_TYPE="amd64"
        ;;
    aarch64|arm64)
        ARCH_TYPE="arm64"
        ;;
    *)
        ARCH_TYPE="unknown"
        ;;
esac

TAG="v1.0.0"
ASSET_NAME=""
if [ "$OS_TYPE" = "android" ] && [ "$ARCH_TYPE" = "arm64" ]; then
    ASSET_NAME="kin-android-arm64.tar.gz"
elif [ "$OS_TYPE" = "linux" ] && [ "$ARCH_TYPE" = "amd64" ]; then
    ASSET_NAME="kin-linux-amd64.tar.gz"
elif [ "$OS_TYPE" = "linux" ] && [ "$ARCH_TYPE" = "arm64" ]; then
    ASSET_NAME="kin-linux-arm64.tar.gz"
fi

# Helper to show installation success
show_success() {
    INSTALL_DIR="$1"
    echo ""
    echo "${GREEN}==============================================${NC}"
    echo "${GREEN}  🎉 Kin installed successfully!${NC}"
    echo "${GREEN}==============================================${NC}"
    echo "[+] Binary path: $INSTALL_DIR/kin"

    if [ "$IS_TERMUX" = false ]; then
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
}

# 2. Attempt Precompiled Binary Download
DOWNLOAD_SUCCESS=false
if [ -n "$ASSET_NAME" ]; then
    echo "[*] Attempting to download pre-compiled binary: $ASSET_NAME..."
    URL="https://github.com/sm-o3/Kin/releases/download/$TAG/$ASSET_NAME"
    TEMP_DIR=$(mktemp -d)
    
    if command -v curl >/dev/null 2>&1; then
        if curl -fsSL -o "$TEMP_DIR/$ASSET_NAME" "$URL"; then
            DOWNLOAD_SUCCESS=true
        fi
    elif command -v wget >/dev/null 2>&1; then
        if wget -q -O "$TEMP_DIR/$ASSET_NAME" "$URL"; then
            DOWNLOAD_SUCCESS=true
        fi
    fi
    
    if [ "$DOWNLOAD_SUCCESS" = true ]; then
        echo "[+] Download successful! Extracting binary..."
        tar -xzf "$TEMP_DIR/$ASSET_NAME" -C "$TEMP_DIR"
        
        BINARY_FILE=""
        if [ "$ASSET_NAME" = "kin-android-arm64.tar.gz" ]; then
            BINARY_FILE="kin-android-arm64"
        elif [ "$ASSET_NAME" = "kin-linux-amd64.tar.gz" ]; then
            BINARY_FILE="kin-linux-amd64"
        elif [ "$ASSET_NAME" = "kin-linux-arm64.tar.gz" ]; then
            BINARY_FILE="kin-linux-arm64"
        fi
        
        INSTALL_DIR=""
        if [ "$IS_TERMUX" = true ]; then
            INSTALL_DIR="$PREFIX/bin"
            cp "$TEMP_DIR/$BINARY_FILE" "$INSTALL_DIR/kin"
            chmod +x "$INSTALL_DIR/kin"
        else
            INSTALL_DIR="$HOME/.local/bin"
            mkdir -p "$INSTALL_DIR"
            cp "$TEMP_DIR/$BINARY_FILE" "$INSTALL_DIR/kin"
            chmod +x "$INSTALL_DIR/kin"
        fi
        
        rm -rf "$TEMP_DIR"
        show_success "$INSTALL_DIR"
        exit 0
    else
        echo "[!] Pre-compiled binary download not available or failed. Falling back to build from source."
        rm -rf "$TEMP_DIR"
    fi
fi

# 3. Source Build Fallback (Requires Go and Git)
echo "[*] Checking source build dependencies..."

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
    pkg update -y
    pkg install -y golang git
else
    if command -v apt-get >/dev/null 2>&1; then
        check_and_install_debian
    elif command -v dnf >/dev/null 2>&1; then
        check_and_install_fedora
    elif command -v pacman >/dev/null 2>&1; then
        check_and_install_arch
    else
        if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
            echo "${RED}[!] Error: Please install 'go' (golang) and 'git' manually for your Linux distribution before running this installer.${NC}"
            exit 1
        fi
    fi
fi

if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
    echo "${RED}[!] Error: Dependency installation failed. Please install 'go' and 'git' manually.${NC}"
    exit 1
fi

TEMP_DIR=$(mktemp -d)
echo "[*] Cloning Kin repository to temporary directory..."
git clone https://github.com/sm-o3/Kin.git "$TEMP_DIR"

cd "$TEMP_DIR"

echo "[*] Compiling Kin from source..."
if [ "$IS_TERMUX" = true ]; then
    GOOS=android GOARCH=arm64 go build -ldflags "-checklinkname=0" -mod=vendor -o kin-binary .
else
    go build -mod=vendor -o kin-binary .
fi

INSTALL_DIR=""
if [ "$IS_TERMUX" = true ]; then
    INSTALL_DIR="$PREFIX/bin"
    cp kin-binary "$INSTALL_DIR/kin"
    chmod +x "$INSTALL_DIR/kin"
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
    cp kin-binary "$INSTALL_DIR/kin"
    chmod +x "$INSTALL_DIR/kin"
fi

cd "$HOME"
rm -rf "$TEMP_DIR"

show_success "$INSTALL_DIR"
