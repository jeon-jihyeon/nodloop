package textkey

import "errors"

var (
	ErrCreate  = errors.New("textkey: cannot create the HMAC key")
	ErrRead    = errors.New("textkey: cannot read the HMAC key")
	ErrInvalid = errors.New("textkey: the HMAC key is not 32 bytes")
	ErrShared  = errors.New("textkey: another user may read or write the HMAC key")
)
