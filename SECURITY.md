# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately via
[GitHub Security Advisories](https://github.com/truvity/cnpg/security/advisories/new).

Do NOT open a public issue for security vulnerabilities.

## Supported Versions

Only the latest release is supported with security updates.

## What is in scope

This repository publishes:

- The charts `cnpg-platform`, `cnpg-cluster` and `cnpg-database`, as published to `oci://ghcr.io/truvity/charts`.
- The example operator and barman-cloud plugin values under `examples/operator/`.
- The Go module (`github.com/truvity/cnpg/v2`) that tests and guards the charts.
- The documentation, where it tells an adopter to do something unsafe.

Reports that matter most:

- A chart default or a profile that weakens database TLS, client-certificate authentication, network policy or backup credentials.
- The admission guard against privileged database roles, or the `cnpg-database` render-time guard, accepting a role or grant it should refuse.
- A password, certificate key or backup credential reaching a rendered manifest, a log line or an error message.
- An example value that would be a vulnerability in a real cluster, since examples are what people copy.

A finding that depends on how a particular deployment uses this repository
belongs with that deployment's owner.
