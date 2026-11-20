package fixturetests_test

import (
	"context"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/canvas"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/shapes"
)

func shapeSize(s shapes.Shape) uint32 {
	switch v := s.(type) {
	case shapes.ShapeCircle:
		return v.Value
	case shapes.ShapeSquare:
		return v.Value * v.Value
	}
	return 0
}

type myShapes struct{}

func (myShapes) Count(ctx context.Context, d shapes.Drawing) (uint32, error) {
	return uint32(len(d.Shapes)), nil
}

func (myShapes) Depth(ctx context.Context, s shapes.Scene) (uint32, error) {
	var n uint32
	for _, l := range s.Layers {
		if d, ok := l.(shapes.LayerDrawn); ok {
			n += uint32(len(d.Value.Shapes))
		}
	}
	if s.Focus.IsSome {
		n += 10 * shapeSize(s.Focus.Value)
	}
	n += 100 * (shapeSize(s.Origin.F0) + s.Origin.F1)
	if e, ok := s.Last.(shapes.ResultDrawingShapeErr); ok {
		n += 1000 * shapeSize(e.Value)
	}
	return n, nil
}

type myCanvas struct{}

func (myCanvas) Area(ctx context.Context, d shapes.Drawing) (uint32, error) {
	var a uint32
	for _, s := range d.Shapes {
		a += shapeSize(s)
	}
	return a, nil
}

func TestShapes_RecordsUseLaterVariants(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	fac, err := shapes.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer fac.Close(ctx)

	inst, err := fac.NewInstance(ctx, myShapes{}, nil)
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	defer inst.Close(ctx)

	w := shapes.WrapInstance(inst, inst.Core())
	d := shapes.Drawing{Shapes: []shapes.Shape{shapes.ShapeCircle{Value: 1}, shapes.ShapeSquare{Value: 2}}}

	n, err := w.Count(ctx, d)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}

	scene := shapes.Scene{
		Layers: []shapes.Layer{
			shapes.LayerDrawn{Value: d},
			shapes.LayerEmpty{},
			shapes.LayerDrawn{Value: shapes.Drawing{Shapes: []shapes.Shape{shapes.ShapeSquare{Value: 3}}}},
		},
		Focus:  shapes.SomeShape(shapes.ShapeCircle{Value: 4}),
		Origin: shapes.TupleShapeU32{F0: shapes.ShapeSquare{Value: 2}, F1: 1},
		Last:   shapes.ResultDrawingShapeErr{Value: shapes.ShapeCircle{Value: 6}},
	}
	depth, err := w.Depth(ctx, scene)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if want := uint32(3 + 10*4 + 100*(4+1) + 1000*6); depth != want {
		t.Errorf("Depth = %d, want %d", depth, want)
	}
}

func TestCanvas_ImportedRecordUsesImportedVariant(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	shapesFac, err := shapes.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("shapes.NewFactory: %v", err)
	}
	defer shapesFac.Close(ctx)
	shapesInst, err := shapesFac.NewInstance(ctx, myShapes{}, nil)
	if err != nil {
		t.Fatalf("shapes.NewInstance: %v", err)
	}
	defer shapesInst.Close(ctx)

	fac, err := canvas.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("canvas.NewFactory: %v", err)
	}
	defer fac.Close(ctx)
	inst, err := fac.NewInstance(ctx, myCanvas{}, &canvas.Deps{Shapes: shapesInst.Core()})
	if err != nil {
		t.Fatalf("canvas.NewInstance: %v", err)
	}
	defer inst.Close(ctx)

	w := canvas.WrapInstance(inst, inst.Core())
	area, err := w.Area(ctx, shapes.Drawing{Shapes: []shapes.Shape{shapes.ShapeCircle{Value: 1}, shapes.ShapeSquare{Value: 2}}})
	if err != nil {
		t.Fatalf("Area: %v", err)
	}
	if area != 5 {
		t.Errorf("Area = %d, want 5", area)
	}
}
