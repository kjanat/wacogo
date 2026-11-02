package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/kjanat/wacogo/internal/canon"
)

type rawCallPhase uint8

const (
	rawWriting rawCallPhase = iota
	rawExecuting
	rawReading
	rawFinished
)

// rawCallResources owns only transfers performed through this call's contexts.
// Argument ownership commits when write succeeds; result ownership commits only
// when the entire call succeeds. Failed calls consume and destroy these owns;
// they do not restore caller wrappers that were already invalidated.
type rawCallResources struct {
	task      *canon.Task
	caller    *ComponentInstance
	phase     rawCallPhase
	arguments []*liveResourceHandle
	results   []*liveResourceHandle
	borrows   []*liveResourceHandle
}

type rawTransferTarget struct {
	table  *ResourceTable
	call   *rawCallResources
	caller bool
}

func (*rawTransferTarget) transferTarget() {}

func (t *rawTransferTarget) IssueOwn(tr *TypeResource, rep uint32) ResourceHandle {
	if t.call.phase == rawFinished {
		panic("wacogo/core: transfer target used after call")
	}
	h := t.table.issueOwn(tr, rep)
	switch {
	case !t.caller && t.call.phase == rawWriting:
		t.call.arguments = append(t.call.arguments, h)
	case t.caller && t.call.phase == rawReading:
		t.call.results = append(t.call.results, h)
	}
	return h
}

// LendTo needs the actual table for the defining-instance shortcut, but keeps
// the call tracker so aborted writes cannot strand a borrow in that table.
func rawTargetTable(target TransferTarget) (*ResourceTable, *rawCallResources) {
	switch t := target.(type) {
	case *ResourceTable:
		return t, nil
	case *rawTransferTarget:
		if t.call.phase == rawFinished {
			return nil, nil
		}
		return t.table, t.call
	default:
		return nil, nil
	}
}

// finishTask runs while the callee is still entered. A successful guest call
// must have consumed all borrows; record that error before removing leftovers.
// An aborted write never exposed its borrows to the guest, so remove them first.
func (r *rawCallResources) finishTask() error {
	if r.phase == rawWriting {
		r.dropBorrows()
	}
	err := r.task.End()
	r.dropBorrows()
	return err
}

func (r *rawCallResources) dropBorrows() {
	for _, h := range slices.Backward(r.borrows) {
		if !h.isValid() {
			continue
		}
		e := &h.table.entries[h.slotIdx]
		if e.borrowScope != nil {
			e.borrowScope.BorrowDropped()
		}
		h.table.free(h.slotIdx)
		h.valid = false
	}
	r.borrows = nil
}

// finishOwns is deferred outside CallBinding, after its Exit and trap recovery.
// Detach lists before destructors: a destructor can reenter a different binding
// or mutate tables, and every recorded handle must still match its generation.
func (r *rawCallResources) finishOwns(ctx context.Context, result *error) {
	panicked := recover()
	phase := r.phase
	r.phase = rawFinished
	arguments, results := r.arguments, r.results
	r.arguments, r.results = nil, nil
	var cleanupErr error
	if phase == rawWriting {
		cleanupErr = r.dropOwns(ctx, arguments)
	}
	if *result != nil || panicked != nil || cleanupErr != nil {
		cleanupErr = errors.Join(cleanupErr, r.dropOwns(ctx, results))
	}
	*result = errors.Join(*result, cleanupErr)
	if panicked != nil {
		if cleanupErr == nil {
			panic(panicked)
		}
		if cause, ok := panicked.(error); ok {
			panic(errors.Join(cause, *result))
		}
		panic(errors.Join(fmt.Errorf("component call panic: %v", panicked), *result))
	}
}

func (r *rawCallResources) dropOwns(ctx context.Context, handles []*liveResourceHandle) error {
	var errs []error
	for _, h := range slices.Backward(handles) {
		if !h.isValid() {
			continue
		}
		// Remove the entry before invoking user code. The original caller may
		// still be entered by its enclosing host call, which permits its own
		// destructor shortcut; a poisoned definer never permits that shortcut.
		e := &h.table.entries[h.slotIdx]
		if e.numLends != 0 {
			errs = append(errs, fmt.Errorf("raw call cleanup: handle %d has %d outstanding borrows", h.HandleID(), e.numLends))
			continue
		}
		tr, rep := e.tr, e.rep
		h.table.free(h.slotIdx)
		h.valid = false
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					if cause, ok := p.(error); ok {
						err = fmt.Errorf("raw call cleanup destructor: %w", cause)
					} else {
						err = fmt.Errorf("raw call cleanup destructor: %v", p)
					}
				}
			}()
			return runDtor(ctx, tr, rep, r.caller)
		}()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
