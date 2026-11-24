package types

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
)

type receiveError struct{ err error }

func (e receiveError) Error() string { return e.err.Error() }

func (e receiveError) Unwrap() error { return e.err }

func receiveErrorCode(err error) ErrorCode {
	if coded, ok := errors.AsType[*CodedError](err); ok && coded.Code != nil {
		return coded.Code
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return ErrorCodeConnectionReadTimeout{}
	}
	return ErrorCodeHTTPProtocolError{}
}

func httpErrorCode(err error) (ErrorCode, bool) {
	if coded, ok := errors.AsType[*CodedError](err); ok && coded.Code != nil {
		return coded.Code, true
	}
	if received, ok := errors.AsType[receiveError](err); ok {
		return receiveErrorCode(received.err), true
	}
	return nil, false
}

func ErrorCodeOf(err error) ErrorCode {
	if err == nil {
		return nil
	}

	var coded *CodedError
	if errors.As(err, &coded) && coded.Code != nil {
		return coded.Code
	}

	if errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrorCodeHTTPProtocolError{}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorCodeConnectionTimeout{}
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCodeInternalError{Value: SomeString(err.Error())}
	}

	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		switch {
		case dnsErr.IsTimeout:
			return ErrorCodeDNSTimeout{}
		case dnsErr.IsNotFound:
			return ErrorCodeDestinationNotFound{}
		}
		return ErrorCodeDNSError{
			Value: DNSErrorPayload{Rcode: SomeString(dnsErr.Err)},
		}
	}

	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return ErrorCodeTLSCertificateError{}
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return ErrorCodeTLSProtocolError{}
	}

	if opErr, ok := errors.AsType[*net.OpError](err); ok {
		if opErr.Timeout() {
			switch opErr.Op {
			case "read":
				return ErrorCodeConnectionReadTimeout{}
			case "write":
				return ErrorCodeConnectionWriteTimeout{}
			}
			return ErrorCodeConnectionTimeout{}
		}
		if sysErr, ok := errors.AsType[*os.SyscallError](opErr.Err); ok {
			if errno, ok := sysErr.Err.(syscall.Errno); ok {
				switch errno {
				case syscall.ECONNREFUSED:
					return ErrorCodeConnectionRefused{}
				case syscall.ECONNRESET, syscall.EPIPE:
					return ErrorCodeConnectionTerminated{}
				case syscall.ETIMEDOUT:
					return ErrorCodeConnectionTimeout{}
				case syscall.EHOSTUNREACH, syscall.ENETUNREACH:
					return ErrorCodeDestinationIPUnroutable{}
				}
			}
		}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return ErrorCodeConnectionTimeout{}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorCodeConnectionTimeout{}
	}

	return ErrorCodeInternalError{Value: SomeString(err.Error())}
}
