package errs

import "errors"

var (
	// DriverInitPending means the saved driver is awaiting user authentication.
	DriverInitPending = errors.New("等待完成登录验证")
	EmptyToken        = errors.New("empty token")
	LinkIsDir         = errors.New("link is dir")
)
