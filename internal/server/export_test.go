package server

import "net/http"

var (
	HandlerFor   = (*Server).handlerFor
	WithConnInfo = withConnInfo
	StatusFor    = statusFor
	IfMatch      = ifMatch
	WriteProblem = (*Server).writeProblem
	WriteJSON    = (*Server).writeJSON
	WriteOp      = (*Server).writeOp
	Meta         = (*Server).meta
	ExemptMeta   = (*Server).exemptMeta
	LongCtx      = (*Server).longCtx
	PathInt      = pathInt
)

func DecodeBody[T any](r *http.Request) (T, []byte, error) { return decodeBody[T](r) }
