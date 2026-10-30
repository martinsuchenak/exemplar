# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
A release takes its notes from its section here (`task release` refuses a
version without one); see [docs/development.md](docs/development.md#releasing).

## [Unreleased]

### Added

- Anonymise and ingest read PDFs, rewritten in place by PDFium (Chrome's
  PDF engine) run inside exemplar as WebAssembly: no CGO and no other
  service, and each file gets its own engine with a memory cap and a
  deadline. Each text object holding a match is drawn again with the fakes
  at the same place, size and colour, in the standard font nearest its own;
  annotations and form fields are flattened into the page first, links are
  kept with their addresses rewritten, and every image is replaced with a
  grey one. Metadata, bookmarks and other strings outside the pages are
  rewritten; scripts, embedded files, XMP metadata, the structure tree and
  page thumbnails are dropped and listed. Password-protected and scanned
  PDFs are refused. A PDF attached to an email is rewritten in place when
  the profile says `attachments = "anonymise"`. See
  docs/anonymisation.md#pdf. The binary grows by about 12 MB; its
  third-party licences are in THIRD_PARTY_NOTICES.md, shipped in the
  archives and images.
- Bank account numbers and BSBs are found by their labels (`BSB: 062-000`,
  `Account No. 1234 5678`) and by column or key labels (`BSB`, `Account
  Number`), in every format, and replaced digit by digit.

## [0.3.0] - 2026-10-30

### Added

- Anonymise and ingest read XLSX workbooks: cell text and the strings
  cells share, formulas' strings, comments old and threaded, tables, pivot
  caches, charts, text boxes, links to other workbooks, connections and the
  file's properties. A sheet named for a person is renamed within Excel's
  rules, and every formula, chart and pivot cache that refers to it
  follows. A column labelled `Name`, `Email`, `Phone`… marks the cells
  below it, as in a CSV, including phone numbers typed as numbers. Macros,
  printer settings and other parts nothing reads are dropped and listed.
  An XLSX attached to an email is rewritten in place when the profile says
  `attachments = "anonymise"`. See docs/anonymisation.md#xlsx.

### Changed

- A column of people is found without a label that says so. Once
  detection has run, a CSV column, sheet column or JSON key whose values
  are mostly known people (by distinct values, or by cells when one name
  fills most rows) has its other name-like values taken for people too.
  Before, a column such as `Account Manager` lost only the names the
  detector happened to find. See docs/anonymisation.md#documents.

- A value under a labelled CSV column or JSON key that is a placeholder
  (`N/A`, `TBD`, `Total`, `unknown`…), or a value under a phone label with
  fewer than 6 digits, is no longer taken for what the label names. Before,
  its fake replaced it everywhere in the file, so every "Total" could
  become a person's name. Detection still reads these values.
- A CSV column or JSON key whose label ends in a phone or email label
  (`Mobile Phone`, `homePhone`, `Work Email`) marks its values as one, as
  `Phone` and `Email` do. Before, numbers in such a column were left as
  written unless the detector knew their country's format.

### Security

- A name written as a mention (`@Sarah Lane`, as Outlook, Teams and Office
  comments write them) is replaced, in emails and every document format;
  before, a known name right after an `@` was left as written. The `@` of
  an address still only ever precedes a faked domain.
- A phone number written with its calling code (`+234 801 234 5678`) is
  replaced in any country's format. Before, only Australian numbers and
  the formats Presidio knows were found, so most other countries' numbers
  were left as written in free text.
- A phone number right after a name is replaced. Presidio can run a name
  into the number beside it ("Sarah Lane 0412"), and that name's match
  kept the number from being found; a detected name now loses its words
  with no letter first. This affects every format.

## [0.2.0] - 2026-10-30

### Added

- MCP: `list_datasets` takes a `query`, words matched in dataset names,
  descriptions and tags, and `search_fixtures` a `query` matched in fixture
  names.
- CLI: every command that takes a dataset takes its name as well as its id
  (`exemplar generate crm-contacts …`), except `datasets delete`;
  `datasets list` and `fixtures list` take `--query`. Over REST,
  `GET /api/v1/datasets/{id}` takes a name and both lists take `?query=`.
- `exemplar review <dataset>` walks a quarantine dataset's unreviewed
  fixtures one at a time: each opens in its default app, you approve,
  reject or skip it, and its local copy is deleted.
- `exemplar fixtures download-all <dataset> --dir DIR` writes every fixture
  that matches the list filters to a directory.
- The config file is also read from `~/.config/exemplar/` (or
  `$XDG_CONFIG_HOME/exemplar/`, `%AppData%\exemplar\` on Windows) when the
  current directory has none, so the CLI can keep `client.server` there.
- Server: `storage.max_concurrent_runs` (default 2) caps how many ingest and
  anonymise runs are held at once, together; a run past the cap gets `429`
  before its upload is read, where before any number of concurrent runs (one
  per key, and a write key can mint more keys) buffered their uploads in
  memory together.

### Changed

- MCP: `search_fixtures` takes `dataset`, a dataset's id or name, instead of
  `dataset_id`, and returns the dataset it searched.

### Security

- Anonymise: RTF and DOCX files that split into millions of tokens (a file
  of nothing but escapes, or of nothing but tiny tags) are refused on a
  token budget, and a DOCX may no longer expand past 256 MiB however well it
  compresses, where before one such file multiplied into many times its size
  in memory while it parsed.
- Revoking a key or disabling an account now also stops transfers already in
  flight (large fixture downloads, exports, imports, ingests and anonymise
  runs): the authentication middleware re-checks the key about every 10
  seconds while a transfer streams, where before a revoked key could finish a
  transfer it had started.
- A long upload or download that moves no bytes for two minutes is ended,
  where before a client that stopped sending or reading held the request,
  and an ingest or anonymise run slot with its memory, for up to an hour.
- Ingest checks for an admin key and a configured anonymiser before it takes
  a run slot, so other keys cannot hold the shared slots.
- `backup` creates its database snapshot with mode 600 from the start,
  where before it was briefly readable by others while it was written.
- DOCX errors name a part only by the name Office gives it, never a part
  name, element name or relationship id the sender chose.
- Key names follow the account name rules (1-64 letters, digits, `.`, `_`
  or `-`); keys already issued keep their names.
- `golang.org/x/net` 0.62.0 (GO-2026-6860, GO-2026-6866; exemplar did not
  use the affected package).

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

[Unreleased]: https://github.com/martinsuchenak/exemplar/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/martinsuchenak/exemplar/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/martinsuchenak/exemplar/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/martinsuchenak/exemplar/releases/tag/v0.1.0
