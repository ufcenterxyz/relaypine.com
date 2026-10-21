# Security policy

## Reporting a vulnerability

Use [GitHub's private vulnerability reporting](https://github.com/ufcenterxyz/relaypine.com/security/advisories/new) for authentication bypasses, credential exposure, cross-tenant access, SSRF, billing integrity issues, or other security vulnerabilities. Please do not publish an exploit or sensitive details in a public issue before maintainers have investigated it.

Include the affected commit or release, a minimal reproduction using synthetic data, the impact, and any suggested fix. Do not send live API keys, Mailgun credentials, session cookies, verification codes, customer data, or database backups. Revoke any credential you believe has already been exposed.

## Supported versions

Security fixes target the latest release and `main`. Earlier preview releases do not receive separate backports. Keep self-hosted deployments current and review configuration changes before upgrading.

## Deployment boundaries

Each installation configures its own `SUPERADMIN_EMAIL` and verifies mailbox ownership before issuing a session. Other addresses join the waitlist. Forking the source does not grant access to the hosted service.

Environment files and deployment secrets are not part of the repository. Preserve and protect `MASTER_KEY` alongside database backups. The application rejects private upstream destinations in production and does not silently bypass configured proxies. See [operations](docs/operations.md) for reverse proxy, email, and backup configuration.
