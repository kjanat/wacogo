package canon

import (
	"context"
	"testing"
)

func requireArithmeticTrap(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if _, ok := recover().(*Trap); !ok {
			t.Fatal("expected *Trap panic")
		}
	}()
	fn()
}

func TestUTF16TransferLengthDoesNotWrap(t *testing.T) {
	const codedLen = uint32(1 << 31)
	if got := stringByteLen(codedLen, EncUTF16); got != uint64(codedLen)*2 {
		t.Fatalf("UTF-16 byte length = %d, want %d", got, uint64(codedLen)*2)
	}

	mem := setupMem(t, 0)
	allocations := 0
	tc := &transferContext{
		caller: &transferSide{Memory: mem, StringEncoding: EncUTF16},
		callee: &transferSide{Memory: mem, StringEncoding: EncUTF16, Realloc: func(context.Context, uint32, uint32, uint32, uint32) (uint32, error) {
			allocations++
			return 16, nil
		}},
	}
	requireArithmeticTrap(t, func() {
		transferStringContent(context.Background(), tc, 0, codedLen, &allocSource{})
	})
	if allocations != 0 {
		t.Fatalf("realloc called %d times for invalid UTF-16 length", allocations)
	}
}

func TestUTF16SizeWalkRejectsOverflowedLength(t *testing.T) {
	tc := &transferContext{caller: &transferSide{StringEncoding: EncUTF16}, callee: &transferSide{StringEncoding: EncUTF16}}
	requireArithmeticTrap(t, func() {
		stringContentSizeStep(tc, 1<<31, &allocSink{})
	})
}

func TestCallReallocAcceptsInBoundsAddressZero(t *testing.T) {
	mem := setupMem(t, 0)
	ptr, err := callRealloc(context.Background(), func(context.Context, uint32, uint32, uint32, uint32) (uint32, error) {
		return 0, nil
	}, mem, 0, 0, 1, 1)
	if err != nil || ptr != 0 {
		t.Fatalf("callRealloc = (%d, %v), want (0, nil)", ptr, err)
	}
}

func TestCallReallocRejectsOutOfBoundsAddressZero(t *testing.T) {
	mem := setupMem(t, 0)
	requireArithmeticTrap(t, func() {
		_, _ = callRealloc(context.Background(), func(context.Context, uint32, uint32, uint32, uint32) (uint32, error) {
			return 0, nil
		}, mem, 0, 0, 1, ^uint32(0))
	})
}

func TestTaggedLatin1UTF16LengthAndCrossEncodingSize(t *testing.T) {
	if got := stringByteLen(3, EncLatin1UTF16); got != 3 {
		t.Fatalf("Latin-1 byte length = %d, want 3", got)
	}
	const tagged = latin1UTF16HighBit | 3
	if got := stringByteLen(tagged, EncLatin1UTF16); got != 6 {
		t.Fatalf("tagged UTF-16 byte length = %d, want 6", got)
	}
	if got := stringByteLen(latin1UTF16HighBit|0x7fffffff, EncLatin1UTF16); got != 0xfffffffe {
		t.Fatalf("maximum tagged UTF-16 byte length = %#x, want 0xfffffffe", got)
	}

	mem := setupMem(t, 0)
	helper := &batchReallocHelper{scratch: mem}
	for _, tc := range []struct {
		name     string
		srcCoded uint32
		srcEnc   StringEncoding
		dstEnc   StringEncoding
		want     uint32
	}{
		{name: "latin1 to utf16", srcCoded: 3, srcEnc: EncLatin1UTF16, dstEnc: EncUTF16, want: 6},
		{name: "tagged utf16 to utf8", srcCoded: tagged, srcEnc: EncLatin1UTF16, dstEnc: EncUTF8, want: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &allocSink{helper: helper}
			stringContentSizeStep(&transferContext{
				caller: &transferSide{StringEncoding: tc.srcEnc},
				callee: &transferSide{StringEncoding: tc.dstEnc},
			}, tc.srcCoded, sink)
			got, ok := mem.ReadUint32Le(0)
			if !ok || got != tc.want {
				t.Fatalf("discovered allocation = %d, want %d (ok=%t)", got, tc.want, ok)
			}
		})
	}
}
