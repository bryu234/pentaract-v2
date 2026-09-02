# Contributing

## Before opening a change

- Use an issue for bugs or feature proposals.
- Keep changes focused and preserve backward compatibility.
- Never include credentials, `.env`, master keys, backups or Telegram data.

## Validation

```bash
make test
make lint
docker compose config -q
```

Pull requests must explain the problem, the chosen solution and manual checks. Real Telegram tests must use a private test channel.
