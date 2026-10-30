# Third-party notices

exemplar's binary includes the following software. Each keeps its own
licence; see each project for the full text. Go modules not listed here are
under the licences in their source, as `go mod download` fetches it.

## PDF support

PDFs are read and written by PDFium compiled to WebAssembly, which the
binary embeds, run by wazero.

- **go-pdfium** (github.com/klippa-app/go-pdfium): MIT License, Copyright
  (c) 2022 Klippa App BV. It embeds the WebAssembly build of PDFium below.
- **PDFium** (pdfium.googlesource.com/pdfium): BSD 3-Clause License and
  Apache License 2.0, Copyright 2014 The PDFium Authors. PDFium is built
  with these libraries:
  - **FreeType**: FreeType License. Portions of this software are copyright
    © The FreeType Project (www.freetype.org). All rights reserved.
  - **libjpeg-turbo**: IJG License, BSD 3-Clause License and zlib License.
    This software is based in part on the work of the Independent JPEG
    Group.
  - **OpenJPEG**: BSD 2-Clause License.
  - **Little CMS**: MIT License.
  - **libpng**: PNG Reference Library License.
  - **zlib**: zlib License.
  - **ICU**: Unicode License v3.
  - **Abseil**: Apache License 2.0.
  - **Anti-Grain Geometry**: the AGG 2.3 licence (a permissive licence).
- **wazero** (github.com/tetratelabs/wazero): Apache License 2.0.
- **pdfcpu** (github.com/pdfcpu/pdfcpu): Apache License 2.0, Copyright The
  pdfcpu Authors.
