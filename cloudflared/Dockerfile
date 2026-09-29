# The Cloudflare Tunnel: the app's only way in. A separate Railway service that
# connects out to Cloudflare and forwards alphabeter.org to the app over
# Railway's private network. Where it forwards to is set on the tunnel in
# Cloudflare's dashboard; the service only needs TUNNEL_TOKEN.
FROM cloudflare/cloudflared:2026.9.3

# The image's entrypoint is "cloudflared --no-autoupdate".
CMD ["tunnel", "run"]
