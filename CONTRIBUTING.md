# Contributing

Issues and pull requests are welcome. For a larger change, open an issue first so
we can agree on the approach.

## Develop

```bash
make dev-gateway   # the gateway on :4777
make dev-ui        # Vite on :5177, /api forwarded to the gateway
make test          # go vet + go test, and the frontend typecheck
```

The gateway is Go with the standard library only; please keep it that way. The
[Layout](README.md#layout) section of the README maps the packages.

## Pull requests

- Keep each PR to one change, with tests for gateway behaviour.
- Run `make test` before pushing.
- Screenshots in `frontend/docs/screenshots/` use synthetic demo data only; never
  commit real prompts, keys or request logs.

By contributing you agree that your work is licensed under the
[Apache License 2.0](LICENSE).
