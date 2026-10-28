# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
A release takes its notes from its section here (`task release` refuses a
version without one); see [docs/development.md](docs/development.md#releasing).

## [Unreleased]

## [0.1.0] - 2026-10-28

### Added

- Licensed under the Apache License 2.0 (`LICENSE`, `NOTICE`).
- A library of test fixtures in datasets (global, workspace and quarantine),
  with accounts, scoped API keys, dataset visibility rules and an audit log.
- Synthetic data: a registry of fake people and companies, and generated
  emails, documents and records (built-in and custom TOML schemas) about
  them, reproducible from a seed.
- Ingest of real emails and documents (text, Markdown, CSV, TSV, JSON,
  HTML, Twig and Jinja templates, DOCX, RTF) through Presidio into a
  quarantine dataset, where every fixture is reviewed before use.
- The anonymise tool: files in, anonymised files out in the same format,
  storing nothing, for any key over REST or the CLI, or locally with
  `--local`; and a skill (`skills/exemplar-anonymise`) that lets agents use
  it without reading the originals.
- A REST API, a read-only MCP server and a CLI over the same services.
- Export and import of datasets between instances, host backups and
  restores, and pushing fixtures to Mailpit.
- Deployment: a Docker image, Docker Compose and Nomad setups with Presidio.
- Releases: binaries for Linux, macOS and Windows (amd64 and arm64) with
  checksums, SBOMs, keyless cosign signatures and build provenance, macOS
  binaries signed with a Developer ID and notarized by Apple, a
  signed multi-arch image at `ghcr.io/martinsuchenak/exemplar`, and a
  Homebrew cask (`brew install --cask martinsuchenak/tap/exemplar`).

[Unreleased]: https://github.com/martinsuchenak/exemplar/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/martinsuchenak/exemplar/releases/tag/v0.1.0
