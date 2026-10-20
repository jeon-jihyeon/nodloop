package datapackage

import "errors"

var (
	ErrFolderLink     = errors.New("datapackage: the folder is a link")
	ErrFolderNotEmpty = errors.New("datapackage: the folder is not empty")
	ErrNotFolder      = errors.New("datapackage: the path is no folder")
	ErrFieldInvalid   = errors.New("datapackage: the JSON Schema of a field does not decode")
	ErrRowInvalid     = errors.New("datapackage: a row is no JSON object")
)
