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
exemplar ingest inbox-2025 ./mail --dry-run

# 3. Ingest for real: fixtures are stored as <batch>-001.eml, <batch>-002.csv, …
exemplar ingest inbox-2025 ./mail --batch inbox-2025-01

# 4. Review each fixture: it opens, you approve, reject or skip it
exemplar review inbox-2025
#    or by hand: exemplar fixtures download <fixture-id>, then
#    exemplar approve <fixture-id>... / exemplar reject <fixture-id>...

# 5. Copy the approved fixtures into the next version of a global dataset
exemplar promote inbox-2025 --name inbox --description "Reviewed anonymised emails"

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
rewritten; its row in the result carries the error. A file that fails because
Presidio did carries only `the anonymiser's detector is unavailable; try again
later` (see [Presidio](#presidio)).

## What is replaced

| Data | Found by | Replaced with |
|------|----------|---------------|
| People in headers | Address headers | A registry person; the same real address always maps to the same person |
| Names in text | Presidio (`PERSON`), plus every name from the batch's headers; a run of words in capitals is read again title-cased, since the model reads `PETRA VANCLIFF` poorly (only the lines holding such runs, at most 200 bytes either side, go to Presidio again; a single capitalised word is never taken as a name) | Fake first and last names, case kept, each real name token mapped once; also in a mention (`@Sarah Lane`), but not after the `@` of an address |
| Email addresses | Pattern | The person's fake address, or a fake local part at a fake domain |
| Domains and companies | Addresses, URLs, `Pty Ltd`-style suffixes | A fake company; its domain is the company name on `.example`. A company name learned from a domain (`acme` from `acme.com.au`) is also replaced inside identifiers: `acme-prod`, `logo_acme.png`. Every domain in the address headers is also replaced where it appears alone, as in a DMARC subject |
| Subdomains | Domains anywhere | Generic labels (`mail`, `www`, `api`, `reply`, `mx1`…) kept; any other label (a client, product or system name) replaced with a fake, the same every time |
| URLs | Pattern | Same scheme on the fake domain; path and query replaced |
| Profile terms | The profile's `terms` list | The company-style fake (the same company as the domain it names), in any case, also inside identifiers; a term wins over a name a display name made of it |
| Phone numbers | Australian patterns, any number written with its calling code (`+234 …`), and any number after a phone label (`Tel:`, `Mobile`, `Fax`, `Ph`, `Direct:`, `Direct line`) with 6 to 15 digits, ending in two or more, so `Mobile 0412 345 678 2 units` leaves the `2` | Fake digits; leading zeros and the first other digit kept (`04…`, `(02)…`, `13…`) |
| ABN, ACN | Labelled patterns | Fake digits, same layout |
| Bank account numbers, BSBs | Their labels (`BSB: 062-000`, `Account No. 1234 5678`); a column or key labelled `BSB` or `Account Number` | Fake digits, same layout |
| Money | `$`, `AUD` patterns | Fake digits, same layout |
| Street addresses, PO boxes | Pattern | A synthetic street address |
| Message-ID, In-Reply-To, References, Content-ID | Header and `cid:` | Keyed hashes, so threads and inline images still match |
| Attachments | MIME disposition and type | A synthetic file of the same format, renamed `attachment-<hex>.<ext>`; or, with a profile's `attachments = "anonymise"`, rewritten in place when the anonymiser reads its format (see [Attachments](#attachments)) |

Images keep their size class. mail-processor treats images under 50 KB as
signature images. A replacement stays on the same side of that line: PNG and
JPEG files are padded with a metadata chunk, so they still decode.

## What is kept

- MIME structure, transfer encodings and charsets (non-UTF-8 text is converted to UTF-8 and re-encoded as quoted-printable). Multipart boundaries are the sender's text, so each is replaced with a keyed one (`=_<hex>`).
- HTML markup; only text and attribute values are replaced. Text is matched as it reads, so a name written with character references (`Jos&eacute;`, `jane&#64;…`) is found and replaced; other references stay as written.
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
| HTML | `.html`, `.htm` | Visible text, `<title>`, `<meta>` content, comments, and the `alt`, `title`, `href`, `src`, `placeholder`, `aria-label` and `value` attributes, character references decoded | Markup, scripts and styles (a value found elsewhere is still replaced in them), and references outside replaced text |
| DOCX | `.docx` | See [DOCX](#docx) | See [DOCX](#docx) |
| RTF | `.rtf` | See [RTF](#rtf) | See [RTF](#rtf) |
| XLSX | `.xlsx` | See [XLSX](#xlsx) | See [XLSX](#xlsx) |
| PDF | `.pdf` | See [PDF](#pdf) | See [PDF](#pdf) |
| Twig, Jinja | `.twig`, `.j2`, `.jinja`, `.jinja2` | Literal text (as HTML for `page.html.twig`), comments, and quoted strings inside tags | Every tag as written outside its strings: variables, filters and keywords are code, so `{{ customer.name }}` is untouched while `{% set signer = "Jane Doe" %}` is not. `{% verbatim %}` and `{% raw %}` bodies are literal text |

A CSV column or JSON key named for an entity marks its values as that entity,
even where the detector would not find them: `name`, `full_name`,
`first_name`, `last_name`, `surname`; `email`, `e-mail`, `email_address`;
`phone`, `mobile`, `tel`; `company`, `organisation`, `employer`; `bsb`,
`account_number`, `bank_account`. A longer
label ending in a phone or email one counts too: `Mobile Phone`,
`homePhone`, `Contact Phone Number`, `Work Email`. Only letters and digits
of the label count, in any case. A placeholder under such a label
(`N/A`, `TBD`, `Total`, `unknown`…) is not taken for one, nor is a value
under a phone label with fewer than 6 digits (an extension, a year): a fake
replaces its value everywhere in the file, so taking `Total` for a name
would turn every "Total" into a person. Detection still reads them.

**Columns of people.** A column whose label says nothing (`Account
Manager`, `Configurator`) can still be a column of people: the detector
finds some of the names in it and misses others. Once detection has run,
a column counts as one of people when known people are at least half of
it, by distinct values (at least two) or by cells (at least three, as when
one detected name fills most rows). Its other values that look like names,
two to five words with letters and no digits or symbols, are then taken
for people, and their fakes replace them everywhere in the file. A single
word (`Vacant`, `Admin`) and a placeholder are not. A name learned in one
column counts in the others, so the rule repeats until nothing changes. A
column is a CSV column below its header row, a sheet's column below its
label row (the header row, or else the sheet's first row with text), or
the values of one JSON key across objects. Its label is never one of its
values, so it stays.

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

### DOCX

A `.docx` is a zip of XML parts. Each part is read by a rule that knows what
it can hold, and only its text and the attributes the rule names are
rewritten, in place in the bytes: styles, numbering, layout and every part
without personal data stay exactly as they were.

| Part | What is personal there | Treatment |
|------|------------------------|-----------|
| `word/document.xml`, `header*.xml`, `footer*.xml`, `footnotes.xml`, `endnotes.xml`, `comments.xml`, `glossary/document.xml` | Text (`w:t`), deleted text (`w:delText`), text in shapes and text boxes (`a:t`), equations (`m:t`), field instructions kept or deleted (`HYPERLINK "mailto:…"`), alt text (`descr`, `title`, `alt`), a picture's name (Word keeps the inserted file's name there), link tooltips, legacy form fields' default, help and status text and list entries, content controls' list items, and the `w:author` of tracked changes and comments | Rewritten; a picture name that is a file name becomes `picture-<hex>` with its extension; `w:initials` become the fake author's |
| `word/people.xml` | Reviewers and their account ids (`S::jane.doe@…::<guid>`) | Names rewritten; addresses and hex ids in account ids faked |
| `word/settings.xml` | Document variables; the mail-merge connection string and query | Variables rewritten; mail-merge sources replaced whole |
| `docProps/core.xml`, `app.xml`, `custom.xml` | Author, last modified by, title, subject, keywords, description; company, manager, template, hyperlink base, part titles; custom property values | Rewritten; dates and counts kept |
| `word/charts/chart*.xml`, `word/diagrams/data*.xml`, `drawing*.xml` | Chart titles, series and category names; SmartArt text | Rewritten; numbers kept |
| `customXml/item*.xml` | Anything (SharePoint metadata names people) | Every text value rewritten |
| Relationships (`*.rels`) | External links, `mailto:` addresses, the attached template's path (`C:\Users\jdoe\…`) | URLs and addresses faked; file paths become `file:///anonymised/<hex>` with their extension |
| `word/media/*`, `docProps/thumbnail.*` | Photos, signatures, scans, a picture of the first page | Replaced with generated images of the same format; EMF, WMF, BMP and TIFF with a PNG, the relationship and content type following. An image not named as Word names them (`image1.png`) is renamed `image-<hex>` |
| `word/embeddings/*` | Embedded workbooks, documents, OLE objects | Replaced like email attachments, under a keyed name (`embedding-<hex>.xlsx`); one exemplar cannot generate becomes a placeholder, so the document opens but the object does not |
| Styles, numbering, fonts, theme, web settings, custom XML properties, chart styles and colours, diagram layouts | Nothing personal | Kept byte for byte |
| Anything else (macros, add-ins, ink, ActiveX, Apple's `docProps/meta.xml`…) | Unknown | Dropped and listed (`dropped_parts`), with its relationships and content type. The parts Office writes are listed by name (`word/vbaProject.bin`); any other by a keyed name (`part-<hex>.bin`), since the sender chose its name |

**Text split across runs.** Word splits text wherever formatting, a
spell-check mark or a revision changes, so "Sarah Lane" can be `Sa` + `rah La`
+ `ne`. Each run of text is joined for detection, and a fake goes into the
piece where its match starts; the matched text is taken out of the pieces
after it, which keep their formatting and the rest of their text. A tab,
break, symbol or field ends a run of text, so nothing is matched across one,
and deleted text is never joined to the text around it.

**Checks.** The output must open: every XML part well-formed, every
relationship a part uses still there and pointing at a part that is, every
part with a content type. A dropped part that the document refers to fails
this check, and the file fails rather than coming back broken. A DOCX may
hold at most 2,000 parts and expand to at most 100 times its size (at least
16 MiB) and, however well it compresses, to at most 256 MiB in total; its
XML parts may hold at most 2,097,152 tokens between them. So a zip bomb
fails, and so does a document of nothing but tiny tags. Password-protected
documents and old `.doc` files are not zip archives and are refused with a
hint. An error names a part only by the name Office gives it; it never
repeats a part name, element name or relationship id the sender chose. The output's entries
keep their order, with `[Content_Types].xml` first and fixed timestamps, so
the same file and key give the same bytes.

### XLSX

An `.xlsx` is the same kind of package as a DOCX, and is read the same way:
each part by a rule that knows what it can hold, text rewritten in place,
images and embedded files replaced, unknown parts dropped, with the same
limits, checks and error naming.

| Part | What is personal there | Treatment |
|------|------------------------|-----------|
| `xl/sharedStrings.xml` | Most cell text: Excel keeps each distinct string once and cells point to it | Rewritten, runs joined as in DOCX. Phonetic readings (`rPh`) are searched too, and dropped when their string changes, since they spell it out |
| `xl/worksheets/sheet*.xml`, `xl/chartsheets/sheet*.xml` | Inline strings and formula results, formulas, links' text and tooltips, printed headers and footers, data validation prompts and lists, filter values, scenarios' users and comments, a protected range's account | Rewritten; formulas as described below; the account replaced whole; numbers, dates and styles kept |
| `xl/workbook.xml` | Sheet names, defined names' formulas and comments, who reserved the file (`fileSharing`), where it was last saved (`C:\Users\jdoe\…`) | Sheet names rewritten as described below; the save path replaced like a DOCX's file paths |
| `xl/comments*.xml` | Comments and their authors | Rewritten |
| `xl/threadedComments/*`, `xl/persons/*` | Threaded comments, and the people who wrote them with their account ids | Rewritten; a comment's mentions are dropped when its text changes, since they give offsets into it; account ids faked as in DOCX |
| `xl/tables/*`, `xl/queryTables/*` | Column names, totals labels, filter values | Rewritten; a column name stays equal to its header cell |
| `xl/pivotCache/*`, `xl/pivotTables/*` | A full copy of the pivot's source data; field and item captions | Rewritten like the cells, so a value gets the same fake as its cell |
| `xl/externalLinks/*` | Another workbook's path, sheet names and cached values | Rewritten; the path replaced like a DOCX's linked files |
| `xl/connections.xml` | Connection strings and queries, with database paths and user names | Replaced whole, like a mail merge's sources |
| `xl/drawings/*`, `xl/charts/*`, slicers, timelines | Text boxes, shape and picture descriptions, chart titles and categories, captions | Rewritten as in DOCX |
| `xl/drawings/vmlDrawing*.vml` | The older drawings Excel keeps comment boxes, form controls and pictures in printed headers in: their text, a picture's title (often its file's name), alt text, a control's linked cell | Rewritten; a linked cell follows its sheet |
| `docProps/*`, `customXml/*`, relationships, `xl/media/*`, `xl/embeddings/*` | As in DOCX | As in DOCX |
| Styles, theme, calculation chain, cell metadata, form controls, chart styles | Nothing personal | Kept byte for byte |
| Anything else (macros, ActiveX, printer settings, add-ins, a Power Pivot data model, macro sheets, shared-workbook revisions…) | Unknown | Dropped and listed (`dropped_parts`). A sheet's link to its printer settings goes with them; a workbook that cannot do without a dropped part (a macro sheet, a data model) fails rather than coming back broken |

**Column labels.** As in a CSV, a column whose label names an entity
(`Name`, `Email`, `Phone`, `Company`…) marks the cells below it, even where
the detector would not find them. On each sheet the label row is the first
row with such a label in it, so a title above the table does no harm. A
label marks only its own block: the cells below it, down to the first blank
row or another label. An empty or missing cell inside the block is a value
not given, so a sparse `Phone` column is still read whole. A second table
further down, after a blank row, is not read as
more of the first, and a form with its labels down a column ("Name",
"Email", "Phone", each beside its value) does not mark its own labels. A
phone number typed as a number in a `Phone` column, with at least 6 digits,
has its digits faked and stays a number of the same length; the pivot
caches that copy it get the same fake. A column with no such label can still
be found to hold people; see [Columns of people](#documents).

**Formulas.** Only a formula's strings (`"Dear "&A1`) are searched and
rewritten; functions, cells and names stay as written. A sheet named inside
a string (`INDIRECT("'Sales'!A1")`) follows the sheet's new name, as a
reference outside one does. A formula's cached result is rewritten like any
other text, so it matches. A structured reference may nest at most 8 deep
(`[[#This Row],[Name]]` is two); Excel writes none deeper than three, so a
workbook holding one is a file made to be read slowly, and fails.

**Sheet names.** A sheet named for a person is renamed, and every
reference to it follows: formulas, defined names, links, data validations,
charts, pivot caches and shapes linked to a cell. The new name keeps to
Excel's rules: at most 31 characters, none of `[]:*?/\`, no apostrophe at
either end, and unique in the workbook (`Ann Lee (2)`). A reference to a
table column (`Customers[Full name]`) follows the column's header cell.

**Not covered.** Defined names and table names are identifiers and are
kept as written. A cell's number is never changed outside a `Phone`
column, so a phone number typed as a number in a column labelled otherwise
survives. Newer chart types (waterfall, histogram, funnel and the like) are
not read: they are dropped, and since the sheet cannot do without them,
the workbook fails. Workbooks with macros
(`.xlsm`), binary workbooks (`.xlsb`) and old `.xls` files are refused.

### RTF

An `.rtf` file is one stream of groups, control words and text. It is
tokenised, the text of each destination that holds text is decoded, and only
text that changes is written again; every other byte, unchanged text
included, stays as it was. A file may split into at most 2,097,152 tokens,
so a file of nothing but escapes fails instead of multiplying in memory.

- **Read as text:** the body, footnotes, headers and footers, field results
  and instructions (`HYPERLINK "mailto:…"`), comments (`\annotation`), shape
  text and the shape properties that are text (`wzDescription`,
  `gtextUNICODE`…), index entries, document variables, user property
  values, and legacy form fields' default, status and help text and list
  entries (`\*\ffdeftext`, `\*\ffstattext`, `\*\ffhelptext`, `\*\ffl`).
- **Read as people:** the `\info` group's `\author`, `\operator` and
  `\manager`, comment authors (`\atnauthor`) and the revision table's authors;
  `\company` is a company, and a comment's `\atnid` initials follow its fake
  author. `\title`, `\subject`, `\keywords`, `\doccomm` and `\category` are
  text; dates and counts are kept. The template's path (`\*\template`) and
  `\hlinkbase` are faked like DOCX link targets.
- **Not read:** the font, colour, style and list tables, the generator, and
  any other `\*` destination exemplar does not know.
- **Pictures:** a PNG or JPEG is replaced by a generated one; any other
  picture (EMF, WMF, a bitmap), embedded object data (`\*\objdata`), the
  data store (`\*\datastore`, which can hold SharePoint metadata) and form
  field data (`\*\datafield`, which holds a field's default text again) are
  dropped and listed in `dropped_parts`. An embedded object keeps its result picture,
  so the document shows it; the object itself no longer opens.
- **Escapes:** `\'xx` is decoded as Windows-1252 and `\uN` as Unicode, its
  fallback characters skipped (`\ucN`), surrogate pairs joined. A fake is
  written as plain ASCII where it can be, as `\'xx` for Windows-1252, and as
  `\uN` with the fallback count in effect otherwise. A file declaring another
  code page (`\ansicpg1250`, `\mac`, `\pc`) is refused; per-font character
  sets are not read.
- **Runs of text:** formatting groups and control words do not split a name
  (`{\b Sa}rah La\i ne` is "Sarah Lane"); a paragraph, line, tab, cell or a
  destination of its own does. A fake goes into the run where its match
  starts, as in DOCX.

The output is tokenised again and must have the same groups and control
words, apart from text, replaced pictures and dropped groups. Groups may nest
at most 2,000 deep.

### PDF

A PDF stores no words: each page is drawing instructions that place
characters, often in fonts holding only the letters the file uses. So a PDF
is rewritten in place, page by page, by PDFium, Chrome's PDF engine. It runs
inside exemplar compiled to WebAssembly (go-pdfium, wazero): no other
service, and a malformed file can at worst stop its own engine. Each file
gets one at a time, which may use at most 512 MiB, and the file, however
many engines it takes, is stopped after two minutes.

1. The file is opened (one that needs a password is refused) and every page
   is flattened: annotations and form fields become page content, so their
   visible text is read and rewritten as the page's is. Links are read
   first, since flattening removes them, and added back at the end.
2. Each page's text is read with the box of every character, along with
   every text and image object and its bounds, inside forms too. A
   character belongs to the smallest text object holding its centre.
3. Detection reads the pages' text, marked-content strings and every string
   stored outside the drawing (see below), as one document.
4. A text object holding a match is removed and drawn again with the fakes
   in it: same place, size, colour and render mode, in the standard font
   nearest its own (Helvetica, Times or Courier, bold and italic as the
   original). A name split across objects (`Sarah` in one, `Lane` in the
   next) gets its fake in the first, which may take the width the whole
   match took; what follows the match starts where the match ended. A fake
   longer than that room is squeezed into it. Only a page with something to
   change is written again (or one with an image inline in its content).
5. pdfcpu rewrites the strings outside the drawing, replaces every image and
   drops what no rule knows. The file is then opened again and must hold
   exactly the text that was written. PDFium writes the text of some
   embedded fonts back wrong when it writes a page again (a symbolic subset
   whose codes it re-derives): an object kept whose text comes back
   different is drawn again in a standard font too, and the file written
   once more. Text that was never written, or new text missing, fails it.

| Where | What is personal there | Treatment |
|-------|------------------------|-----------|
| Page text | Names, addresses, numbers | Each text object with a match drawn again as above; the rest kept, in its own font |
| Text in forms (reusable content) | Letterheads, stamps, flattened fields | As page text. The new text is drawn on the page just after its form; where the form sits inside another (a flattened field), what the inner form drew is moved onto the page with it, as PDFium writes no change deeper |
| Annotations, form fields | Comments, filled fields, signatures' appearance | Flattened: what they show is page text; what they hide (a comment's note, a field's value, its author) goes |
| Links | Web and `mailto:` addresses | Rewritten; links to pages kept; links to files, programs or scripts dropped |
| Marked content | `ActualText`, `Alt`: what a screen reader says | Rewritten. PDFium writes a form's content only when an object leaves it, so an object in a form whose marked content changes is moved onto the page, as text in a nested form is |
| Images | Photos, signatures, logos | Every image replaced with a grey pixel, drawn or not: page thumbnails, masks and patterns included |
| Strings outside the drawing | Title, author, subject and keywords; bookmarks; named destinations; layer names | Rewritten (the author is a person); strings a reader looks things up by (a font's registry and ordering, a language tag, a font's glyph list, a file identifier) are left alone |
| JavaScript, embedded files, form-field data, the structure tree, XMP metadata, page thumbnails, applications' private data, digital signatures, portfolios, web captures, annotations flattening left | Unknown or duplicated | Dropped and listed (`dropped_parts`) |

The same file anonymised with the same key comes out the same: the file
identifier PDFium would make at random is a hash of the content. An
encrypted PDF that opens without a password comes back unencrypted.

**Refused.** A password-protected PDF; a damaged one; one of more than 2000
pages; a scan: a page whose images cover half of it, with fewer than 50
characters of visible text, or with any invisible text (the layer OCR adds
over a scanned image, which rewriting would leave the image under); and a
page whose text objects, bucketed for finding which object draws a
character, cover more than 65536 grid cells on a page or 4194304 in the
document, or a form whose content streams decode to more than 64 MiB, or
pages whose content streams decode to more than 256 MiB together: however
well a file compresses, it must not multiply into many times its size in
the server's memory. A changed text object holding a character the
standard fonts cannot show (Chinese, Arabic, Cyrillic) fails its page: the
fakes cannot be drawn in its own font.

**Not covered.** Text drawn as outlines, or inside an image, cannot be read.
A form holding no match keeps its content as written, an image inline in
it included. A rewritten line is in
a standard font, a longer fake is squeezed and a shorter one leaves a gap;
the tags screen readers use are dropped with the structure tree.

## Anonymise without ingesting

`exemplar anonymise` anonymises files and gives them back, storing nothing:
no dataset, fixture, person or blob. It reads every format ingest reads,
with the same detection, and is meant for quick use and workflows.

```bash
# One file in, the same format out, on stdout (any API key)
exemplar anonymise notes.md > anon.md
cat export.json | exemplar anonymise - --format json > anon.json

# Several files, as one batch sharing fakes, into a directory
exemplar anonymise ./mail ./notes --out-dir ./anonymised

# On your own machine: no server, no API key
exemplar anonymise notes.md --local --presidio-url http://localhost:3000
exemplar anonymise notes.md --local --patterns-only
```

It runs either way:

| | Server | `--local` |
|-|--------|-----------|
| Who | Any key with `read` | Anyone who can run the binary; no API key |
| Detector | The server's Presidio | `--presidio-url`, or `--patterns-only` |
| Key | A random key per request, never stored | `--anon-key-file` (or `EXEMPLAR_ANON_KEY`) if given, otherwise a random key per run |
| Registry | In memory for the request, unless an admin links (below) | In memory |
| Profiles | The server's | Built-in, plus `--anon-profiles-dir` |
| Limits | `storage.max_ingest_mb` and 1000 files per run; one run at a time per key, and at most `storage.max_concurrent_runs` runs at once across all keys, shared with ingest (`429` otherwise, before the upload is read) | 1000 files |
| Audit | One `file.anonymise` event, counts only | None |

**Unlinked fakes.** If the tool used the server's key and registry, anyone
could send "Jane Doe" and check whether the fake matches a person in the
library, which would re-identify anonymised fixtures. So each request gets a
key that is thrown away afterwards: fakes are consistent within one run (the
same person keeps one fake across its files) and match nothing else, not the
library and not another run. An admin can ask for **linked** fakes,
`--link-registry`: the server's key and registry, with new people saved, as
ingest makes them. Locally, a key file gives the same fakes across runs; only
someone holding it gets fakes tied to it.

**Output.** With one file, the anonymised file goes to stdout, or to
`--output` (never overwritten). Several files need `--out-dir`, which must be
empty or new; each file is written under its fake name
(`document-3f9a1c.md`), readable by its owner only. Stdout carries only the
file. A summary goes to stderr, one line per file with its original name
(taken from the command line, never from the server), its fake name and the
replacement count; `--quiet` drops it. Every run ends with a warning on
stderr: the output is anonymised but not reviewed. Check it before sharing:
unlike ingest, nothing here goes through review.

**In a pipeline.** Since stdout carries only the file, the tool sits between
two commands like any filter:

```bash
cat export.json | exemplar anonymise - --format json | jq '.customer'
exemplar anonymise notes.md --quiet | pandoc -f markdown -o notes.pdf
exemplar anonymise notes.md 2>/dev/null | wc -w   # no summary or warning
```

A file that cannot be anonymised writes nothing to stdout, and the command
exits non-zero; with `set -o pipefail` the pipeline fails instead of handing
the next command an empty input. Nothing is written to stdout either until
the whole file is anonymised, so a failure never leaves half a file behind.
Several files cannot share one stream, so they need `--out-dir`; there a
failed file is left out, the others are written, and the command exits
non-zero.

**Check.** Detection misses things: a company with no `Pty Ltd`, a name in
a language the model reads poorly, a reference number with no label.
`--check` lists on stderr what each output holds exactly as its input did:
email and web addresses, numbers of six digits or more (dates aside),
names (two or more capitalised words) and capitalised words. It is the
place to start reviewing. Each file is read as the anonymiser reads its
format, both sides, on the machine running the command: the check works
with a server too, and nothing original is sent anywhere for it.

```
Unchanged: in the output exactly as in the original. Check each before sharing.
1 notes.md -> document-3f9a1c.md:
  number  7700012345
  name    Lumen Fabrication ×2
  words   Acme ×4, Invoice, Thanks
```

`--check-json FILE` writes the same as JSON, for a pipeline to act on (fail
a build on a kept email address, say); `-` is stdout when the files go to
`--output` or `--out-dir`. Like `--output`, it never overwrites; a file
that exists, a directory that does not, or the `--output` file fails the
command before anything is anonymised:

```json
{"files": [{"input": "notes.md", "output": "document-3f9a1c.md",
  "unchanged": [{"kind": "name", "value": "Lumen Fabrication", "count": 2}]}]}
```

`kind` is `email`, `url`, `number`, `name` or `word`; `count` is how often
the output holds the value; a file that failed, or could not be read back,
has `error` instead. The list holds only values the output holds, so it
shows nothing the output does not; what it adds is that each is real.
Words are many and mostly harmless (`Invoice`); names, numbers and
addresses are what to look at.

```bash
exemplar anonymise invoices/ --out-dir anon --check-json - --quiet \
  | jq -e '[.files[].unchanged[] | select(.kind != "word")] | length == 0'
```

**Report.** `--report` prints every replacement, original next to fake, to
stderr. It works with `--local` only: the server never sends original data
back. For a report from the server, use `ingest --dry-run`.

**Without Presidio.** `--patterns-only` still finds addresses, URLs, phone
numbers, ABNs, money and street addresses, names from email headers, and
values under name-like CSV columns or JSON keys; those names are then found
anywhere as whole words, in any case and any script (`José`, `Zoë`), the
longest first. It misses names in prose that nothing else names, and says
so in its help.

Over HTTP the tool is `POST /api/v1/anonymise` (see [api.md](api.md)); it is
not an MCP tool, since real data would pass through an agent before it is
anonymised.

**From an AI agent.** An agent that runs shell commands can use the CLI, and
[`skills/exemplar-anonymise/SKILL.md`](../skills/exemplar-anonymise/SKILL.md)
tells it how to do so safely: pass the files to exemplar by path or pipe and
never read the originals, read only the output, never use `--report` or
`ingest --dry-run`, keep the output local until a person has reviewed it,
and take the API key from the environment. It is a skill for Claude Code
(and other agents that read `SKILL.md`); install it by copying or linking
the folder into `~/.claude/skills/` for yourself, or into a project's
`.claude/skills/`:

```bash
ln -s "$PWD/skills/exemplar-anonymise" ~/.claude/skills/exemplar-anonymise
```

The skill describes the CLI's flags and output, so a change to either
updates it too.

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
  -p 127.0.0.1:3000:3000 mcr.microsoft.com/presidio-analyzer:2.2.362
```

- `GUNICORN_CMD_ARGS="--timeout 300"` is needed: the first request loads the model, which takes longer than Gunicorn's default 30-second worker timeout, so the worker is killed and the request hangs.
- The image is about 1 GB.
- Default Presidio does not tag organisations, ABNs, Australian mobile numbers or many other countries' numbers. exemplar's own patterns cover those (other countries' numbers when written with their calling code), and companies are also learned from addresses.
- To tune recognisers, mount a configuration file and point `ANALYZER_CONF_FILE` at it (see the Presidio documentation).

exemplar sends each text part with the batch's header names as an ad-hoc deny
list, so Presidio tags those names even where its model would not. Presidio
reports offsets in characters; the client converts them to byte offsets.
A name Presidio runs into the number beside it ("Sarah Lane 0412" out of
"Sarah Lane 0412 345 678") loses its words with no letter, so the phone
number is still found and replaced whole.

**What it misses.** Presidio's model is trained mostly on English text, so
in free text it misses many names from other languages, and names in
capitals unless title-casing them helps (`SARAH LANE` is read again as
`Sarah Lane`); a column of them is caught by the column rule
(see [Columns of people](#documents)), a sentence is not. Its phone
recogniser knows only some countries' formats. exemplar's patterns add
Australia's, any number written with its calling code (`+234 801 234
5678`, 8 to 15 digits), and any number after a phone label (`Tel: 0801 234
5678`); one written the way another country writes it, with neither, can be
left as written, since it looks like any other number. `--check` lists the
long numbers left. A column labelled `Phone` or
`Name` is safe either way; a free-text column holding contact details needs
checking by hand.

**When Presidio fails.** A file fails if Presidio cannot be reached, answers
with an error status, or sends something that is not its JSON. The error
behind it names Presidio's URL, host and resolver (`dial tcp: lookup presidio
on 127.0.0.11:53: no such host`), so the server never passes it on: ingest and
the anonymise tool report only `the anonymiser's detector is unavailable; try
again later` for that file, and a one-file anonymise request gets `503`.
The server logs what failed, once per file: `WARN anonymise: the detector
failed` (or `ingest:`) with the status Presidio returned, the network error
or the timeout. It never logs the file, its name, or what Presidio said
back, which could quote the text it was sent. Check Presidio itself too
(`docker logs presidio`, or `curl` its `/health` from the server's host).
`exemplar anonymise --local` shows the whole error, since the person
running it owns the setup.

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
| `attachments` | `replace` (the default) or `anonymise`; see [Attachments](#attachments) |

A profile file is complete on its own: fields it leaves out are empty, not
inherited. Start from a copy of
[`internal/anonymise/profiles/default.toml`](../internal/anonymise/profiles/default.toml).
A file in the profiles directory with the name of a built-in profile replaces
it.

### Attachments

By default every attachment is replaced by a generated file of the same
type, so nothing of the original survives. A profile with
`attachments = "anonymise"` rewrites instead the attachments the anonymiser
reads, in place, through the same handler a document of that format goes
through: text, Markdown, CSV, TSV, JSON, JSON Lines, HTML, DOCX, RTF, XLSX and PDF. The
format comes from the attachment's file name, or its media type when the
name gives none (`text/csv`, `application/json`…); text is converted from
its charset to UTF-8 first. Everything else (images, `.xls` and other
spreadsheets, an `.eml` sent as a file) is still replaced, and so is an attachment its
handler cannot read: a broken file is never passed through.

A rewritten attachment is read with the email's people already known, and
its own names are learned while the batch is read, so a person named only
in an attachment is replaced in the email's body too. Its replacements
appear in the email's dry-run report, and what its handler dropped (a DOCX's
macros) is listed in `dropped_parts` as `<attachment>: <part>`, with
`attachment (<format>)` for one without a name. Results
count replaced attachments in `attachments` and rewritten ones in
`attachments_rewritten`. A rewritten attachment keeps its content, so review
it as you review the email.

## Dry run

`--dry-run` anonymises the files and returns, per file, every replacement as
`entity`, `original`, `fake`. Nothing is stored and no registry people or name
tokens are created; the audit log records that a dry run happened, without the
report. The response carries `Cache-Control: no-store`, and the CLI warns on
stderr that it contains original data.

Fakes are drawn from the key, so a dry run normally shows the fakes a real
ingest with the same key will produce.

## Limits

- Emails and the document formats above are read; the extension picks the format, case aside. Any other file (images, `.doc`, `.dotx`, `.docm`, `.xls`, `.xlsm`, `.xlsb`) fails on its own, with an error naming the supported extensions. `.msg` (Outlook) files are rejected with a hint to convert them to `.eml` first. exemplar serves uploaded `.msg` fixtures as they are but cannot anonymise them yet.
- In JSON, keys and numbers are never changed: a phone number stored as a number, or a name used as a key, survives. In templates, nothing is matched across a tag.
- Charsets other than UTF-8, US-ASCII, ISO-8859-1 and Windows-1252 are rejected per file.
- Attachment contents are replaced, not anonymised, unless the profile rewrites attachments in a format the anonymiser reads.
- Dates are kept, so a timeline can be recognised by someone who knows it.
- Detection is not perfect. Names Presidio misses and that never appear in a header, or identifiers with no pattern (customer numbers, for example), can survive. This is why every fixture is reviewed before it leaves quarantine.
- One request is capped at `storage.max_ingest_mb` (default 200 MiB) and 1000 files. At most `storage.max_concurrent_runs` ingest and anonymise runs (default 2) are held at once; the rest get `429` before their upload is read.
