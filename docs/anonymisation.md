# Anonymising real email

exemplar can take real `.eml` files and turn them into fixtures that are safe
to share. It replaces personal and commercial data with consistent fakes, and
keeps everything a mail parser depends on. The anonymiser is a Go port of
mail-processor's `anonymise-corpus.py`.

Real-derived fixtures go into a quarantine dataset. An admin reviews each one,
then approves or rejects it. Only approved fixtures leave quarantine, through
promote. The access rules are in [security.md](security.md).

## Workflow

```bash
# 1. A quarantine dataset (admin key)
exemplar datasets create --name inbox-2025 --scope quarantine

# 2. Check what would be replaced. The report shows ORIGINAL data: read it, don't keep it.
exemplar ingest <dataset-id> ./mail --dry-run

# 3. Ingest for real: fixtures are stored as <batch>-001.eml, <batch>-002.eml, …
exemplar ingest <dataset-id> ./mail --batch inbox-2025-01

# 4. Review each fixture, then approve or reject it
exemplar fixtures download <fixture-id>
exemplar approve <fixture-id>...
exemplar reject <fixture-id>...

# 5. Copy the approved fixtures into the next version of a global dataset
exemplar promote <dataset-id> --name inbox

# 6. Use approved emails as templates for as many synthetic variants as you need
exemplar generate <workspace-id> --kind emails --count 50 --from <global-fixture-id>

# Removal on request: delete a whole batch, wherever it was copied or rendered
exemplar purge inbox-2025-01 --yes
```

Ingest runs on the server. The CLI streams the files up in one
`multipart/form-data` request. The server reads them into memory and never
writes them to disk. It stores only the anonymised output.

Every file in a request is one batch. The engine first reads every sender and
recipient in the batch, then rewrites each email. A name from one email's
headers is therefore found in the bodies of all the others. One file failing
(a `.msg`, an unsupported charset, a malformed message) does not stop the
rest; its row in the result carries the error.

## What is replaced

| Data | Found by | Replaced with |
|------|----------|---------------|
| People in headers | Address headers | A registry person; the same real address always maps to the same person |
| Names in text | Presidio (`PERSON`), plus every name from the batch's headers | Fake first and last names, case kept, each real name token mapped once |
| Email addresses | Pattern | The person's fake address, or a fake local part at a fake domain |
| Domains and companies | Addresses, URLs, `Pty Ltd`-style suffixes | A fake company; its domain is the company name on `.example`. A company name learned from a domain (`acme` from `acme.com.au`) is also replaced inside identifiers: `acme-prod`, `logo_acme.png`. Every domain in the address headers is also replaced where it appears alone, as in a DMARC subject |
| Subdomains | Domains anywhere | Generic labels (`mail`, `www`, `api`, `reply`, `mx1`…) kept; any other label (a client, product or system name) replaced with a fake, the same every time |
| URLs | Pattern | Same scheme on the fake domain; path and query replaced |
| Profile terms | The profile's `terms` list | The company-style fake (the same company as the domain it names), in any case, also inside identifiers; a term wins over a name a display name made of it |
| Phone numbers | Australian patterns | Fake digits; leading zeros and the first other digit kept (`04…`, `(02)…`, `13…`) |
| ABN, ACN | Labelled patterns | Fake digits, same layout |
| Money | `$`, `AUD` patterns | Fake digits, same layout |
| Street addresses, PO boxes | Pattern | A synthetic street address |
| Message-ID, In-Reply-To, References, Content-ID | Header and `cid:` | Keyed hashes, so threads and inline images still match |
| Attachments | MIME disposition and type | A synthetic file of the same format, renamed `attachment-<hex>.<ext>` |

Images keep their size class. mail-processor treats images under 50 KB as
signature images. A replacement stays on the same side of that line: PNG and
JPEG files are padded with a metadata chunk, so they still decode.

## What is kept

- MIME structure, boundaries, transfer encodings and charsets (non-UTF-8 text is converted to UTF-8 and re-encoded as quoted-printable).
- HTML markup; only text and attribute values are replaced.
- Dates, subjects' structure (`Re:`, `Fwd:`), greetings and sign-offs (the profile's stop words).
- Public mail providers' domains and system senders such as `noreply@` (the profile's keep lists).
- Attached emails (`message/rfc822`) are anonymised recursively; delivery reports keep their fields with addresses replaced.

Headers not on the allowlist are dropped: `Received`, DKIM and ARC signatures,
`X-MS-Exchange-*`, `X-Originating-IP` and the like. They carry addresses, IPs
and tenant ids that nothing downstream reads. The result reports how many were
dropped. The allowlist is `keepHeaders` in `internal/anonymise/engine.go`. Kept
headers that hold free text (`Subject`, `Thread-Topic`, `Content-Description`,
`X-Mailer`, `User-Agent`) go through the same replacements as bodies, and so
does each MIME part's `Content-Description`.

MIME parts (and nested multiparts) keep only the headers they need to parse
(`Content-Type`, `Content-Transfer-Encoding`, `Content-Disposition`,
`Content-ID`, `Content-Language`, `Content-Description`, `Content-Location`,
`Content-Features`, `Content-MD5`, `MIME-Version`); any other part header,
such as `X-*` or `Comments`, is dropped and counted with the dropped message
headers. A part's `Content-Location` URL gets the fake domain; a relative one
is renamed like an attachment. Text before the first boundary and after the
last (preamble and epilogue, where list footers sit) goes through the same
detection and replacement as bodies.

A message may nest multiparts and attached emails at most 32 levels deep in
all and have at most 10,000 parts; deeper or larger structures are refused
(`mimetree.MaxDepth`, `mimetree.MaxParts`). Parsing costs one pass over the
message per nesting level, so a hostile message cannot make it slow.

## Consistency and the key

Fakes are derived from an HMAC of the real value under the anonymisation key.
The same key gives the same fakes in every batch, so an email ingested today
threads with its reply ingested next month. The key never leaves the server.

The registry stores how real identities map to fakes, but only as keyed hashes:

- `identity_keys`: hash of a real address or display name → registry person.
- `name_tokens`: hash of a real name token → its fake.

Neither table holds a real value. Without the key, the hashes cannot be
reversed or checked against a guess. Each row records the key id
(`k-` + 12 hex characters), and so does each fixture (`anon_key_id`).

Changing the key starts a fresh mapping: people already in the registry stay,
but the same real person ingested under a new key becomes a new person.

### Setting up the key

```bash
head -c 32 /dev/urandom | base64 > /etc/exemplar/anon.key
chmod 600 /etc/exemplar/anon.key
exemplar serve --anon-key-file /etc/exemplar/anon.key --presidio-url http://localhost:3000
```

The server refuses a key file that its group or other users can read. The key
can come from `EXEMPLAR_ANON_KEY` instead, for container secrets. It must be
at least 16 bytes; surrounding whitespace is ignored. Back it up like any
other secret: losing it breaks consistency with earlier batches.

Ingest is off (the endpoint answers 400 "not configured") until the server has
both a key and a Presidio URL.

## Presidio

Presidio Analyzer finds names with named-entity recognition. Run it next to
the server and keep it private: emails are sent to it in plain text.

```bash
docker run -d --name presidio -e GUNICORN_CMD_ARGS="--timeout 300" \
  -p 127.0.0.1:3000:3000 mcr.microsoft.com/presidio-analyzer:latest
```

- `GUNICORN_CMD_ARGS="--timeout 300"` is needed: the first request loads the model, which takes longer than Gunicorn's default 30-second worker timeout, so the worker is killed and the request hangs.
- The image is about 1 GB.
- Default Presidio does not tag organisations, ABNs or Australian mobile numbers. exemplar's own patterns cover those, and companies are also learned from addresses.
- To tune recognisers, mount a configuration file and point `ANALYZER_CONF_FILE` at it (see the Presidio documentation).

exemplar sends each text part with the batch's header names as an ad-hoc deny
list, so Presidio tags those names even where its model would not. Presidio
reports offsets in characters; the client converts them to byte offsets.

## Profiles

A profile tunes the anonymiser for one source of mail. The built-in `default`
profile carries mail-processor's lists for Australian business email. An
unknown `--profile` is answered with the names of the profiles the server has.

| Field | Meaning |
|-------|---------|
| `name` | Lower-case letters, digits, `-` and `_`; what `--profile` takes |
| `description` | Free text |
| `score_threshold` | Presidio scores below this are ignored (default 0.5) |
| `keep_domains` | Domains and their subdomains left as they are |
| `keep_local_parts` | Address local parts left as they are (`noreply`, `support`, …) |
| `stop_words` | Words never replaced, even when tagged as a name |
| `automated_markers` | Markers of automated senders (`dmarc`, `report`, `noreply`, `alert`…): an address whose local part contains one is kept like `keep_local_parts`, and its display name ("DMARC Report") is not read as a person's name. Terms and company names inside a kept local part are still replaced (`acme-alerts@` becomes `<fake>-alerts@`) |
| `terms` | Names that identify where the mail comes from and no detector can know: your organisation, products, client codes. Always replaced |
| `two_level_suffixes` | Public suffixes such as `com.au`, so `acme.com.au` is read as company `acme` |

A profile file is complete on its own: fields it leaves out are empty, not
inherited. Start from a copy of
[`internal/anonymise/profiles/default.toml`](../internal/anonymise/profiles/default.toml).
A file in the profiles directory with the name of a built-in profile replaces
it.

## Dry run

`--dry-run` anonymises the files and returns, per file, every replacement as
`entity`, `original`, `fake`. Nothing is stored and no registry people or name
tokens are created; the audit log records that a dry run happened, without the
report. The response carries `Cache-Control: no-store`, and the CLI warns on
stderr that it contains original data.

Fakes are drawn from the key, so a dry run normally shows the fakes a real
ingest with the same key will produce.

## Limits

- `.msg` (Outlook) files are rejected; convert them to `.eml` first. exemplar serves uploaded `.msg` fixtures as they are but cannot anonymise them yet.
- Charsets other than UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 are rejected per file.
- Attachment contents are replaced, not anonymised: a PDF's text does not survive.
- Dates are kept, so a timeline can be recognised by someone who knows it.
- Detection is not perfect. Names Presidio misses and that never appear in a header, or identifiers with no pattern (customer numbers, for example), can survive. This is why every fixture is reviewed before it leaves quarantine.
- One request is capped at `storage.max_ingest_mb` (default 200 MiB) and 1000 files.
