package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/partite-ai/wacogo/wasmparser"
)

// CoreTable describes a table defined by a core module. Imported tables are
// borrowed from their defining instance and do not allocate another table.
type CoreTable = wasmparser.CoreTableType

// CoreModuleTables reads defined table limits without instantiating the module.
// It does not replace the runtime's full module validation.
func CoreModuleTables(data []byte) ([]CoreTable, error) {
	p := wasmparser.NewParser(bytes.NewReader(data))
	payload, err := p.Next()
	if err != nil {
		return nil, err
	}
	version, ok := payload.(*wasmparser.VersionPayload)
	if !ok || version.Encoding != wasmparser.EncodingModule {
		return nil, errors.New("expected a core WebAssembly module")
	}
	var tables []CoreTable
	for {
		payload, err := p.Next()
		if err == io.EOF {
			return tables, nil
		}
		if err != nil {
			return nil, err
		}
		if section, ok := payload.(*wasmparser.ModuleTableSectionPayload); ok {
			for table, err := range section.Items() {
				if err != nil {
					return nil, err
				}
				tables = append(tables, table)
			}
		}
	}
}

type coreTableAllocatorKey struct{}

type coreTableAllocator func([]CoreTable) (func(), error)

// WithCoreTableAllocator installs admission accounting for each newly
// instantiated inline core module. The callback runs before table allocation,
// receives only defined tables, and may reject the instance. A returned release
// function runs once after module teardown, or if instantiation fails. Nested
// instances inherit the context; imported table aliases are not charged again.
// The callback must be safe for concurrent use and must not retain the slice.
func WithCoreTableAllocator(ctx context.Context, allocate func([]CoreTable) (release func(), err error)) context.Context {
	return context.WithValue(ctx, coreTableAllocatorKey{}, coreTableAllocator(allocate))
}

type tableReservation struct{ release func() }

func (r tableReservation) Close(context.Context) error {
	r.release()
	return nil
}

func reserveCoreTables(ctx context.Context, tables []CoreTable) (func(), error) {
	allocate, ok := ctx.Value(coreTableAllocatorKey{}).(coreTableAllocator)
	if !ok || allocate == nil || len(tables) == 0 {
		return nil, nil
	}
	// Each callback receives its own limits; compiled metadata stays immutable.
	copyOfTables := append([]CoreTable(nil), tables...)
	release, err := allocate(copyOfTables)
	if release != nil {
		release = sync.OnceFunc(release)
	}
	return release, err
}
