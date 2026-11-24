package types

import (
	"io"
	"slices"
	"sync"
)

// wasmtime-wasi-http queues two chunks between the body stream and hyper.
const bodyPipeChunks = 2

type bodyPipe struct {
	chunks chan []byte

	closeOnce sync.Once
	closed    chan struct{}
	err       error

	readOnce   sync.Once
	readClosed chan struct{}
	pending    []byte
}

func newBodyPipe() *bodyPipe {
	return &bodyPipe{
		chunks:     make(chan []byte, bodyPipeChunks),
		closed:     make(chan struct{}),
		readClosed: make(chan struct{}),
	}
}

func (p *bodyPipe) Write(b []byte) (int, error) {
	select {
	case <-p.closed:
		return 0, io.ErrClosedPipe
	case <-p.readClosed:
		return 0, io.ErrClosedPipe
	default:
	}
	select {
	case p.chunks <- slices.Clone(b):
		return len(b), nil
	case <-p.closed:
		return 0, io.ErrClosedPipe
	case <-p.readClosed:
		return 0, io.ErrClosedPipe
	}
}

func (p *bodyPipe) closeWithError(err error) {
	p.closeOnce.Do(func() {
		p.err = err
		close(p.closed)
	})
}

func (p *bodyPipe) reader() io.ReadCloser { return bodyPipeReader{p} }

type bodyPipeReader struct{ p *bodyPipe }

func (r bodyPipeReader) Read(b []byte) (int, error) {
	p := r.p
	if len(p.pending) == 0 {
		select {
		case p.pending = <-p.chunks:
		case <-p.readClosed:
			return 0, io.ErrClosedPipe
		case <-p.closed:
			select {
			case p.pending = <-p.chunks:
			default:
				if p.err != nil {
					return 0, p.err
				}
				return 0, io.EOF
			}
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (r bodyPipeReader) Close() error {
	r.p.readOnce.Do(func() { close(r.p.readClosed) })
	return nil
}
