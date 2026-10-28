# Generating synthetic data

exemplar generates files, emails and records about fake people from its
**person registry**, so a contact record, the CSV that lists that contact and
the emails they send all name the same person. Generation lives in
`internal/synth` (pure content, no database) and `internal/generate` (stores
the content as fixtures through the fixture service, so the usual access rules
and audit apply). It uses only the Go standard library.

## Quick start

```bash
exemplar datasets create --name demo
exemplar generate <dataset> --kind emails --count 24          # every shape, twice
exemplar generate <dataset> --kind files --formats pdf,xlsx --count 6
exemplar generate <dataset> --kind records --count 10 --people 4
exemplar generate <dataset> --kind records --schemas product,quote --count 20
exemplar schemas list                                              # the record schemas this server has
exemplar fixtures list <dataset> --person <person-id>           # everything about one person
```

The same request over the API is `POST /api/v1/datasets/{id}/generate` with a
JSON body (see [api.md](api.md)).

`task sample-data` fills a server with a ready-made set: workspaces
`sample-emails` (every shape), `sample-documents` (every format),
`sample-records` (contacts, companies, projects, opportunities and
addresses), `sample-sales` (the product catalogue and quotes priced from it)
and `sample-tasks` (one task per person), all about the same 20 people, from
one seed. It uses `EXEMPLAR_SERVER` (default `http://localhost:8080`) and
`EXEMPLAR_API_KEY`, asking for the key when it is not set, and leaves
workspaces that already hold fixtures alone; `scripts/sample-data.zsh
--replace sample-records` deletes one and generates it again.

## The person registry

A person has a name, email, mobile, job title, company and postal address.
People are grouped a few to a company; a company's domain, ABN, phone, website
and address are derived from its name, so they are the same everywhere it
appears. Every address is on a reserved `.example` domain (RFC 2606), so
generated mail can never reach a real mailbox.

- `exemplar persons generate --count N` adds people.
- Generation reuses people already in the registry and only creates new ones
  when there are too few for `--people`.
- Any key that can read can list and read people; they are fake by
  construction. Creating them needs a write key.

## Request fields

| Field | Default | Meaning |
|-------|---------|---------|
| `kind` | required | `files`, `emails` or `records` |
| `count` | required | 1–500 items; a thread email counts once but yields several messages |
| `formats` | all | Files: `csv`, `docx`, `gif`, `html`, `jpg`, `pdf`, `png`, `rtf`, `txt`, `xlsx` |
| `shapes` | all | Emails: see below |
| `from` | | Emails: template email fixture IDs (1–50) to render instead of shapes; see [templates](#emails-from-templates) |
| `schemas` | all | Records: schema names; `exemplar schemas list` (or `GET /api/v1/record-schemas`) shows what the server has |
| `profile` | `insurance` | Business vocabulary: `insurance` or `manufacturing` |
| `people` | up to 10 | How many registry people the content is about (1–200; emails need 2; with `from`, at least the largest template's people, default twice that and at least 10) |
| `seed` | random | Returned in the result; see reproducibility |
| `scenario`, `tags` | | Labels for every fixture; emails default their scenario to their shape |

Generation cycles through the lists: `--count 6 --formats pdf,csv` gives three
of each.

## Files

Each file is a business document (title, reference, date, sender and
recipient, paragraphs and line items) written in the profile's vocabulary.
Spreadsheet formats (`csv`, `xlsx`) list the request's people as rows. PDFs
are real PDF 1.4 (Helvetica, A4, as many pages as needed); DOCX and XLSX are
minimal Office Open XML packages; images are PNG, JPEG and GIF of coloured
shapes. The first person is linked as `author`, the others as `mentioned`.

## Email shapes

Every message is RFC 5322 with CRLF line endings, a `Message-ID` and a
date. Each shape covers a structure mail parsers commonly get wrong.

| Shape | Structure |
|-------|-----------|
| `plain` | `text/plain` only |
| `html` | `multipart/alternative`: text and HTML |
| `reply` | `Re:` subject, `In-Reply-To` and `References`, quoted text under a Gmail- or Outlook-style header, `<blockquote>` in HTML |
| `forward` | `Fwd:` subject, original inline under "Forwarded message" |
| `forward-attached` | Original attached as `message/rfc822` |
| `attachment` | `multipart/mixed` with a generated PDF, DOCX or XLSX |
| `inline-image` | `multipart/related` HTML whose `<img src="cid:…">` matches the image part's `Content-ID` |
| `calendar` | `text/calendar; method=REQUEST` with a `VEVENT`, organiser and attendees |
| `auto-reply` | Out-of-office with `Auto-Submitted: auto-replied` |
| `bounce` | `multipart/report; report-type=delivery-status` from `mailer-daemon`, with the original attached |
| `newsletter` | Bulk mail with `List-Id`, one-click `List-Unsubscribe` and `Precedence: bulk` |
| `thread` | 3–5 messages between two people, each replying to and quoting the one before, with a growing `References` chain |

Emails are linked to their registry people as `from`, `to` and `cc`. A
bounce's `mailer-daemon` and a newsletter's sender are not registry people and
are not linked. With `--scenario`, the scenario applies to every email and the
shape becomes a `shape-<name>` tag.

## Emails from templates

Hand-written shapes never quite match real mail: Outlook's nested
`multipart/related`, a Gmail quote header, a signature image, a mailing-list
footer. A template keeps that real shape. Any approved real-derived email is a
template, and so is any email with linked people, such as a generated one:

```bash
exemplar generate <dataset> --kind emails --count 20 --from <fixture-id>,<fixture-id>
```

Each email is one of the templates, in turn, with its linked people (`from`,
`to`, `cc`…) swapped for people picked from the registry:

- Every form of a person's name (`First Last`, `Last, First`, each name alone, all capitals), their address, phone, job title and street. Names, job titles, company names and streets match only as written or in capitals, since many names are words: swapping "Grace Price" leaves "the grace period" and "the price" alone.
- Their company: name, stem (`Noosa Engineering`), the compact form a domain uses (`noosaengineering`), domain (including other addresses and subdomains at it), ABN and phone.
- Message-ID, In-Reply-To and References get fresh IDs on the new domain; IDs that matched still match.

People who worked for one company in the template get people from one company
where the registry has them, and different companies stay different. The
template's own people are not reused while others are free.

Everything else is kept: MIME structure, encodings, wording, dates, and
attachments (which the anonymiser has already replaced). People mentioned in
the text but not linked, such as a third party in a forwarded message, keep
their (already fake) names in every copy.

Rules:

- Real-derived templates must be approved, even for admins: rendering copies their wording out of quarantine.
- The caller must be able to read the template and change the target dataset; members use the approved copy in a global dataset.
- Generated emails are `source: synthetic` with `generator: template:<fixture-id> seed=<seed>`, tagged `template`, with the template's scenario (or `template`).
- They keep the template's `ingest_batch`, so `exemplar purge <batch>` removes them along with the real-derived originals.

## Records

Records are JSON fixtures (kind `record`) with a `record_schema`. Every
schema is a TOML file (see [record schema files](#record-schema-files)): eight
are built in, and a server can load more. The request cycles through its
schemas; each schema starts at a different person, spaced evenly through the
request's people by its place among the *request's* schemas, and moves on one
person per record. So `--count 30 --people 20` with five schemas gives 6 of
each, the contacts about 6 different people and the five schemas between them
about all 20, with no two records alike. A schema repeats a person only once
it has used every one. Company records go through the distinct companies
instead, and catalogue records through the catalogue.

| Schema | Fields | Links |
|--------|--------|-------|
| `contact` | person_id, names, email, phone, job_title, company, address | `contact` |
| `company` | name, abn, phone, website, industry, address, contacts (person IDs) | every contact as `contact` |
| `project` | name, code, company, manager_person_id, status, start/end dates, budget, description | `manager` |
| `opportunity` | name, company, contact_person_id, stage, value, source, expected_close, notes | `contact` |
| `address` | person_id, street, suburb, state, postcode, country | `resident` |
| `product` | sku, name, category, unit, unit_price, description: one catalogue entry | none |
| `quote` | quote_number, name, company, contact_person_id, status, issued, valid_until, items (catalogue lines with qty and amount), subtotal, gst, total, notes | `contact` |
| `task` | task_number, name, description, status (todo, in-progress, blocked, in-review, done), priority, assignee_person_id, assignee, company, created, due, estimate_hours | `assignee` |

The product catalogue has 24 entries per business profile. Entry *n* depends
only on the schema file, the profile and *n*, never on the request's seed, so
product 7 is the same in every request, dataset and instance, and every line
of every quote is a product record with the same SKU, name and price. A
quote's amounts are exact: each line is `qty` × `unit_price`, GST is 10% of
the subtotal rounded to the cent, and the total adds up.

Some registry people have no company: ingest creates them for senders at a
public mail provider (`gmail.com` and the like). For such a person every
`company.*` value is empty (`company.address` is `null`, `company.contacts`
is `[]`), never a company invented from no name. A quote or project about
one is named after them (`QU-482917 - Jo Bloggs`), and company records skip
them; a request for company records about people who all lack one is refused
as invalid.

Uploaded JSON can be a record too: pass `record_schema` (any lower-case name)
with the upload.

## Record schema files

A schema is one TOML file named after it (`quote.toml`). The built-in files
are in `internal/synth/schemas/`; `serve --record-schemas-dir DIR` (env
`RECORD_SCHEMAS_DIR`, config `generate.record_schemas_dir`) adds every
`*.toml` file in `DIR`, and a file named like a built-in replaces it (the
server logs that it did). The server reads them when it starts: a change needs
a restart, and a file with a mistake stops the server with the file and field
named, so a bad schema never fails halfway through a request. Clients (CLI,
MCP) use whatever the server they talk to has loaded.

| Key | Meaning |
|-----|---------|
| `name` | The schema name: lower-case letters, digits and `-`; the file is `<name>.toml` |
| `description` | Shown by `exemplar schemas list` |
| `step` | What successive records go through: `person` (default), `company` or `catalogue` |
| `title` | A template naming the record, such as `"{name}"` or `"{person.name}"`; fixture names are built from it |
| `[[links]]` | The people the record refers to: `role`, and `value` = `person.id` or `company.contacts` |
| `[[fields]]` | The fields, **in order** (see below) |
| `[catalogue]` | For `step = "catalogue"`: `size`, and `[[catalogue.fields]]` every entry has |
| `[lists.<profile>]` | Word lists per business profile (`insurance`, `manufacturing`), or `[lists.all]` for every one |

Each field has a `name` (lower-case, `_`, not `schema`, which every record
gets) and exactly one type key. Fields are evaluated top to bottom, so a field
can use any field above it, and the order is the order random values are
drawn in: changing it changes what a seed gives. The JSON keys come out
sorted.

| Type | Example | Gives |
|------|---------|-------|
| `value` | `value = "company.abn"` | The record's person (`person.id`, `first_name`, `last_name`, `name`, `email`, `phone`, `job_title`, `company`, `address`, `address.street`, `.suburb`, `.state`, `.postcode`, `.country`), its company (`company.name`, `short` without Pty Ltd, `domain`, `abn`, `phone`, `website`, `industry`, `address`, `address.*`, `contacts`), or a field above |
| `template` | `template = "{label} - {company.short\|person.name}"` | Text with `{…}` filled from the same names, or from `{label}`, `{title}`, `{subject}`, `{sentence}` (text in the profile's words). `{a\|b}` tries each alternative in turn and uses the first that is not empty; a generator later in the list draws only when it is reached |
| `ref` | `ref = "QU"`, `ref = ""` | A reference such as `QU-482917`; `""` uses the mixed prefixes (`PL`, `CL`, `JOB`…) |
| `pick` | `pick = ["draft", "sent"]`, `pick = "@product"` | One item of a list, or of a named list for the request's profile |
| `int` | `int = [1, 20]` | A whole number in the range |
| `money` | `money = [5000, 250000]` | Dollars, held in cents |
| `date` | `date = { within_days = 60 }`, `date = { after = "issued", days = [14, 45] }` (or `months`) | A date near the generator's base date, or after a date field above |
| `text` | `text = "sentence"`, `text = { paragraph = [1, 2] }` | Prose in the profile's words: `sentence`, `title`, `label`, `subject`, or a paragraph of min plus up to extra sentences |
| `lines` | `lines = { catalogue = "product", count = [1, 6], qty = [1, 20] }` | Distinct catalogue entries, each with every entry field plus `qty` and `amount` |
| `sum` | `sum = "items.amount"`, `sum = ["subtotal", "gst"]` | The total of money fields, or of a lines field's money column |
| `percent` | `percent = { of = "subtotal", rate = 10 }` | A percentage of a money field, rounded to the cent |
| `nth` | `nth = "@product"` | Catalogue entries only: the list's item for the entry's number, so entries take the list in order and never repeat one (the catalogue may not be longer than the list) |

Money fields (`money`, `sum`, `percent`) take `format = "display"` (`"$1,234.56"`,
the default) or `format = "number"` (`1234.56`); sums work on cents, whatever
their inputs show. Date fields take a Go layout in `format` (default
`2006-01-02`). A lines field's `amount` takes the catalogue's `unit_price`
format.

A catalogue schema's records are its entries, in order, and link no people;
its entry fields cannot read a person or company. A catalogue that a `lines`
field uses needs a money `unit_price`. A minimal schema of your own:

```toml
name = "ticket"
description = "A support ticket raised by a client contact"
title = "{reference} {subject}"

[[links]]
role = "reporter"
value = "person.id"

[[fields]]
name = "reference"
ref = "TK"

[[fields]]
name = "subject"
text = "subject"

[[fields]]
name = "reporter"
value = "person.email"

[[fields]]
name = "priority"
pick = ["low", "normal", "high", "urgent"]

[[fields]]
name = "opened"
date = { within_days = 30 }
```

## Reproducibility

The seed drives two separate streams: one picks people, the other writes the
content. The same seed with the same people therefore gives byte-identical
fixtures, whatever else is in the registry. Fixture names start with
`g` and six hex digits of the seed (`g00007b-001-…`), so runs with different
seeds never clash in one dataset, and rerunning a seed into the same dataset
fails with a name conflict instead of duplicating it.

Fixture provenance records `source: synthetic` and
`generator: exemplar-synth/2 seed=<seed>`; a record's also names its schema
file, `exemplar-synth/2 schema=quote@3fa1c2d0 seed=<seed>`, where the hash is
of the file's content, so you can tell which definition made it.
Emails from templates record `template:<fixture-id> seed=<seed>`. The same
seed, people, templates and schema files give the same content.

Version 2 changed records once: each schema is spaced by its place among the
request's schemas, not among every schema there is, so adding a schema file
never changes what the others give. A seed from version 1 reproduces its
records only on a build from before the change.

## Into a mailbox

`exemplar push-mailpit <dataset>` sends the generated emails to Mailpit,
tagged with a run ID; see [cli.md](cli.md).

## Limits and failures

Up to 500 items and 200 people per request. Items are stored one by one; if one
fails (for example a name clash or the size limit), the ones before it stay and
the error says how many were created.

## Word pools

Names, companies, places and the insurance and manufacturing vocabulary were
ported from db-anonymizer2 and are maintained in `internal/synth/pools.go`.
