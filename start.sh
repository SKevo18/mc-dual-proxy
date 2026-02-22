#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# Paper Server Start Script (for use with mc-dual-proxy)
# ============================================================
#
# This starts your Paper server configured to:
#   1. Accept PROXY protocol from mc-dual-proxy
#   2. Authenticate players via the multiauth server
#
# Prerequisites:
#   - paper-global.yml: proxies.proxy-protocol: true
#   - server.properties: enforce-secure-profile=false
#   - server.properties: server-port=25566
#   - mc-dual-proxy running on the same machine
#
# ============================================================

# --- Configuration (edit these) ---

# Paper jar filename
PAPER_JAR="paper.jar"

# Memory allocation
MIN_MEMORY="2G"
MAX_MEMORY="4G"

# mc-dual-proxy multiauth address
# Change this if your multiauth server is on another host or behind Caddy
MULTIAUTH_HOST="http://127.0.0.1:8652"

# Server port (must match mc-dual-proxy's -backend flag)
SERVER_PORT=25566

# --- JVM Flags ---

# Aikar's flags (widely recommended for Paper servers)
AIKAR_FLAGS=(
    -XX:+UseG1GC
    -XX:+ParallelRefProcEnabled
    -XX:MaxGCPauseMillis=200
    -XX:+UnlockExperimentalVMOptions
    -XX:+DisableExplicitGC
    -XX:+AlwaysPreTouch
    -XX:G1NewSizePercent=30
    -XX:G1MaxNewSizePercent=40
    -XX:G1HeapRegionSize=8M
    -XX:G1ReservePercent=20
    -XX:G1HeapWastePercent=5
    -XX:G1MixedGCCountTarget=4
    -XX:InitiatingHeapOccupancyPercent=15
    -XX:G1MixedGCLiveThresholdPercent=90
    -XX:G1RSetUpdatingPauseTimePercent=5
    -XX:SurvivorRatio=32
    -XX:+PerfDisableSharedMem
    -XX:MaxTenuringThreshold=1
    -Dusing.aikars.flags=https://mcflags.emc.gs
    -Daikars.new.flags=true
)

# Mojang API host overrides
#
# IMPORTANT: Paper requires ALL THREE of session.host, services.host, and
# profiles.host to be set, or it silently ignores them all with:
#   "Ignoring hosts properties. All need to be set: [...]"
#
# Only session.host points at mc-dual-proxy's multiauth server.
# The rest must be set to their standard Mojang URLs.
SESSION_FLAGS=(
    -Dminecraft.api.auth.host="https://authserver.mojang.com/"
    -Dminecraft.api.account.host="https://api.mojang.com/"
    -Dminecraft.api.services.host="https://api.minecraftservices.com/"
    -Dminecraft.api.profiles.host="https://api.mojang.com/"
    -Dminecraft.api.session.host="${MULTIAUTH_HOST}"
)

# --- Startup ---

echo "Starting Paper server on port ${SERVER_PORT}..."
echo "  Multiauth: ${MULTIAUTH_HOST}"
echo "  Memory:    ${MIN_MEMORY} - ${MAX_MEMORY}"
echo ""

exec java \
    -Xms"${MIN_MEMORY}" \
    -Xmx"${MAX_MEMORY}" \
    "${AIKAR_FLAGS[@]}" \
    "${SESSION_FLAGS[@]}" \
    -Dcom.mojang.eula.agree=true \
    -jar "${PAPER_JAR}" \
    --port "${SERVER_PORT}" \
    --nogui
