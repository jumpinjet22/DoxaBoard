# Security Policy

DoxaBoard runs on church networks, puts content on screens people trust, and
holds API keys for systems that store people's data. We take security reports
seriously and appreciate responsible disclosure.

## Reporting a vulnerability

**Please don't open a public issue, discussion or pull request.**

Report privately through GitHub's private vulnerability reporting:

**https://github.com/jumpinjet22/DoxaBoard/security/advisories/new**

(or **Security → Report a vulnerability** on the repository page). Only the
maintainers can see your report.

Please include:

- what you found and where (file, endpoint, config)
- steps to reproduce, or a proof of concept
- the impact: what an attacker could do with it
- the version or commit you tested
- whether you'd like to be credited, and how

## What happens next

| Step | Target |
|---|---|
| We acknowledge your report | within **7 days** |
| We confirm (or not) and assess severity | within **14 days** |
| We release a fix | within **90 days**, sooner for critical issues |
| We publish a GitHub Security Advisory | when the fix is released |

We'll keep you updated along the way and coordinate the disclosure date with
you. If we can't fix something within 90 days, we'll explain why and agree on
a new date. Please don't disclose publicly before the advisory is published,
or 90 days after your report, whichever comes first.

We credit reporters in the advisory unless you'd rather we didn't. DoxaBoard is
a volunteer project and doesn't run a paid bug bounty.

## Supported versions

DoxaBoard is pre-1.0. Only the latest release and `main` get security fixes.

## Scope

**In scope:** the server and admin console, the screen agent, `core`, the slide
feed parser, in-tree connectors, the deploy files (Docker Compose, relay
config, install script) and our CI workflows.

**Out of scope:**

- community sidecar connectors: report those to their maintainers
- third-party software we deploy (mediamtx, rclone, mpv, ffmpeg): report
  those upstream, but tell us too if DoxaBoard's configuration makes it worse
- attacks that need an already-compromised admin token, server or screen
- denial of service by flooding the network

## Known limitations

These are documented, not vulnerabilities (but ideas to fix them are welcome):

- Anyone on the local network can publish to the relay while OBS is offline.
  Publish credentials are on the roadmap.
- The server speaks plain HTTP; put it behind a TLS reverse proxy if it's
  reachable beyond a trusted network.
