# Anonymising real email and documents

exemplar can take real `.eml` files, and documents such as Markdown, CSV,
JSON, HTML and Twig templates, and turn them into fixtures that are safe to
share. It replaces personal and commercial data with consistent fakes, and
keeps everything a parser depends on: an email's MIME structure, a CSV's
cells, a JSON document's keys, a template's code. The email anonymiser is a
Go port of mail-processor's `anonymise-corpus.py`; see
[Documents](#documents) for the other formats.

Real-derived fixtures go into a quarantine dataset. An admin reviews each one,
then approves or rejects it. Only approved fixtures leave quarantine, through
promote. The access rules are in [security.md](security.md).

## Workflow

```bash
# 1. A quarantine dataset (admin key)
exemplar datasets create --name inbox-2025 --scope quarantine

# 2. Check what would be replaced. The report shows ORIGINAL data: read it, don't keep it.
exemplar ingest <dataset-id> ./mail --dry-run

# 3. Ingest for real: fixtures are stored as <batch>-001.eml, <batch>-002.csv, …
exemplar ingest <dataset-id> ./mail --batch inbox-2025-01

# 4. Review each fixture, then approve or reject it
exemplar fixtures download <fixture-id>
exemplar approve <fixture-id>...
exemplar reject <fixture-id>...

# 5. Copy the approved fixtures into the next version of a global dataset
exemplar promote <dataset-id> --name inbox --description "Reviewed anonymised emails"

# 6. Use approved emails as templates for as many synthetic variants as you need
exemplar generate <workspace-id> --kind emails --count 50 --from <global-fixture-id>

# Removal on request: delete a whole batch, wherever it was copied or rendered
exemplar purge inbox-2025-01 --yes
```

Ingest runs on the server. The CLI streams the files up in one
`multipart/form-data` request. The server reads them into memory and never
writes them to disk. It stores only the anonymised output.

Every file in a request is one batch. The engine first reads every file in
the batch: each email's senders and recipients, and the names detection finds
in each document. Then it rewrites each file. A name from one email's headers,
or found in one document, is therefore found in all the others, and gets the
same fake everywhere. One file failing (a `.msg`, an unsupported charset, a
malformed message or document, one over the nesting or part limits) does not
stop the rest, whether it fails while the batch is read or while it is
rewritten; its row in the result carries the error.

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

- MIME structure, transfer encodings and charsets (non-UTF-8 text is converted to UTF-8 and re-encoded as quoted-printable). Multipart boundaries are the sender's text, so each is replaced with a keyed one (`=_<hex>`).
- HTML markup; only text and attribute values are replaced.
- Dates, subjects' structure (`Re:`, `Fwd:`), greetings and sign-offs (the profile's stop words).
- Public mail providers' domains and system senders such as `noreply@` (the profile's keep lists).
- Attached emails (`message/rfc822`) are anonymised recursively; delivery reports keep their fields with addresses replaced.

### Headers

Every header a message or MIME part keeps has a kind, and the kind says how
its value is anonymised. Any header without one is dropped: `Received`, DKIM
and ARC signatures, `X-MS-Exchange-*`, `X-Originating-IP` and the like on a
message; `X-*`, `Comments`, `Content-MD5`, `Content-Features` and anything
else on a part. They carry addresses, IPs and tenant ids that nothing
downstream reads. The table is `messageHeaderKinds` and `partHeaderKinds`
in `internal/anonymise/headers.go`.

| Kind | Headers | Value |
|------|---------|-------|
| Address | `From`, `To`, `Cc`, `Bcc`, `Reply-To`, `Sender`, `Return-Path`, `Disposition-Notification-To` | Rebuilt from the parsed mailboxes: each address mapped to its registry person, display names replaced, comments dropped. An unparseable list keeps only the addresses in it, faked; `Return-Path: <>` stays |
| ID | `Message-ID`, `In-Reply-To`, `References`, `Content-ID` | Only the `<id>`s, each a keyed hash; text around them is dropped |
| Free text | `Subject`, `Thread-Topic`, `X-Mailer`, `User-Agent`, `Content-Description` | Detected and replaced like a body, at every level |
| URL list | `List-Unsubscribe`, `List-Post`, `List-Help` | Only the `<URL>`s, faked like other URLs (`List-Post: NO` stays) |
| List-Id | `List-Id` | The phrase as free text, the `<id>` as a host |
| Location | `Content-Location` | A URL gets the fake domain; a relative name is renamed like an attachment |
| Structure | the rest | Rebuilt from its parsed tokens before anything reads the message, or dropped when it does not parse |

The structural rules keep only what each header's grammar allows, so a
comment, an extra word or an unknown parameter cannot carry personal data
through:

- `Content-Type`: the media type and the parameters a reader needs (`charset`,
  `boundary`, `name`, `method`, `report-type`, `type`, `start`, `format`,
  `delsp`, `reply-type`, `protocol`, `micalg`), each checked; `name` is
  renamed with the attachment and `start` faked with the `Content-ID` it
  names.
- `Content-Disposition`: `inline` or `attachment` and the file name, which is
  renamed; dates, sizes and other parameters are dropped.
- `Content-Transfer-Encoding`: one of the five standard encodings.
  `MIME-Version`: `1.0`.
- `Content-Language`, `Accept-Language`: language tags (with weights for
  `Accept-Language`).
- `Date`: reformatted as `Mon, 3 Feb 2025 09:15:00 +1100`, without comments.
- `Thread-Index`: each 22- or 5-byte block replaced by a keyed one, so
  emails in one conversation still share a prefix.
- `Auto-Submitted`, `Precedence`, `Importance`, `X-Priority`,
  `X-Auto-Response-Suppress`, `X-MS-Has-Attach`, `List-Unsubscribe-Post`:
  only their known values. `X-Autoreply` and `X-Autorespond` become `yes`,
  since classifiers read only that they are there. `X-MS-TNEF-Correlator` is
  kept only when empty.

A header that may appear only once (anything but the address and URL lists)
keeps its first copy, the one parsers read; later copies are dropped, so a
second `Content-Type` cannot slip a value past the rules. The result's
dropped-header count includes the headers dropped by these rules.

Text before the first boundary and after the last (preamble and epilogue,
where list footers sit) goes through the same detection and replacement as
bodies.

The regression test for all of this is Strix's slot corpus
(`TestPartHeaderSlotsLeakNothing`, `TestMessageHeaderSlotsLeakNothing` in
`internal/anonymise/headers_test.go`): a unique address in every header slot
of a part and a message, plain, in comments, in parameters and duplicated,
none of which may survive.

A message may nest multiparts and attached emails at most 32 levels deep in
all and have at most 10,000 parts; deeper or larger structures are refused
(`mimetree.MaxDepth`, `mimetree.MaxParts`), and that email alone fails. Parsing costs one pass over the
message per nesting level, so a hostile message cannot make it slow.

## Documents

The file name's extension picks the format, case aside. Each document is
searched the way an email body is (Presidio, the patterns, the batch's names)
and only its text is rewritten. Afterwards the output is parsed again and must
have the same structure as the input, or the file fails rather than coming
back broken.

| Format | Extensions | What is searched | What stays exactly as sent |
|--------|------------|------------------|----------------------------|
| Text | `.txt`, `.text`, `.log` | The whole file | Everything but the replaced values |
| Markdown | `.md`, `.markdown` | The whole file: text, code, front matter, link and image targets (URLs and `mailto:` addresses) | The markup |
| CSV, TSV | `.csv`, `.tsv` | Each row, header row included | Every cell that does not change, with its quoting, and the line endings. A changed cell keeps its quotes, or gains them if the fake needs them |
| JSON, JSON Lines | `.json`, `.jsonl`, `.ndjson` | Every string value | Keys, key order, spacing, numbers, booleans and the escaping of unchanged values |
| HTML | `.html`, `.htm` | Visible text, `<title>`, `<meta>` content, comments, and the `alt`, `title`, `href`, `src`, `placeholder`, `aria-label` and `value` attributes | Markup, scripts and styles (a value found elsewhere is still replaced in them) |
| Twig, Jinja | `.twig`, `.j2`, `.jinja`, `.jinja2` | Literal text (as HTML for `page.html.twig`), comments, and quoted strings inside tags | Every tag as written outside its strings: variables, filters and keywords are code, so `{{ customer.name }}` is untouched while `{% set signer = "Jane Doe" %}` is not. `{% verbatim %}` and `{% raw %}` bodies are literal text |

A CSV column or JSON key named for an entity marks its values as that entity,
even where the detector would not find them: `name`, `full_name`,
`first_name`, `last_name`, `surname`; `email`, `e-mail`, `email_address`;
`phone`, `mobile`, `tel`; `company`, `organisation`, `employer`. Only letters
and digits of the label count, in any case.

Text must be UTF-8, with or without a byte-order mark, or UTF-16 with one;
the output keeps the input's encoding. Other encodings, and files holding NUL
bytes, are refused rather than guessed. A document links to no registry
people: a person found only by name has no address to match one by.

The names found while the batch is read are mapped to their fakes before any
file is rewritten, as an email's headers are. So "Sarah Lane" in one file and
`sarah.lane@…` in another get matching fakes (`kenneth.huang@…` for Kenneth
Huang), and the same files with the same key always give the same output.
Reading a document's names first means each document goes to Presidio twice. Its texts are sent in chunks of about 32 KiB, so a
CSV of thousands of rows takes a few calls, not one per row.

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

- Emails and the document formats above are read; the extension picks the format, case aside. Any other file (PDF, DOCX, RTF, XLSX, images) fails on its own, with an error naming the supported extensions. `.msg` (Outlook) files are rejected with a hint to convert them to `.eml` first. exemplar serves uploaded `.msg` fixtures as they are but cannot anonymise them yet.
- In JSON, keys and numbers are never changed: a phone number stored as a number, or a name used as a key, survives. In templates, nothing is matched across a tag.
- Charsets other than UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 are rejected per file.
- Attachment contents are replaced, not anonymised: a PDF's text does not survive.
- Dates are kept, so a timeline can be recognised by someone who knows it.
- Detection is not perfect. Names Presidio misses and that never appear in a header, or identifiers with no pattern (customer numbers, for example), can survive. This is why every fixture is reviewed before it leaves quarantine.
- One request is capped at `storage.max_ingest_mb` (default 200 MiB) and 1000 files.
