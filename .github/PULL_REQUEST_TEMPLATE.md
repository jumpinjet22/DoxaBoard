## What and why

<!-- What does this change, and why? Link the issue it closes. -->

## How I tested it

<!-- Commands you ran, hardware you tried it on. -->

## Checklist

- [ ] `make lint test` passes
- [ ] Commits are signed off (`git commit -s`, see CONTRIBUTING.md)
- [ ] Docs updated if behavior or setup changed

### For connector PRs

- [ ] `make connector-check NAME=<connector>` passes
- [ ] Fixtures are scrubbed: no real names, emails, phone numbers or keys
- [ ] No new third-party dependencies (or explained below)
- [ ] README lists config fields, setup steps and limitations
