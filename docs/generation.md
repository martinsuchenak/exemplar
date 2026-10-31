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
| `schemas` | the `schema_files` ones, else every one whose `request_lists` the request gives (see [API schemas](#schemas-added-through-the-api)) | Records: schema names; `exemplar schemas list` (or `GET /api/v1/record-schemas`) shows what the server has |
| `schema_files` | | Records: schema files (TOML text) for this request only; see [records for another system](#records-for-another-system) |
| `lists` | | Records: lists by name (`{"users": [{"id": 101, "name": "Sam"}]}`) that replace the schemas' lists or give their `request_lists` |
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
named, so a bad schema never fails halfway through a request. Admins can also
add schemas [through the API](#schemas-added-through-the-api), with no
restart. Clients (CLI, MCP) use whatever the server they talk to has loaded.

| Key | Meaning |
|-----|---------|
| `name` | The schema name: lower-case letters, digits and `-`; the file is `<name>.toml` |
| `description` | Shown by `exemplar schemas list` |
| `step` | What successive records go through: `person` (default), `company` or `catalogue` |
| `exemplar` | The oldest exemplar version the file works with, such as `"0.5"`; see [versions](#which-exemplar-a-schema-needs) |
| `request_lists` | Lists a generate request must give (`["users", "statuses"]`), such as a target system's own IDs; not also under `[lists]` |
| `title` | A template naming the record, such as `"{name}"` or `"{person.name}"`; fixture names are built from it |
| `[[links]]` | The people the record refers to: `role`, and `value` = `person.id` or `company.contacts` |
| `[[fields]]` | The fields, **in order** (see below) |
| `[catalogue]` | For `step = "catalogue"`: `size`, and `[[catalogue.fields]]` every entry has |
| `[lists.<profile>]` | Lists per business profile (`insurance`, `manufacturing`), or `[lists.all]` for every one: strings, numbers, true or false, or tables (`{ id = 3, name = "Won" }`); a request may replace any of them |

Each `[[fields]]` block makes one key of the record:

- `name` is the key, as it appears in the JSON: letters, digits and `_`, starting with a letter, so `companyName` and `company_name` both work (not `schema` at the top, which every record gets).
- One type key, from the table below, is the source of the data: `value` copies something that already exists (the record's person or company, or a field above), `ref` makes a new reference such as `CU-482917`, `pick` chooses from a list, `money` draws an amount, and so on.
- Options such as `optional`, `hidden` and `format` change how the value is kept or written.

```toml
[[fields]]
name = "customerCode"   # the key:    "customerCode": "CU-440279"
ref = "CU"              # the source: a new reference with this prefix

[[fields]]
name = "abn"            # the key:    "abn": "67 621 690 972"
value = "company.abn"   # the source: the record's company's ABN
```

Fields are evaluated top to bottom, so a field can use any field above it,
and the order is the order random values are drawn in: changing it changes
what a seed gives. The JSON keys come out sorted.

Any field can also take:

- `optional = 0.2`: the chance (0 to 1) it is `null` instead. It draws once more, before its value. A field that a `sum`, `percent` or date `after` uses cannot be optional, nor can a list or lines field a `sum` adds up, so a total never claims an amount the record does not hold.
- `hidden = true`: it is made, and fields below it can read it, but it is left out of the record. Use it to pick a `{ id, name }` once and write its parts into separate keys.

| Type | Example | Gives |
|------|---------|-------|
| `value` | `value = "company.abn"` | The record's person (`person.id`, `first_name`, `last_name`, `name`, `email`, `phone`, `job_title`, `company`, `address`, `address.street`, `.suburb`, `.state`, `.postcode`, `.country`), its company (`company.name`, `short` without Pty Ltd, `domain`, `abn`, `phone`, `website`, `industry`, `address`, `address.*`, `contacts`), `record.number` (the record's number in its schema in the request, from 1), `item.number` (in a list's item, from 1), a field above, or a key inside one (`status.id` for a picked `{ id, name }`, `address.street` for an object field) |
| `template` | `template = "{label} - {company.short\|person.name}"` | Text with `{…}` filled from the same names, or from `{label}`, `{title}`, `{subject}`, `{sentence}` (text in the profile's words). `{a\|b}` tries each alternative in turn and uses the first that is not empty; a generator later in the list draws only when it is reached |
| `ref` | `ref = "QU"`, `ref = ""` | A reference such as `QU-482917`; `""` uses the mixed prefixes (`PL`, `CL`, `JOB`…) |
| `pick` | `pick = ["draft", "sent"]`, `pick = [{ id = 1, name = "Lead" }]`, `pick = "@product"` | One item of a list (strings, numbers, true or false, or tables), or of a named list: the request's, else the file's for the request's profile |
| `int` | `int = [1, 20]` | A whole number in the range; `format = "string"` writes it as text (`"7"`) |
| `bool` | `bool = 0.8` | `true` with that chance (0 to 1), else `false` |
| `const` | `const = 4`, `const = { theme = "dark" }` | The same value every time: a string, number, true or false, list or table (a date as a string; `nan` and `inf` are refused, as JSON has no such numbers) |
| `object` | `object = true` and `[[fields.fields]]` under it | An object of its own fields, which also read the fields above it |
| `list` | `list = { count = [1, 3] }`, `list = { each = "company.contacts" }` and `[[fields.fields]]` under it | A list of objects: `count` of them about the record's person (0 to 100), or one per request person at the record's company (none for a person with no company), each about that person |
| `money` | `money = [5000, 250000]` | Dollars, held in cents |
| `date` | `date = { within_days = 60 }`, `date = { after = "issued", days = [14, 45] }` (or `months`) | A date near the generator's base date, or after a date field above |
| `text` | `text = "sentence"`, `text = { paragraph = [1, 2] }` | Prose in the profile's words: `sentence`, `title`, `label`, `subject`, or a paragraph of min plus up to extra sentences |
| `lines` | `lines = { catalogue = "product", count = [1, 6], qty = [1, 20] }` | Distinct catalogue entries, each with every entry field plus `qty` and `amount` |
| `sum` | `sum = "items.amount"`, `sum = ["subtotal", "gst"]` | The total of money fields, or of a lines field's money column |
| `percent` | `percent = { of = "subtotal", rate = 10 }` | A percentage of a money field, rounded to the cent |
| `nth` | `nth = "@product"` | Catalogue entries only: the list's item for the entry's number, so entries take the list in order and never repeat one (the catalogue may not be longer than the list) |

Money fields (`money`, `sum`, `percent`) take `format = "display"` (`"$1,234.56"`,
the default), `format = "number"` (`1234.56`) or `format = "string"`
(`"1234.56"`); sums work on cents, whatever their inputs show. A `sum` adds
up a lines field's money column (`items.amount`) or a list field's
(`deals.value`). Date fields take a Go layout in `format` (default
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

### Schemas added through the API

An admin key can add, change and remove schemas on a running server; every
key can then list them, read their files and generate from them.

```bash
exemplar schemas add ticket.toml                  # POST /api/v1/record-schemas
exemplar schemas show ticket > ticket.toml        # GET /api/v1/record-schemas/ticket
exemplar schemas update ticket.toml --if-version 1   # PUT, if no one changed it since
exemplar schemas remove ticket                    # DELETE
```

- **Checked first.** A file is checked as the server's own are, with its catalogues, before it is stored; one with a mistake is refused with the field named, and the server is unchanged.
- **Live.** A change takes effect when it is stored, with no restart. A request already running finishes with the schemas it started with.
- **Versioned.** A schema's version counts its changes from 1 (`api v2` in `schemas list`). `--if-version` (`"version"` in the API) refuses an update if someone changed the schema since, so no change is lost unseen. The same file again changes nothing. Fixtures name the hash of the file that made them (`schema=ticket@1a2b3c4d`), so a record can be traced to the exact version.
- **Kept with the data.** Schemas live in the database, so a backup holds them and a restore brings them back. Every change is in the audit log (`record_schema.create`, `.update`, `.delete`), with the version and hash.
- **Files come first.** An API schema cannot take the name of a built-in or a `--record-schemas-dir` file, and the API cannot change those. If a file later takes an API schema's name, or a newer exemplar no longer reads one, the server still starts: it warns, and lists the schema with the reason it is not loaded (`NOT LOADED:` in `schemas list`, `problem` in the API) until an admin updates or removes it; `schemas show <name> --api` reads its file back first.
- **Who reads them.** Every key can read an API schema's file, so keep in it only what any reader may see. A `--record-schemas-dir` file's text is shown to admin keys only.
- **Catalogues.** An API schema may use the server's catalogues and other API schemas' catalogues. A change to a catalogue that would break a schema using it, or removing a catalogue in use, is refused.
- **Limits.** 200 API schemas, of up to 256 KiB each.

A request that names no schemas generates every schema whose `request_lists`
it gives, so a schema that needs a target system's lists never fails
requests that are not about it. A schema the request gives any of its lists
for is generated too, so one short of a list is told which; a request that
leaves no schema at all is refused.

## Objects and lists

An `object` or `list` field's own fields go under it as `[[fields.fields]]`
(and `[[fields.fields.fields]]` a level further; objects and lists nest at
most 4 deep). They are evaluated like a record's: each reads its own fields
above it first, then the fields above the object or list, so a name inside
hides the same name outside, and a field can copy an outer one before
defining its own (`idCustomer = value "id"` above the item's own `id`).
Items of an `each` list are about their own person, so `person.*` there is
that contact. A record makes at most 100,000 values (fields, at any depth),
and one request's records come to at most 32 MiB of JSON; past either, the
request fails and says which.

Catalogue records, and catalogue entries, cannot hold objects or lists, and
catalogue entry fields cannot be optional (a hidden one is left out of the
record and of the lines that use it).

## Records for another system

Sample data for another system, through its API or by hand in its UI, is
usually one tree per customer: the account, its contacts, the deals, the
projects. One record can hold the whole tree, so whoever seeds the system
creates the account first and each child with the ID the system gave its
parent; no record has to know another's ID. The pieces:

- **A schema file of your own**, with the target's key names (`companyName`, `idUserOwner`), sent with the request (`--schema-file`, `schema_files`) or put in a server's `--record-schemas-dir`. A file sent with a request is checked like a server's, may use the server's catalogues, and may not take a name the server already has; it changes nothing on the server.
- **Lists given at generate time** (`--lists lists.json`, `lists`) for what only the target knows: its users, statuses and priorities with their IDs. A schema names the lists every request must give in `request_lists`; a request may also replace any other list. A list a request gives must be one a schema it generates picks from, so a misspelt name fails. Lists given this way do not reach catalogue entries, which depend on the schema file alone.
- **Local generation** (`exemplar generate --local`), which needs no server, key or dataset: people are made up from the seed, a few to a company as the registry makes them, with IDs from the seed too, and the records are printed as JSON. The same seed, schema files and lists give the same records.

[docs/examples/crm-account.toml](examples/crm-account.toml), with
[crm-lists.json](examples/crm-lists.json), is a worked example: an account
per company, its contacts, one to three opportunities, and for about half of
them a project with work requests.

```bash
exemplar generate --local --kind records --count 5 --people 12 --seed 42 \
  --schema-file docs/examples/crm-account.toml --lists docs/examples/crm-lists.json \
  | jq '.records[]'
```

The [exemplar-seed skill](../skills/exemplar-seed/SKILL.md) walks an agent
through it: write the schema from the target's API description, fetch the
target's lists, generate, transform, seed in order, and report.

### Which exemplar a schema needs

`exemplar = "0.5"` at the top of a file says the oldest exemplar version it
works with, like the `go` line of a `go.mod`. A release build older than
that refuses the file with a reason (`needs exemplar 0.5 or later; this is
v0.4.0`) instead of misreading it; a newer one reads it. A development build
(`dev`, or a build between releases) reads every file, since it may hold
features newer than any release. The requirement is read before anything
else in the file, so a file for a newer release names the version it needs
even when it holds keys or shapes this one does not know. Give it in every
file of your own. Releases before 0.5 know nothing of it and refuse the key
as unknown, which still stops them misreading the file.

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

Up to 500 items and 200 people per request. Records are made in full before
any is stored, and a request whose records come to over 32 MiB fails before
storing anything (`413`). Items are stored one by one; if one fails (for
example a name clash or the size limit), the ones before it stay and the
error says how many were created.

## Word pools

Names, companies, places and the insurance and manufacturing vocabulary were
ported from db-anonymizer2 and are maintained in `internal/synth/pools.go`.
