# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| Latest default branch | Yes |

We recommend running the latest version of JustRAG.

## Reporting A Vulnerability

Do not open a public GitHub issue for security vulnerabilities.

Report vulnerabilities by email to `stekarcher@gmail.com` and include:

- a description of the issue
- reproduction steps
- impact
- optional mitigation ideas

Target response times:

- acknowledgement within 48 hours
- critical issue mitigation or fix plan within 7 days when practical

## Current Security-Relevant Deployment Notes

For the current Go-first deployment:

- `JWT_SECRET` must be set to a strong random value
- `REDIS_PASSWORD` is required in production
- `ADMIN_PASSWORD` must be changed before first deployment
- database, Redis, and MinIO example credentials should never be used unchanged in production
- `ALLOWED_ORIGINS` should be set explicitly
- `FETCHER_ALLOW_NO_SANDBOX=true` is only appropriate inside the container image where Chromium runs as root

See [DEPLOYMENT.md](DEPLOYMENT.md) for the current deployment shape and env variables.

## In Scope

- authentication and authorization bypasses
- injection vulnerabilities including SQLi, XSS, and SSRF
- sensitive data exposure
- privilege escalation
- vulnerabilities in the default Go deployment, worker processing, or container configuration

## Out Of Scope

- social engineering
- physical-access attacks
- denial-of-service reports without a concrete product bug
- vulnerabilities that only exist in an already-compromised host environment
- third-party dependency disclosures without a corresponding issue in JustRAG’s integration or update posture
