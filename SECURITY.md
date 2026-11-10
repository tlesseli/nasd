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
design, holding the Backup button resets the admin password).

## Security design, in short

- **LAN only.** The NAS is not meant to be reachable from the internet; don't
  forward its ports.
- **Web interface**: HTTPS (self-signed certificate per installation),
  passwords hashed with PBKDF2, sessions expire after 30 minutes idle,
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
