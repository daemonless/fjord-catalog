# Overrides

What a provider cannot read from an app's own metadata, one file per app:
`overrides/<source>/<app>.yaml`. Each file says where its facts come from:
`source` (a link) and `why` (the line they rest on). Unknown keys are an
error, so a typo cannot be ignored silently.

linuxserver (`overrides/linuxserver/`):

| key | what |
|---|---|
| `web_port` | the Open link's port, where readme-vars list none (plex, on host networking) |
| `login.password_var` | the app's own password setting; fjord makes it up at install |
| `login.user_var`, `login.user_default` | its user name setting, and the default offered |
| `login.env` | settings that switch the login on (`PASSWORD_ACCESS=true`) |
| `login.user`, `login.password` | the default login the app ships with: shown, with "change it" |
| `login.note` | a line for the Sign in card (audiosilo: the password is in the log) |
| `given_secrets` | secrets from an outside service, asked for, never made up (duckdns's `TOKEN`) |

Desktop apps (Selkies) get their login from a rule in the provider, not a
file each.

daemonless (`overrides/daemonless/`): `login` only, naming settings the
app's compose already has (`user_var`, `password_var`) or a fixed user
(`user`). The password setting becomes a required secret fjord makes up.
