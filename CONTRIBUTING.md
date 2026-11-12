# Contributing to DoxaCast

Thanks for helping! A few things keep this project healthy.

## Ground rules

- **Open an issue first** for anything bigger than a small fix, so we agree on
  the approach before you spend a weekend on it.
- **Keep PRs focused.** One change per PR is much easier to review.
- **Be kind.** Most people here are church volunteers doing this in their spare time.

## Development

```sh
make lint test      # gofmt, vet, tests with the race detector
make build          # arm64, armv7 and amd64 binaries in ./dist
make run-server     # local server with throwaway tokens
golangci-lint run   # the same linters CI runs (config in .golangci.yml)
```

CI also runs CodeQL, govulncheck and dependency review (no vulnerable or
copyleft dependencies). Labels are applied automatically.

Go 1.26.11+ (the `go` command fetches it automatically).

## Connectors

See [docs/connectors.md](docs/connectors.md). In short:

- Most CMSes need **no code**: try the RSS or JSON feed connectors first.
- **Sidecar** connectors (any language) live in your own repo; get listed with a "List my connector" issue.
- **In-tree** Go connectors start with a "Connector proposal" issue, then
  `go run ./cmd/doxacast-connector new <name>`, then `make connector-check NAME=<name>`.

## Sign your commits (DCO)

We use the [Developer Certificate of Origin](https://developercertificate.org/)
instead of a CLA. Add a sign-off to every commit:

```sh
git commit -s -m "Fix the thing"
```

That adds `Signed-off-by: Your Name <you@example.com>`, certifying you wrote the
change (or have the right to submit it) under the project's Apache-2.0 license.

## Security

Never report vulnerabilities in public issues. See [SECURITY.md](SECURITY.md).

## Data

Never commit real member data: no names, emails, phone numbers, photos of
people, or API keys, including in test fixtures and screenshots.
