# Security

`codex-tg` is local-first.

## Defaults

- Codex App Server runs locally over stdio.
- Telegram access is restricted to exactly one allowed user in one configured
  private AFC forum group.
- SQLite state stays on the operator machine.
- `ctr-go init` stores local configuration in `~/.codex-tg/config.env` by default.
- `ctr-go service install` creates a user LaunchAgent whose environment contains
  only `CTR_GO_CONFIG`.
- If proxy variables are needed for network access, they are stored in the
  private `config.env` and applied by the process after startup; proxy URLs may
  contain credentials, so treat the config file as secret material.
- The macOS tray app controls service lifecycle and opens local files, but it
  does not read or display Telegram tokens.
- The optional Yandex Messenger adapter authorizes by an explicit sender-login
  allowlist plus a robot mention. It intentionally does not trust or filter on
  a source chat id.

## Never Commit

- `.env`
- Bot tokens
- Yandex Messenger OAuthTeam tokens
- `config.env`
- Telegram user sessions
- Chat ids from private deployments
- SQLite databases
- Logs
- Private screenshots

## Network Boundary

Do not expose Codex App Server on a public interface. App Server stays local or
private to the operator machine.

The experimental control-plane HTTP adapter is disabled by default and can only
bind loopback TCP addresses through `CTR_GO_CONTROL_API_LISTEN`. Public network
listeners, cloud brokers, and unauthenticated non-local control surfaces require
a separate ADR.

Telegram, tray, voice, and future HTTP/mobile surfaces are adapters. They must
not bypass Codex approvals, sandboxing, allowlists, or App Server lifecycle
guards.

Voice wake-word adapters must treat transcription as untrusted input. A spoken
request can route to Codex, but it must not auto-approve sensitive file,
command, permission, or MCP requests.

Secrets stay in the local config file today. A future Keychain migration is
allowed, but runtime docs and logs must continue to avoid printing secrets in
full.

Yandex Messenger text is untrusted launch input. It follows the configured
manual or automatic launch policy, but it cannot approve later Codex permission
prompts and never bypasses sender authorization.
