package wacogo

import (
	"context"

	"github.com/partite-ai/wacogo/internal/core"
)

// CoreTable describes the element type and limits of a defined core table.
// Max.Valid distinguishes finite maxima from unbounded tables.
type CoreTable = core.CoreTable

// CoreModuleTables reads defined tables from a core WebAssembly binary without
// instantiating it. Imported tables are excluded. Full module validation remains
// the runtime's responsibility.
func CoreModuleTables(data []byte) ([]CoreTable, error) { return core.CoreModuleTables(data) }

// WithCoreTableAllocator installs admission for inline core module instances. It
// runs before allocation and receives only defined tables. A returned release
// runs once after module teardown or upon failed instantiation. Nested instances
// inherit the context; borrowed table imports are not charged again. The
// callback must be concurrency safe and must not retain the supplied slice.
// Opaque modules supplied through WrapCompiledModule carry no table metadata;
// their trusted host remains responsible for their resource policy.
func WithCoreTableAllocator(ctx context.Context, allocate func([]CoreTable) (release func(), err error)) context.Context {
	return core.WithCoreTableAllocator(ctx, allocate)
}
