---
name: exemplar-anonymise
description: Anonymise real files (emails, text, Markdown, CSV, TSV, JSON, HTML, Twig and Jinja templates, DOCX, RTF, XLSX) with the exemplar CLI, without reading the originals. Use when asked to anonymise, scrub, de-identify or redact personal data in files, to make real data safe to share or use as test data, or to put exemplar anonymise in a script or pipeline.
---

# Anonymising files with exemplar

`exemplar anonymise` takes real files and gives back anonymised files in the
same format, storing nothing. Names, addresses, phone numbers, URLs and
company names become consistent fakes; structure stays as it was. Your job
is to run it on the user's files without the original data passing through
you.

## Rules

1. **Never read an original file.** Do not open, `cat`, `head`, `grep`,
   summarise or quote a file before it is anonymised, and do not ask the
   user to paste one. Pass it to exemplar by path, or pipe it in
   (`cat in.json | exemplar anonymise - --format json`): the pipe carries
   the data, not you. File names can be personal too; refer to files by the
   path the user gave, and don't invent descriptions of their contents.
2. **Read only the output.** What exemplar writes to stdout, `--output` or
   `--out-dir` is anonymised; that is what you may read, check and work
   with.
3. **Never use `--report` or `ingest --dry-run`.** Both print original data
   next to the fakes. If the user wants a report, tell them to run the
   command in their own terminal; do not run it, and do not read its output
   if they paste it.
4. **The output is not reviewed.** Every run ends with a warning on stderr.
   Do not commit it, upload it, send it to another service or paste it
   elsewhere until the user has checked it. Say so when you hand it over.
   You may help that review: read the output and point out anything that
   still looks real (a name in prose, a customer number), since detection is
   not perfect.
5. **Keys stay in the environment.** The API key comes from
   `EXEMPLAR_API_KEY` and the server from `EXEMPLAR_SERVER` (default
   `http://localhost:8080`). Never put a key on the command line, never ask
   for one in chat, and never print one. If the key is missing, ask the user
   to set it in their shell.
6. **Write output somewhere safe.** Use a path outside the repository or one
   git ignores. exemplar never overwrites: `--output` must not exist, and
   `--out-dir` must be empty or new.

## Commands

```bash
# One file to stdout, in its own format
exemplar anonymise notes.md

# One file to a new file (mode 600)
exemplar anonymise contacts.csv --output /tmp/anon/contacts.csv

# stdin needs its format (or a file name that gives it)
cat export.json | exemplar anonymise - --format json
cat page | exemplar anonymise - --name page.html.twig

# Several files or a directory: one batch, so a person keeps one fake
# across all of them; written under fake names
exemplar anonymise mail/ notes.md --out-dir /tmp/anon/batch1

# In a pipeline: stdout carries only the file
exemplar anonymise notes.md --quiet | pandoc -f markdown -o notes.pdf
set -o pipefail; exemplar anonymise data.jsonl 2>/dev/null | jq -c '.name'
```

Formats are picked by extension: `.eml`, `.txt`/`.text`/`.log`,
`.md`/`.markdown`, `.csv`, `.tsv`, `.json`, `.jsonl`/`.ndjson`,
`.html`/`.htm`, `.twig`, `.j2`/`.jinja`/`.jinja2`, `.docx`, `.rtf`, `.xlsx`. For
stdin, `--format` takes the same names without the dot (`md`, `csv`, `json`,
`eml`, `docx`, `xlsx`…). PDF, `.xls`, `.xlsm`, `.doc` and `.msg` are not supported; say so
rather than converting them yourself, since converting means reading them.

## Choosing where it runs

| Situation | Flags | Notes |
|-----------|-------|-------|
| A server is running (default) | none | Any API key that can read. Fakes come from a key made for the run, so they differ from run to run |
| No server, Presidio available | `--local --presidio-url http://localhost:3000` | Runs in the CLI; needs no API key |
| No server, no Presidio | `--local --patterns-only` | Finds addresses, phones, URLs and names from headers or name columns, but **misses names in prose**: tell the user, and check the output with extra care |
| The same fakes every run | `--local --anon-key-file KEY` (or `EXEMPLAR_ANON_KEY`) | The key file is the user's; don't read it |
| Fakes matching the server's fixture library | `--link-registry` | Admin key only; saves new people in the registry, so only when the user asks |

`--profile NAME` picks an anonymisation profile (default `default`); a profile
can, for example, rewrite email attachments in place instead of replacing
them.

## Reading the result

- **stdout** is the anonymised file, and nothing else.
- **stderr** has one summary line per file (`1 notes.md -> document-3f9a1c.md,
  4 replacements`, plus attachments rewritten and parts dropped);
  `--quiet` removes it. Then the unreviewed warning, and last, when a file
  failed, the error (`exemplar: 1 of 3 files failed`).
- **Exit status** is non-zero when any file failed. A failed file writes
  nothing to stdout; with `--out-dir` the other files are still written.
- **Dropped parts** (a DOCX's or XLSX's macros, an RTF's embedded objects) are listed
  in the summary. They are removed, not anonymised; mention them to the
  user.
- Errors name the problem (`not UTF-8 text`, `cannot anonymise .pdf`,
  `this key already has an anonymise run in progress`). Runs are one at a
  time per key: wait for one to finish before starting the next, rather
  than running several in parallel.
- `the anonymiser's detector is unavailable; try again later` means the
  server's Presidio is down. Retry later or tell the user; do not switch to
  `--local --patterns-only` without asking, since it misses names in prose.

## What not to do

- Don't work around a refusal (an unsupported format, a charset that is
  refused, a password-protected DOCX) by decoding or converting the file
  yourself.
- Don't anonymise by hand, with `sed` or your own code: exemplar keeps fakes
  consistent across files and formats, and checks the output still parses.
- Don't use `exemplar ingest` unless the user asks to store the result as
  fixtures; it needs an admin key and a quarantine dataset.

More: `docs/anonymisation.md` ("Anonymise without ingesting") and
`docs/cli.md` in the exemplar repository.
