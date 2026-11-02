package canon

import (
	"context"
	"errors"
	"testing"
)

func TestAuditUTF16TransferOverflow(t *testing.T) {
	m := setupMem(t, 0)
	allocations := 0
	tc := &transferContext{caller: &transferSide{Memory: m, StringEncoding: EncUTF16}, callee: &transferSide{Memory: m, StringEncoding: EncUTF16, Realloc: func(context.Context, uint32, uint32, uint32, uint32) (uint32, error) { allocations++; return 16, nil }}}
	var caught any
	func() {
		defer func() { caught = recover() }()
		p, n := transferStringContent(context.Background(), tc, 0, 1<<31, &allocSource{})
		t.Logf("accepted ptr=%d coded_length=%d allocations=%d", p, n, allocations)
	}()
	if caught == nil {
		t.Fatal("invalid 2^31-unit UTF16 string accepted instead of rejected")
	}
	t.Logf("rejected: %v", caught)
}
func TestAuditZeroReallocPointer(t *testing.T) {
	m := setupMem(t, 0)
	var caught any
	func() {
		defer func() { caught = recover() }()
		p, e := callRealloc(context.Background(), func(context.Context, uint32, uint32, uint32, uint32) (uint32, error) { return 0, nil }, m, 0, 0, 1, 1)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("pointer=%d", p)
	}()
	if caught != nil {
		t.Fatalf("valid in-bounds address zero rejected: %v", caught)
	}
}

func TestAuditRawWriteFailureStopsCall(t *testing.T) {
	calls := 0
	want := errors.New("argument lowering failed")
	var writeErr error
	inst := &auditPoisonInstance{}
	cb := NewCallBinding(nil, nil, Callee{Instance: inst, Direct: func(context.Context, []uint64) { calls++ }})
	posts := 0
	cb.postReturn = func(context.Context, []uint64) error { posts++; return nil }
	writerCalls := 0
	write := func([]uint64) error { writerCalls++; writeErr = want; return want }
	err := cb.CallRaw(context.Background(), write, func([]uint64) error { return nil })
	t.Logf("guest_calls=%d CallRaw_error=%v captured_write_error=%v", calls, err, writeErr)
	if calls != 0 || posts != 0 {
		t.Fatalf("failed writer reached guest or post-return: calls=%d posts=%d", calls, posts)
	}
	if !errors.Is(err, want) || !errors.Is(writeErr, want) {
		t.Fatalf("write failure not propagated: call=%v callback=%v", err, writeErr)
	}
	if !errors.Is(inst.poisoned, want) {
		t.Fatalf("failed writer did not poison instance: %v", inst.poisoned)
	}
	if err := cb.CallRaw(context.Background(), write, func([]uint64) error { return nil }); !errors.Is(err, want) {
		t.Fatalf("reentry after writer failure: %v", err)
	}
	if writerCalls != 1 || calls != 0 || posts != 0 {
		t.Fatalf("poisoned reentry ran callbacks: writers=%d guests=%d post-returns=%d", writerCalls, calls, posts)
	}
}
func TestAuditCancelledLift(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := setupMem(t, 0)
	cb := NewCallBinding(nil, []Type{auditTypeList{auditTypeU8{}}}, Callee{Instance: &testInstance{}, Memory: m, StringEncoding: EncUTF8, Direct: func(_ context.Context, s []uint64) {
		m.WriteUint32Le(0, 128)
		m.WriteUint32Le(4, 1000)
		s[0] = 0
		cancel()
	}, LiftLimits: LiftLimits{MaxBytes: 100000}})
	out, err := cb.Call(ctx, nil)
	t.Logf("result_count=%d error=%v", len(out), err)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled decoding continued to completion")
	}
}

type auditTypeList struct{ elem Type }

func (t auditTypeList) Accept(v TypeVisitor) { v.VisitList(t.elem) }

type auditTypeU8 struct{}

func (auditTypeU8) Accept(v TypeVisitor) { v.VisitU8() }

func TestAuditUTF16DecodeAllocations(t *testing.T) {
	m := setupMem(t, 0)
	for i := range uint32(1000) {
		m.WriteUint16Le(i*2, 65)
	}
	var result string
	n := testing.AllocsPerRun(100, func() { result = decodeString(memShim{m}, 0, 1000, EncUTF16) })
	t.Logf("output_bytes=%d allocations_per_decode=%g", len(result), n)
	if len(result) != 1000 {
		t.Fatal("bad output length")
	}

}

type auditPoisonInstance struct {
	testInstance
	poisoned error
}

func (i *auditPoisonInstance) Poison(err error) { i.poisoned = err }

func (i *auditPoisonInstance) Enter(ctx context.Context) error {
	if i.poisoned != nil {
		return i.poisoned
	}
	return i.testInstance.Enter(ctx)
}

type auditTypeString struct{}

func (auditTypeString) Accept(v TypeVisitor) { v.VisitString() }

func TestAuditMalformedResultPoisonsWithoutPostReturn(t *testing.T) {
	m := setupMem(t, 0)
	m.WriteUint32Le(0, 128)
	m.WriteUint32Le(4, 1)
	m.WriteByte(128, 0xff)
	inst := &auditPoisonInstance{}
	posts := 0
	direct := DirectFunc(func(_ context.Context, s []uint64) { s[0] = 0 })
	cb := NewCallBinding(nil, []Type{auditTypeString{}}, Callee{Instance: inst, Memory: m, StringEncoding: EncUTF8, Direct: direct})
	cb.postReturn = func(context.Context, []uint64) error { posts++; return nil }
	_, err := cb.Call(context.Background(), nil)
	t.Logf("decode_error=%v post_returns=%d poisoned=%v", err, posts, inst.poisoned != nil)
	if err == nil || posts != 0 || inst.poisoned == nil {
		t.Fatal("malformed guest result did not stop post-return and poison the instance")
	}
}

func TestCallRawReadFailurePropagatesPoisonsAndSkipsPostReturn(t *testing.T) {
	inst := &auditPoisonInstance{}
	want := errors.New("result consumer failed")
	posts := 0
	guestCalls, readerCalls := 0, 0
	cb := NewCallBinding(nil, nil, Callee{
		Instance: inst,
		Direct:   DirectFunc(func(context.Context, []uint64) { guestCalls++ }),
	})
	cb.postReturn = func(context.Context, []uint64) error { posts++; return nil }
	read := func([]uint64) error { readerCalls++; return want }
	err := cb.CallRaw(context.Background(), func([]uint64) error { return nil }, read)
	if !errors.Is(err, want) || inst.poisoned == nil || !errors.Is(inst.poisoned, want) || posts != 0 {
		t.Fatalf("read failure lifecycle: err=%v poisoned=%v post-return=%d", err, inst.poisoned, posts)
	}
	if err := cb.CallRaw(context.Background(), func([]uint64) error { return nil }, read); !errors.Is(err, want) {
		t.Fatalf("reentry after reader failure: %v", err)
	}
	if guestCalls != 1 || readerCalls != 1 || posts != 0 {
		t.Fatalf("poisoned reentry ran callbacks: guests=%d readers=%d post-returns=%d", guestCalls, readerCalls, posts)
	}
}
