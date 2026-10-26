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
exemplar generate <dataset-id> --kind emails --count 24          # every shape, twice
exemplar generate <dataset-id> --kind files --formats pdf,xlsx --count 6
exemplar generate <dataset-id> --kind records --count 10 --people 4
exemplar fixtures list <dataset-id> --person <person-id>           # everything about one person
```

The same request over the API is `POST /api/v1/datasets/{id}/generate` with a
JSON body (see [api.md](api.md)).

`task sample-data` fills a server with a ready-made set: workspaces
`sample-emails` (every shape), `sample-documents` (every format) and
`sample-records` (every schema), all about the same 20 people, from one
seed. It uses `EXEMPLAR_SERVER` (default `http://localhost:8080`) and
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
| `schemas` | all | Records: `contact`, `company`, `project`, `opportunity`, `address` |
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
exemplar generate <dataset-id> --kind emails --count 20 --from <fixture-id>,<fixture-id>
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

Records are JSON fixtures (kind `record`) with a `record_schema`. The
request cycles through its schemas; each schema starts at a different person,
spaced evenly through the request's people, and moves on one person per
record. So `--count 30 --people 20` gives 6 of each schema, the contacts
about 6 different people and the five schemas between them about all 20,
with no two records alike. A schema repeats a person only once it has used
every one. Company records go through the distinct companies instead.

| Schema | Fields | Links |
|--------|--------|-------|
| `contact` | person_id, names, email, phone, job_title, company, address | `contact` |
| `company` | name, abn, phone, website, industry, address, contacts (person IDs) | every contact as `contact` |
| `project` | name, code, company, manager_person_id, status, start/end dates, budget, description | `manager` |
| `opportunity` | name, company, contact_person_id, stage, value, source, expected_close, notes | `contact` |
| `address` | person_id, street, suburb, state, postcode, country | `resident` |

Uploaded JSON can be a record too: pass `record_schema` (any lower-case name)
with the upload.

## Reproducibility

The seed drives two separate streams: one picks people, the other writes the
content. The same seed with the same people therefore gives byte-identical
fixtures, whatever else is in the registry. Fixture names start with
`g` and six hex digits of the seed (`g00007b-001-…`), so runs with different
seeds never clash in one dataset, and rerunning a seed into the same dataset
fails with a name conflict instead of duplicating it.

Fixture provenance records `source: synthetic` and
`generator: exemplar-synth/1 seed=<seed>` (`template:<fixture-id> seed=<seed>`
for emails from templates). The same seed, people and templates give the same
emails.

## Into a mailbox

`exemplar push-mailpit <dataset-id>` sends the generated emails to Mailpit,
tagged with a run ID; see [cli.md](cli.md).

## Limits and failures

Up to 500 items and 200 people per request. Items are stored one by one; if one
fails (for example a name clash or the size limit), the ones before it stay and
the error says how many were created.

## Word pools

Names, companies, places and the insurance and manufacturing vocabulary were
ported from db-anonymizer2 and are maintained in `internal/synth/pools.go`.
