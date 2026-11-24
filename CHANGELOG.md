# Changelog

## [Unreleased]

## [0.3.9] - 2026-11-24

### Changed

- v0.3.6 and v0.3.7 are retracted. v0.3.6 traps guests on body read errors. v0.3.7 also panics on valid components and forbids `Content-Length`.

### Fixed

- Outer aliases of components share the aliased component. A few-KB component that aliases the previous level twice at each of 40 levels loads and instantiates in linear time and memory.
- Outer aliases of types bind in the scope that defines them, including after the component leaves that scope.
- `incoming-body.finish` returns `future-trailers` that become ready once the body is received, with the response trailers or the receive error as an `error-code`.
- `http-error-code` returns the `error-code` of a failed body stream.
- A `Content-Length` the guest sets becomes the request's length. A write past it, `outgoing-body.finish` after a different number of bytes, and `handle` without a body fail with `HTTP-request-body-size`, as in wasmtime-wasi-http.
- A guest can write an outgoing body before or after `handle`, and `outgoing-body.finish` without a write stream succeeds.
- Dropping an incoming body or its stream closes the response without reading the rest of it.
- `blocking-write-and-flush` reports a write failure as `last-operation-failed`.
- `syncspec` confines archive extraction to the fixture mirror and rejects duplicate `.wast` entries without overwriting the first copy.
