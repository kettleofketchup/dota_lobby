### Upgrading

Add the new timeout to `config.yaml` before upgrading:

```yaml
lobby:
  create_timeout: 30
```

Then restart the service.

A second, separate upgrade note about bot credentials.

### Added

- **Lobby timeouts.** `POST /lobby/create` gives up after
  `lobby.create_timeout` seconds instead of waiting forever.
- `GET /bots` reports each bot's last error.

### Changed

- From a-b.md, which sorts first: bots are listed by username.
- From a.md, sorted after a-b.md in code-point order: lobby names are trimmed.
