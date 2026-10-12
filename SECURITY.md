# Security Policy

## Supported versions

The project is under active development. Security fixes are applied to the
latest release on the default branch.

| Version | Supported |
| --- | --- |
| latest | ✅ |
| older | ❌ |

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report vulnerabilities privately to security@wenyuan.dev. Include:

- a description of the issue and its impact,
- the affected route, table, or configuration, and
- a minimal reproduction, if you have one.

You will receive an acknowledgement, and we will keep you informed as we
investigate and prepare a fix. Please allow reasonable time for a fix to be
released before public disclosure.

## Scope

In scope:

- the HTTP API surface (`cmd/server`, `internal/httpx`, `internal/bankapi`,
  `internal/memoryops`),
- the PostgreSQL storage layer and migrations (`internal/storage`,
  `migrations`),
- configuration handling (`internal/config`).

Out of scope:

- the deployment platform or network in front of the service,
- third-party dependencies (report those upstream),
- findings that require a already-compromised host.