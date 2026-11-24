package types

import (
	"context"
	"errors"
	"fmt"

	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/io/wioerror"
	wasierr "github.com/kjanat/wacogo/wasi/internal/wasierr"
)

// impl is the top-level Types implementation
type impl struct {
	streamInst *host.ComponentInstance
	pollInst   *host.ComponentInstance
	errorInst  *host.ComponentInstance
}

func (i *impl) HTTPErrorCode(_ context.Context, h *wioerror.ErrorResourceHandle) (OptionErrorCode, error) {
	res, ok := h.LocalImpl()
	if !ok {
		return OptionErrorCode{}, nil
	}
	failure, ok := res.(interface{ Unwrap() error })
	if !ok {
		return OptionErrorCode{}, nil
	}
	code, ok := httpErrorCode(failure.Unwrap())
	return OptionErrorCode{IsSome: ok, Value: code}, nil
}

func (i *impl) NewFields(_ context.Context) (*FieldsHandle, error) {
	return NewFieldsHandle(newFieldsImpl(nil, false)), nil
}

func (i *impl) FieldsFromList(_ context.Context, values []TupleStringListU8) (ResultFieldsHeaderError, error) {
	fields, herr := fieldsFromList(values)
	if herr != nil {
		return types.ResultFieldsHeaderErrorErr{Value: herr}, nil
	}
	return types.ResultFieldsHeaderErrorOk{Value: NewFieldsHandle(fields)}, nil
}

func (i *impl) NewOutgoingRequest(ctx context.Context, fields *FieldsHandle) (*OutgoingRequestHandle, error) {
	entries, err := entriesFromFields(ctx, fields)
	if dropErr := fields.Drop(ctx); err != nil || dropErr != nil {
		return nil, errors.Join(err, dropErr)
	}
	ri := newOutgoingRequestImpl(entries, i.streamInst, i.pollInst, i.errorInst)
	return NewOutgoingRequestHandle(ri), nil
}

func (i *impl) NewRequestOptions(_ context.Context) (*RequestOptionsHandle, error) {
	impl := &requestOptionsImpl{}
	return NewRequestOptionsHandle(impl), nil
}

func (i *impl) ResponseOutparamSet(_ context.Context, _ *ResponseOutparamHandle, _ ResultOutgoingResponseErrorCode) error {
	return fmt.Errorf("wasi:http/types.response-outparam.set: %w", wasierr.ErrNotImplemented)
}

func (i *impl) IncomingBodyFinish(ctx context.Context, bodyHandle *IncomingBodyHandle) (*FutureTrailersHandle, error) {
	impl, ok := bodyHandle.LocalImpl()
	if !ok {
		return nil, fmt.Errorf("wasi:http/types.incoming-body.finish: unknown incoming body handle")
	}
	bImpl, ok := impl.(*incomingBodyImpl)
	if !ok {
		return nil, fmt.Errorf("wasi:http/types.incoming-body.finish: invalid incoming body handle")
	}
	trailers, err := bImpl.finish(i.pollInst)
	if err != nil {
		return nil, fmt.Errorf("wasi:http/types.incoming-body.finish: %w", err)
	}
	if err := bodyHandle.Drop(ctx); err != nil {
		return nil, fmt.Errorf("wasi:http/types.incoming-body.finish: %w", err)
	}
	return NewFutureTrailersHandle(trailers), nil
}

func (i *impl) NewOutgoingResponse(_ context.Context, _ *FieldsHandle) (*OutgoingResponseHandle, error) {
	return nil, fmt.Errorf("wasi:http/types.outgoing-response.constructor: %w", wasierr.ErrNotImplemented)
}

func (i *impl) OutgoingBodyFinish(ctx context.Context, bh *OutgoingBodyHandle, trailers OptionFields) (result Result_ErrorCode, err error) {
	defer func() {
		if dropErr := bh.Drop(ctx); dropErr != nil {
			result, err = nil, errors.Join(err, dropErr)
		}
	}()

	impl, ok := bh.LocalImpl()
	if !ok {
		return nil, fmt.Errorf("wasi:http/types.outgoing-body.finish: unknown outgoing body handle")
	}
	obImpl, ok := impl.(*outgoingBodyImpl)
	if !ok {
		return nil, fmt.Errorf("wasi:http/types.outgoing-body.finish: invalid outgoing body handle")
	}

	if trailers.IsSome {
		entries, err := entriesFromFields(ctx, trailers.Value)
		if dropErr := trailers.Value.Drop(ctx); err != nil || dropErr != nil {
			return nil, errors.Join(err, dropErr)
		}
		obImpl.request.r.Trailer = headerFromEntries(entries)
	}

	if obImpl.writeOpened && !obImpl.streamDropped {
		obImpl.streamDropped = true // prevent the finalizer from panicking
		return nil, fmt.Errorf("wasi:http/types.outgoing-body.finish: body stream not dropped")
	}

	if sizeErr := obImpl.sizeMismatch(); sizeErr != nil {
		obImpl.pipe.closeWithError(&CodedError{Code: ErrorCodeHTTPProtocolError{}, Msg: sizeErr.Msg})
		return types.Result_ErrorCodeErr{Value: sizeErr.Code}, nil
	}
	obImpl.pipe.closeWithError(nil)
	return types.Result_ErrorCodeOk{}, nil
}
