#!/bin/sh
set -e

# Kin uninstaller for Linux & Termux
# Sourced from: https://github.com/sm-o3/Kin

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "${RED}==============================================${NC}"
echo "${RED}          Kin Uninstaller for Linux & Termux   ${NC}"
echo "${RED}==============================================${NC}"

IS_TERMUX=false
if [ -n "$TERMUX_VERSION" ] || [ -d "/data/data/com.termux" ]; then
    IS_TERMUX=true
fi

# Remove binary
if [ "$IS_TERMUX" = true ]; then
    BINARY_PATH="$PREFIX/bin/kin"
else
    BINARY_PATH="$HOME/.local/bin/kin"
fi

if [ -f "$BINARY_PATH" ]; then
    echo "[*] Removing Kin binary from $BINARY_PATH..."
    rm "$BINARY_PATH"
else
    echo "[!] Kin binary not found in $BINARY_PATH."
fi

# Clean up data directory option
echo ""
echo -n "Do you want to delete all Kin data (database, keys, media in ~/.kin)? [y/N]: "
read -r CONFIRM
if [ "$CONFIRM" = "y" ] || [ "$CONFIRM" = "Y" ]; then
    echo "[*] Removing Kin data directory ~/.kin..."
    rm -rf "$HOME/.kin"
    echo "[+] Kin data deleted."
else
    echo "[i] Kin data directory kept at ~/.kin."
fi

echo ""
echo "${GREEN}==============================================${NC}"
echo "${GREEN}  🎉 Kin has been successfully uninstalled!${NC}"
echo "${GREEN}==============================================${NC}"
