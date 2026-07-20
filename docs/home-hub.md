# Running the hub at home (no port forwarding)

The hub is a single binary with an embedded database — a Raspberry Pi, an old
laptop, or the Mac you already own is plenty. The only question is how your
phone or laptop reaches it when you're away from home. Agents always dial
**out** to the hub, so they work from anywhere with no configuration; this
page is about reaching the hub's web UI remotely.

Pick one:

## Option A — Cloudflare Tunnel (free, public URL, no exposed ports)

`cloudflared` makes an outbound connection from your home machine to
Cloudflare; your hub becomes reachable at a hostname you own without opening
any port on your router.

1. [Add your domain to Cloudflare](https://developers.cloudflare.com/fundamentals/setup/) (free plan is fine).
2. Install and authenticate cloudflared:

   ```sh
   brew install cloudflared        # macOS
   cloudflared tunnel login
   ```

3. Create the tunnel and route it at your hub:

   ```sh
   cloudflared tunnel create breakerbox
   cloudflared tunnel route dns breakerbox hub.yourdomain.com
   cloudflared tunnel run --url http://localhost:8090 breakerbox
   ```

4. Use `https://hub.yourdomain.com` as the hub URL everywhere (web login,
   agent enrollment, mobile).

Note: WebSockets (which agents use) pass through Cloudflare Tunnel without
extra configuration.

## Option B — Tailscale (private, zero public exposure)

If you'd rather nothing be public at all, put the hub and your devices on a
[Tailscale](https://tailscale.com) tailnet:

1. Install Tailscale on the hub machine and on each device you'll browse
   from (including your phone).
2. Use the hub machine's tailnet name — e.g. `http://mymac.tailnet-name.ts.net:8090` —
   as the hub URL.
3. Agents on machines outside the tailnet can still reach the hub if you use
   [Tailscale Funnel](https://tailscale.com/kb/1223/funnel) for the single
   `/api/bb/agent/ws` endpoint, or simply join those machines to the tailnet
   too.

## Option C — hub on a cheap VPS (simplest remote access)

Skip tunneling entirely: run the hub on a $3–5/mo VPS with the one-line
installer, and let every machine (home Mac included) run only the agent.

```sh
curl -fsSL https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-hub.sh | sh
```

Put it behind any reverse proxy (Caddy shown) for TLS:

```
hub.example.com {
    reverse_proxy localhost:8090
}
```

Agents at home connect out to it; nothing at home is exposed.
