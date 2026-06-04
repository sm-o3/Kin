#!/usr/bin/env bash

# Exit immediately if a command exits with a non-zero status
set -e

# Receiver device configuration
REC_IP="192.168.31.104"
REC_PW="${REC_PW:-your_receiver_password}"

# Sender device configuration
SND_IP="192.168.31.109"
SND_PW="${SND_PW:-your_sender_password}"

echo "============================================="
echo "  Kin E2E Connection Automation Test"
echo "============================================="
echo "Receiver Device: $REC_IP"
echo "Sender Device:   $SND_IP"
echo "---------------------------------------------"

# Ensure any leftover processes are cleaned up
echo "[*] Cleaning up existing processes on devices..."
sshpass -p "$REC_PW" ssh -p 8022 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null u0_a850@$REC_IP "killall -9 kin tor tor-android-arm64 tor-linux-arm64 || true" 2>/dev/null
sshpass -p "$SND_PW" ssh -p 8022 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null u0_a850@$SND_IP "killall -9 kin tor tor-android-arm64 tor-linux-arm64 || true" 2>/dev/null

# Clean up local logs
REC_LOG="/tmp/kin_rec_test.log"
rm -f "$REC_LOG"

echo "[*] Launching Receiver on $REC_IP in automated test mode..."
sshpass -p "$REC_PW" ssh -p 8022 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null u0_a850@$REC_IP "~/kin -test-receiver" > "$REC_LOG" 2>&1 &
REC_SSH_PID=$!

echo "[*] Waiting for Receiver to bootstrap Tor and generate onion address..."
ONION=""
LIMIT=120
COUNT=0
while [ $COUNT -lt $LIMIT ]; do
    if grep -q "TEST_ONION:" "$REC_LOG" 2>/dev/null; then
        ONION=$(grep "TEST_ONION:" "$REC_LOG" | awk '{print $2}' | tr -d '\r\n')
        break
    fi
    sleep 2
    COUNT=$((COUNT + 2))
    echo -n "."
done
echo ""

if [ -z "$ONION" ]; then
    echo "[!] Error: Receiver failed to bootstrap Tor or output onion address within 120s."
    echo "--- Receiver Log Output ---"
    cat "$REC_LOG" || true
    kill $REC_SSH_PID 2>/dev/null || true
    exit 1
fi

echo "[+] Receiver Tor bootstrapped! Onion address: $ONION"
echo "[*] Starting Sender on $SND_IP to connect and send test message..."

SND_LOG="/tmp/kin_snd_test.log"
set +e # Allow sender to fail so we can print its log on error
sshpass -p "$SND_PW" ssh -p 8022 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null u0_a850@$SND_IP "~/kin -test-sender $ONION" > "$SND_LOG" 2>&1
SND_STATUS=$?
set -e

if [ $SND_STATUS -ne 0 ]; then
    echo "[!] Error: Sender failed with exit code $SND_STATUS."
    echo "--- Sender Log Output ---"
    cat "$SND_LOG" || true
    echo "--- Receiver Log Output ---"
    cat "$REC_LOG" || true
    kill $REC_SSH_PID 2>/dev/null || true
    exit 1
fi

echo "[+] Sender finished successfully."
echo "[*] Waiting for Receiver to capture the message..."

# Wait for receiver process to complete (it should exit 0 on TEST_SUCCESS)
set +e
wait $REC_SSH_PID 2>/dev/null
REC_STATUS=$?
set -e

echo "--- Sender Log Output ---"
cat "$SND_LOG" || true

echo "--- Receiver Log Output ---"
cat "$REC_LOG" || true

if grep -q "TEST_SUCCESS" "$REC_LOG" 2>/dev/null; then
    echo ""
    echo "============================================="
    echo "  🎉 TEST SUCCESS: ICE Connection Established!"
    echo "============================================="
    exit 0
else
    echo ""
    echo "============================================="
    echo "  ❌ TEST FAILED: ICE Connection Failed!"
    echo "============================================="
    exit 1
fi
