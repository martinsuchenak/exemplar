package anonymise

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// PDFs are read and written by PDFium, Chrome's PDF engine, compiled to
// WebAssembly and run inside the process by wazero (go-pdfium's
// webassembly package): no CGO, no other service. A PDF parser is where a
// malformed file does harm, so each file gets its own engine, which can
// use at most pdfMemory and is stopped after pdfTimeout; a crash or a
// runaway file ends that engine, not exemplar.

const (
	pdfTimeout = 2 * time.Minute
	// pdfMemory is the most memory one engine may use, in WebAssembly pages
	// of 64 KiB: 512 MiB.
	pdfMemory = 8192
)

// pdfiumCache keeps PDFium compiled, so an engine after the first starts in
// tens of milliseconds rather than more than a second.
var pdfiumCache = sync.OnceValue(wazero.NewCompilationCache)

// pdfSession is one PDFium engine, for one file. close may be called more
// than once.
type pdfSession struct {
	p     pdfium.Pdfium
	close func()
	ctx   context.Context
}

// openPDFium starts an engine that stops when ctx ends or pdfTimeout
// passes, whichever is first.
func openPDFium(ctx context.Context) (*pdfSession, error) {
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	rc := wazero.NewRuntimeConfig().
		// The features PDFium is built with, as go-pdfium sets them.
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithCompilationCache(pdfiumCache()).
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(pdfMemory)
	pool, err := webassembly.Init(webassembly.Config{Context: ctx, MinIdle: 1, MaxIdle: 1, MaxTotal: 1, RuntimeConfig: rc})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("starting the PDF engine: %w", err)
	}
	inst, err := pool.GetInstanceWithContext(ctx)
	if err != nil {
		_ = pool.Close()
		cancel()
		return nil, fmt.Errorf("starting the PDF engine: %w", err)
	}
	return &pdfSession{p: inst, ctx: ctx, close: sync.OnceFunc(func() {
		_ = inst.Close()
		_ = pool.Close()
		cancel()
	})}, nil
}

// fail turns an engine error into the file's error: a stopped engine says
// why it stopped.
func (s *pdfSession) fail(err error) error {
	switch {
	case errors.Is(s.ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("the PDF took longer than %v to read", pdfTimeout)
	case s.ctx.Err() != nil:
		return s.ctx.Err()
	}
	return err
}

// removeFromForm removes obj from the form object form. go-pdfium passes
// the two to PDFium's FPDFFormObj_RemoveObject in the wrong order (the
// object, then the form), so they are given to it swapped;
// TestPDFiumRemovesFromForms fails if go-pdfium fixes that.
func (s *pdfSession) removeFromForm(form, obj references.FPDF_PAGEOBJECT) error {
	_, err := s.p.FPDFFormObj_RemoveObject(&requests.FPDFFormObj_RemoveObject{FormObject: obj, PageObject: form})
	return err
}
