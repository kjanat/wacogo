# Python JSON formatter

Run Python's standard-library JSON formatter from a Go host through the
Component Model. The [WIT contract](guest/formatter.wit) exposes one function
on the versioned `example:python-json/formatter@0.1.0` interface. JSON comes in
and goes out as UTF-8 bytes; a parse error comes back as a WIT error value.

From the repository root, install Python 3.12 and create a virtual environment
with [componentize-py](https://github.com/bytecodealliance/componentize-py):

```sh
python -m venv .venv
.venv/bin/python -m pip install -r examples/python-json/requirements.txt
.venv/bin/componentize-py -d examples/python-json/guest/formatter.wit -w json-formatter \
  componentize --stub-wasi -p examples/python-json/guest formatter -o /tmp/python-json.wasm
printf '%s\n' '{"name":"café","valid":true}' |
  go run ./examples/python-json /tmp/python-json.wasm
PYTHON_JSON_COMPONENT=/tmp/python-json.wasm go test -v ./examples/python-json
```

This example uses Python's standard JSON decoder. Duplicate property names
collapse to the last value, and decimals can round to binary floating-point
precision. Use a lossless formatter when either behavior matters.

The build produces a component, so the Go host uses `LoadComponent` and
calls the nested interface export. It does not execute a Python subprocess.
This example targets the synchronous Canonical ABI. The formatter does not
need WASI at runtime, so `--stub-wasi` replaces its WASI imports with traps.
Do not use that option for applications that need WASI services, including
secure randomness: the tool's build-time snapshot can freeze PRNG state.
