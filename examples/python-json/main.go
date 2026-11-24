// python-json runs a synchronous Python JSON formatter as a Wasm component.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/kjanat/wacogo"
)

const formatterInterface = "example:python-json/formatter@0.1.0"

func formatJSON(ctx context.Context, guest io.Reader, source []byte) ([]byte, error) {
	engine := wacogo.NewEngine(ctx)
	defer engine.Close(ctx)

	component, err := engine.LoadComponent(ctx, guest)
	if err != nil {
		return nil, fmt.Errorf("load Python component: %w", err)
	}
	defer component.Close(ctx)
	instance, err := component.Instantiate(ctx)
	if err != nil {
		return nil, fmt.Errorf("instantiate Python component: %w", err)
	}
	defer instance.Close(ctx)

	formatter := instance.ExportedInstance(formatterInterface)
	if formatter == nil {
		return nil, fmt.Errorf("missing exported interface %q", formatterInterface)
	}
	format := formatter.ExportedFunc("format-json")
	if format == nil {
		return nil, errors.New("missing format-json export")
	}

	input := make([]wacogo.ValU8, len(source))
	for i, b := range source {
		input[i] = wacogo.ValU8(b)
	}
	values, err := format.Call(ctx, wacogo.NewValListOf(input...))
	if err != nil {
		return nil, fmt.Errorf("call format-json: %w", err)
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("format-json returned %d values, want 1", len(values))
	}
	result, ok := values[0].(*wacogo.ValResult)
	if !ok {
		return nil, fmt.Errorf("format-json returned %T, want result", values[0])
	}
	if !result.IsOk() {
		message, ok := result.Err().(wacogo.ValString)
		if !ok {
			return nil, fmt.Errorf("format-json returned %T as error, want string", result.Err())
		}
		return nil, fmt.Errorf("Python formatter: %s", message)
	}
	list, ok := result.Ok().(*wacogo.ValList)
	if !ok {
		return nil, fmt.Errorf("format-json returned %T as success, want list<u8>", result.Ok())
	}
	output := make([]byte, list.Len())
	for i := range output {
		b, ok := list.Get(i).(wacogo.ValU8)
		if !ok {
			return nil, fmt.Errorf("output byte %d has type %T, want u8", i, list.Get(i))
		}
		output[i] = byte(b)
	}
	return output, nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s COMPONENT.wasm < INPUT.json", os.Args[0])
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

func run(path string) error {
	guest, err := os.Open(path)
	if err != nil {
		return err
	}
	defer guest.Close()
	source, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	output, err := formatJSON(context.Background(), guest, source)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(output)
	return err
}
