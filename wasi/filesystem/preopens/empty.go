package preopens

import (
	"context"

	gen "github.com/kjanat/wacogo/internal/wasi/gen/wasi/filesystem/preopens"
)

type EmptyFS struct{}

func (EmptyFS) GetDirectories(_ context.Context) ([]TupleDescriptorString, error) {
	return nil, nil
}

var _ gen.Preopens = EmptyFS{}
