# Security Policy

DoxaCast runs on church networks and holds API keys for systems that store
people's data, so we take security reports seriously.

## Reporting a vulnerability

**Please don't open a public issue.** Report it privately through
GitHub: **Security → Report a vulnerability** on this repository.

Include what you found, how to reproduce it, and what an attacker could do
with it. You'll get an acknowledgement within a week. Once a fix is released,
we'll credit you in the advisory unless you'd rather we didn't.

## Supported versions

DoxaCast is pre-1.0. Only the latest release and `main` get security fixes.

## Scope

In scope: the server, the agent, `core`, in-tree connectors, the deploy
files and our CI workflows.

Out of scope: community sidecar connectors (report those to their
maintainers), and third-party software we deploy (mediamtx, rclone, mpv,
ffmpeg). Report those upstream, but tell us too if DoxaCast's configuration
makes it worse.
