package wasmparser

import (
	"slices"
	"testing"
)

func TestHostTypeBuilder_NonNil(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	if b == nil {
		t.Fatal("want non-nil HostTypeBuilder")
	}
}

func TestHostTypeBuilder_AllocResourceID_Unique(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	a := b.AllocResourceID()
	c := b.AllocResourceID()
	if a == c {
		t.Fatalf("want distinct ResourceIDs, got %v twice", a)
	}
}

func TestHostTypeBuilder_PushDefinedType_StoresInArena(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	id := b.PushDefinedType(DefinedTypeDesc{
		Kind:      DefinedKindPrimitive,
		Primitive: PrimU32,
	})
	if int(id) >= len(arena.DefinedTypes) {
		t.Fatalf("id %d out of range (arena has %d entries)", id, len(arena.DefinedTypes))
	}
	got := arena.DefinedTypes[id]
	if got.Kind != DefinedKindPrimitive || got.Primitive != PrimU32 {
		t.Fatalf("got %+v, want primitive u32", got)
	}
}

func TestHostTypeBuilder_PushFuncType_StoresInArena(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	id := b.PushFuncType(FuncTypeDesc{
		Params: []FuncParamDesc{
			{Name: "x", Type: ValTypeDesc{IsPrimitive: true, Primitive: PrimU32}},
		},
	})
	if int(id) >= len(arena.FuncTypes) {
		t.Fatal("id out of range")
	}
	got := arena.FuncTypes[id]
	if len(got.Params) != 1 || got.Params[0].Name != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestComponentSubtypeChecksUnorderedEntries(t *testing.T) {
	env := ComponentEntityType{Kind: EntityInstance}
	for _, tc := range []struct {
		name string
		a, b ComponentTypeDesc
	}{
		{"import", ComponentTypeDesc{Imports: map[string]ComponentEntityType{"env": env}}, ComponentTypeDesc{}},
		{"export", ComponentTypeDesc{}, ComponentTypeDesc{Exports: map[string]ComponentEntityType{"env": env}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arena := NewArena(DefaultFeatures())
			builder := arena.HostTypeBuilder()
			a := builder.PushComponentType(tc.a)
			b := builder.PushComponentType(tc.b)
			sc := SubtypeChecker{aArena: arena, bArena: arena}
			if err := sc.isComponentSubtype(a, b); err == nil {
				t.Fatalf("accepted an unsatisfied %s", tc.name)
			}
		})
	}
}

func TestHostTypeBuilder_PushComponentType_StoresInArena(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	id := b.PushComponentType(ComponentTypeDesc{
		Imports: map[string]ComponentEntityType{},
		Exports: map[string]ComponentEntityType{},
	})
	if int(id) >= len(arena.ComponentTypes) {
		t.Fatal("id out of range")
	}
}

func TestHostTypeBuilder_NewComponentTypeHandle(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	id := b.PushComponentType(ComponentTypeDesc{
		Imports: map[string]ComponentEntityType{},
		Exports: map[string]ComponentEntityType{},
	})
	h := b.NewComponentTypeHandle(id)
	if h == nil {
		t.Fatal("want non-nil *ComponentType")
	}
	// Smoke: NewInstance should return a non-nil *InstanceType.
	inst := testInstance(t, h)
	if inst == nil {
		t.Fatal("want non-nil *InstanceType")
	}
}

func TestHostTypeBuilder_SynthComponentCheckInstantiation(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()

	// Host side: a ComponentType exporting one func "hi: func()".
	hostFnID := b.PushFuncType(FuncTypeDesc{})
	hostCompID := b.PushComponentType(ComponentTypeDesc{
		Imports: map[string]ComponentEntityType{},
		Exports: map[string]ComponentEntityType{
			"hi": {Kind: EntityFunc, FuncID: hostFnID},
		},
		ExportOrder: []string{"hi"},
	})
	hostComp := b.NewComponentTypeHandle(hostCompID)
	hostInst := testInstance(t, hostComp)

	// Consumer side: one import "h" of instance type matching host.
	consFnID := b.PushFuncType(FuncTypeDesc{})
	consInstID := b.PushInstanceType(InstanceTypeDesc{
		Exports: map[string]ComponentEntityType{
			"hi": {Kind: EntityFunc, FuncID: consFnID},
		},
	})
	consCompID := b.PushComponentType(ComponentTypeDesc{
		Imports: map[string]ComponentEntityType{
			"h": {Kind: EntityInstance, InstID: consInstID},
		},
		ImportOrder: []string{"h"},
		Exports:     map[string]ComponentEntityType{},
	})
	consComp := b.NewComponentTypeHandle(consCompID)

	// Check: supplying the host instance should satisfy consumer's "h".
	if err := consComp.CheckInstantiation(map[string]any{"h": hostInst}); err != nil {
		t.Fatalf("CheckInstantiation: %v", err)
	}
}

func TestNewArena_StandalonePush(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	if arena == nil {
		t.Fatal("NewArena returned nil")
	}
	id := arena.pushDefinedType(DefinedTypeDesc{
		Kind:      DefinedKindPrimitive,
		Primitive: PrimString,
	})
	if id != 0 {
		t.Fatalf("first push: got id %d, want 0", id)
	}
	if got := len(arena.DefinedTypes); got != 1 {
		t.Fatalf("DefinedTypes length: got %d, want 1", got)
	}
}

func TestTypeArena_CoreModuleTypeFromBytes(t *testing.T) {
	// Minimal core module: (module (memory (export "mem") 1))
	bin := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // header
		0x05, 0x03, 0x01, 0x00, 0x01, // memory section: 1 memory, min=1
		0x07, 0x07, 0x01, 0x03, 'm', 'e', 'm', 0x02, 0x00, // export "mem" (memory 0)
	}
	arena := NewArena(DefaultFeatures())
	desc, err := arena.CoreModuleTypeFromBytes(bin)
	if err != nil {
		t.Fatalf("CoreModuleTypeFromBytes: %v", err)
	}
	if _, ok := desc.Exports["mem"]; !ok {
		t.Fatalf("expected export 'mem', got %+v", desc.Exports)
	}
}

func TestTypeArena_HostTypeBuilder(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	if b == nil {
		t.Fatal("HostTypeBuilder returned nil")
	}
	rid := b.AllocResourceID()
	if rid != 0 {
		t.Fatalf("first AllocResourceID: got %d, want 0", rid)
	}
	if arena.nextResourceID != 1 {
		t.Fatalf("arena.nextResourceID after alloc: got %d, want 1", arena.nextResourceID)
	}
}

func TestHostTypeBuilder_PushInstanceType_OrdersExports(t *testing.T) {
	arena := NewArena(DefaultFeatures())
	b := arena.HostTypeBuilder()
	exports := map[string]ComponentEntityType{"zeta": {Kind: EntityModule}, "alpha": {Kind: EntityModule}, "mid": {Kind: EntityModule}}
	for name, c := range map[string]struct {
		order, want []string
	}{
		"none":    {nil, []string{"alpha", "mid", "zeta"}},
		"partial": {[]string{"zeta", "gone", "zeta"}, []string{"zeta", "alpha", "mid"}},
		"full":    {[]string{"mid", "zeta", "alpha"}, []string{"mid", "zeta", "alpha"}},
	} {
		t.Run(name, func(t *testing.T) {
			id := b.PushInstanceType(InstanceTypeDesc{Exports: exports, ExportOrder: c.order})
			if got := arena.InstanceTypes[id].ExportOrder; !slices.Equal(got, c.want) {
				t.Fatalf("ExportOrder = %q, want %q", got, c.want)
			}
		})
	}
}
