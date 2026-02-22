# mc-dual-proxy

A lightweight Go application that lets your Minecraft server accept players from
**both** direct connections (your domain) and the Minehut proxy simultaneously.

## The Problem

When you connect an external server to Minehut, you must configure your backend
for Minehut's PROXY protocol and MITM session server. This means:

1. **PROXY protocol mismatch** — Minehut sends HAProxy PROXY protocol headers;
   direct players don't. Your backend can't have it both enabled and disabled.
2. **Session server mismatch** — Minehut uses its own session server
   (`api.minehut.com/mitm/proxy/...`) for `hasJoined` auth. Direct players
   authenticate against Mojang's. Your backend can only point at one URL.

## The Solution

This app runs two services in one binary:

### 1. TCP Proxy (port 25565)

Normalizes PROXY protocol so your backend always sees a consistent format:

- **Minehut connection** (has PROXY protocol header): passes it through to the backend
- **Direct connection** (no PROXY protocol): generates a v2 header from the real
  TCP peer address and prepends it

Your backend keeps `proxy-protocol: true` and works for both sources.

### 2. Multiauth HTTP Server (port 8652)

Multiplexes `hasJoined` session server requests:

1. Receives a `hasJoined` request from your backend
2. Fans it out to both Mojang and Minehut **concurrently**
3. Returns whichever responds with HTTP 200

This works because the `serverId` hash is cryptographically unique per connection
— only the correct session server will ever return 200. There's no collision risk.

## Architecture

```plaintext
                                         ┌──────────────────┐
Direct players ──→ myserver.xyz ────────→│                  │     ┌───────────────┐
                                         │  mc-dual-proxy   │────→│ Backend       │
Minehut players ──→ minehut.gg ──→ MH ──→│  TCP :25565      │     │ Velocity/Paper│
                                         └──────────────────┘     │ :25566        │
                                                                  │ proxy-proto:✓ │
                                         ┌──────────────────┐     │               │
Backend hasJoined calls ────────────────→│  mc-dual-proxy   │     └───────┬───────┘
                                         │  HTTP :8652      │             │
                                         └────────┬─────────┘             │
                                                   │                      │
                                    ┌──────────────┴──────────────┐       │
                                    ▼                             ▼       │
                              Mojang Session              Minehut MITM    │
                              Server                      Session Server  │
                              (direct players)            (MH players)    │
```

## Quick Start

### Build

```bash
go build -o mc-dual-proxy .
```

### Run

```bash
./mc-dual-proxy \
  -listen    0.0.0.0:25565 \
  -backend   127.0.0.1:25566 \
  -auth-listen 127.0.0.1:8652 \
  -session-servers "https://sessionserver.mojang.com,https://api.minehut.com/mitm/proxy"
```

### Docker

```bash
docker build -t mc-dual-proxy .
docker run -d --name mc-dual-proxy \
  --network host \
  mc-dual-proxy \
  -listen 0.0.0.0:25565 \
  -backend 127.0.0.1:25566
```

## Backend Configuration

### Velocity

In `velocity.toml`:

```toml
[advanced]
haproxy-protocol = true
```

JVM flags:

```bash
java -Dmojang.sessionserver=http://127.0.0.1:8652/session/minecraft/hasJoined \
     -jar velocity.jar
```

### Standalone Paper (no proxy)

In `config/paper-global.yml`:

```yaml
proxies:
  proxy-protocol: true
```

In `server.properties`:

```properties
enforce-secure-profile=false
server-port=25566
```

JVM flags:

```bash
java \
  -Dminecraft.api.auth.host=https://authserver.mojang.com/ \
  -Dminecraft.api.account.host=https://api.mojang.com/ \
  -Dminecraft.api.services.host=https://api.minecraftservices.com/ \
  -Dminecraft.api.profiles.host=https://api.mojang.com/ \
  -Dminecraft.api.session.host=http://127.0.0.1:8652 \
  -jar paper.jar --nogui
```

> **⚠️ Important:** Paper requires `session.host`, `services.host`, AND
> `profiles.host` to all be set, or it silently ignores them all. You will see
> this in the server log if any are missing:
>
> ```plain
> Ignoring hosts properties. All need to be set: [minecraft.api.services.host, minecraft.api.session.host, minecraft.api.profiles.host]
> ```
>
> Only `session.host` points at mc-dual-proxy — the rest must be set to their
> standard Mojang URLs.

## Minehut Panel Configuration

1. Set your external server IP to your **public IP** (where mc-dual-proxy listens)
2. Set the port to `25565` (or whatever you configured with `-listen`)
3. Set proxy type to "Other" for standalone Paper (or "Velocity" if using Velocity)
4. Set DNS record type to "Port"
5. Leave TCP Shield as "Not Configured"

## Firewall Notes

If you're running on a host with both a cloud firewall and an OS-level firewall
(e.g., Hetzner), make sure **both** allow TCP on port 25565:

```bash
# For UFW
sudo ufw allow 25565/tcp

# For raw iptables
sudo iptables -I INPUT -p tcp --dport 25565 -j ACCEPT
```

You do **not** need to expose port 25566 (backend) or 8652 (multiauth) — those
only need to be reachable from localhost.

## Exposing Multiauth via Caddy (Optional)

If your backend runs on the same machine, `127.0.0.1:8652` works directly. If
the multiauth server needs to be reachable externally (e.g., backend on a
different machine), add to your Caddyfile:

```caddyfile
auth.yourdomain.com {
    reverse_proxy 127.0.0.1:8652
}
```

Then configure your backend with:

```bash
-Dminecraft.api.session.host=https://auth.yourdomain.com
```

(keeping the other `-D` flags pointed at Mojang as shown above)

## Adding More Session Servers

You can add additional session servers (e.g., Minekube Connect) via the
`-session-servers` flag:

```bash
-session-servers "https://sessionserver.mojang.com,https://api.minehut.com/mitm/proxy,https://connect.minekube.com/auth"
```

All endpoints are queried concurrently; the first 200 wins.

## Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `-listen` | `0.0.0.0:25565` | TCP proxy listen address |
| `-backend` | `127.0.0.1:25566` | Backend (Velocity/Paper) address |
| `-auth-listen` | `127.0.0.1:8652` | Multiauth HTTP listen address |
| `-session-servers` | `https://sessionserver.mojang.com,https://api.minehut.com/mitm/proxy` | Comma-separated session server base URLs |

## How It Works (Technical Details)

### PROXY Protocol Detection

On each incoming TCP connection, the proxy peeks at the first bytes:

- **PROXY protocol v1**: starts with ASCII `PROXY` (6 bytes), terminated by `\r\n`
- **PROXY protocol v2**: starts with the 12-byte binary signature
  `\x0D\x0A\x0D\x0A\x00\x0D\x0A\x51\x55\x49\x54\x0A`
- **Neither**: raw Minecraft handshake

If a header is detected, it's consumed and forwarded verbatim to the backend.
If not, a v2 header is generated from the TCP socket addresses and prepended.

### Multiauth Session Server

The Minecraft login flow:

1. Client sends Login Start
2. Server sends Encryption Request (with server's public key + verify token)
3. Client generates shared secret, sends Encryption Response
4. Server computes `serverId` hash = SHA1(serverID + sharedSecret + publicKey)
5. Server calls `hasJoined?username=X&serverId=HASH` on the session server
6. Session server returns the player profile if valid

When Minehut proxies a player, it MITM's the encryption exchange. The hash your
backend computes will only be valid against Minehut's session server. A direct
player's hash will only be valid against Mojang's. By querying both concurrently,
exactly one will return 200.

## License

MIT
