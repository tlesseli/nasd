# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Write to
**tanguy@ulixes-consulting.be** with "nasd security" in the subject, and
include:

- the nasd version (System Settings → About, e.g. `1.7.2+05dbb8c`),
- what an attacker can do, and from where (same network, signed-in admin…),
- the steps to reproduce it, or a proof of concept.

What happens next:

| | |
|---|---|
| Acknowledgement | within 5 working days |
| First assessment (is it real, how serious) | within 14 days |
| Fix | as soon as possible; serious issues get a release of their own |
| Disclosure | together, once a fix is released (credit given if you wish) |

nasd is maintained by one person: if a deadline slips, you'll be told why.

## Supported versions

Only the **latest release** receives security fixes. Updating is the fix.

## Scope

In scope: nasd itself (the web interface and its API, the system image built
by `image/build.sh --dist`, the scripts in this repository).

Out of scope: vulnerabilities in Debian packages (report them to Debian:
https://www.debian.org/security/; nasd picks up their fixes through the
system's updates), and attacks that need physical access to the NAS (by
design, holding the recessed Reset button at the back resets the admin
password).

## Security design, in short

- **LAN only.** The NAS is not meant to be reachable from the internet; don't
  forward its ports.
- **Firewall** (System → Firewall, off until switched on): only private
  networks, or only the NAS's own network, plus listed addresses, may connect;
  everything else is dropped and logged. A change that would block the
  computer making it is refused, a change must be confirmed within 2 minutes
  or it is undone, and the Reset button at the back switches it off.
- **HTTPS**: each installation makes its own certificate authority, limited by
  name constraints to the NAS's own names and private addresses (it can't vouch
  for any other site, even if its key leaked). Installed once on a computer
  (System → HTTPS certificate), it removes the browser warning; its
  fingerprint can be checked on the front display. The server certificate
  renews itself.
- **Web interface**: passwords hashed with PBKDF2-SHA256 (600,000 rounds;
  older hashes are upgraded at the next sign-in), sessions expire after 30 minutes idle,
  5 failed sign-ins lock an address out for 10 minutes, requests are protected
  against cross-site forgery. Deleting shares or folders, clearing logs and
  changing SSH keys ask for the admin password again.
- **First start**: creating the admin account needs a setup code shown on the
  front display (and in the system log).
- **SSH**: keys only, never passwords; NAS users (file sharing accounts) can't
  log in. Distributed images contain no key and no password; every
  installation generates its own SSH host keys.
- **Secrets** (backup credentials, cloud archive keys) are never sent back to
  the browser and are passed to tools through standard input or the
  environment, never on command lines.
- **Settings backups** are encrypted (AES-256-GCM, key derived from a
  passphrase with PBKDF2-SHA256, 600,000 rounds) and kept in a folder only
  root can read.
- **Name lookup**: the NAS resolves names with DNS only (LLMNR is off), so a
  device on the network can't answer in the DNS's place.
- **Releases** are checked with govulncheck (known vulnerabilities in the Go
  code and the Go version it is built with) and for secrets in the image
  before they are published.
