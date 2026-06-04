// Package assets embeds prebuilt Tor binaries for all supported platforms.
// Binaries are sourced from the Termux official package repository
// (packages.termux.dev) — built by the NDK for Android, and work inside
// Termux on any Android device without any package manager installation.
//
// Embedded binaries:
//   bin/tor-android-arm64 — Termux aarch64 (Xiaomi 14, Pixel, etc.)
//   bin/tor-linux-amd64   — Termux x86_64 / Linux desktop (dev/testing)
package assets

import _ "embed"

// TorArm64 is the tor binary for linux/arm64 (Android / Termux aarch64).
//
//go:embed bin/tor-android-arm64
var TorArm64 []byte

// TorAmd64 is the tor binary for linux/amd64 (desktop dev / Termux x86_64).
//
//go:embed bin/tor-linux-amd64
var TorAmd64 []byte
