package errs

const (
	// General error codes.
	//ServerInternalError = 500  // Server internal error
	//ArgsError           = 1001 // Input parameter error
	//NoPermissionError   = 1002 // Insufficient permission
	//DuplicateKeyError   = 1003
	//RecordNotFoundError = 1004 // Record does not exist
	//
	//TokenExpiredError     = 1501
	//TokenInvalidError     = 1502
	//TokenMalformedError   = 1503
	//TokenNotValidYetError = 1504
	//TokenUnknownError     = 1505
	//TokenKickedError      = 1506
	//TokenNotExistError    = 1507
	//
	//OrgUserNoPermissionError = 1520

	//更改为兼容grpc协议的状态码

	// General server error
	ServerInternalError = 13 // INTERNAL

	// Input parameter error
	ArgsError = 3 // INVALID_ARGUMENT

	// Permission/authorization
	NoPermissionError = 7 // PERMISSION_DENIED

	// Duplicate key
	DuplicateKeyError = 6 // ALREADY_EXISTS

	// Record not found
	RecordNotFoundError = 5 // NOT_FOUND

	// Token/authentication related
	TokenExpiredError     = 16 // UNAUTHENTICATED
	TokenInvalidError     = 16 // UNAUTHENTICATED
	TokenMalformedError   = 16 // UNAUTHENTICATED
	TokenNotValidYetError = 16 // UNAUTHENTICATED
	TokenUnknownError     = 16 // UNAUTHENTICATED
	TokenKickedError      = 16 // UNAUTHENTICATED
	TokenNotExistError    = 16 // UNAUTHENTICATED

	// Org/user permission related
	OrgUserNoPermissionError = 7 // PERMISSION_DENIED
)

var (
	ErrArgs                     = NewCodeError(ArgsError, "ArgsError")
	ErrNoPermission             = NewCodeError(NoPermissionError, "NoPermissionError")
	ErrInternalServer           = NewCodeError(ServerInternalError, "ServerInternalError")
	ErrRecordNotFound           = NewCodeError(RecordNotFoundError, "RecordNotFoundError")
	ErrDuplicateKey             = NewCodeError(DuplicateKeyError, "DuplicateKeyError")
	ErrTokenExpired             = NewCodeError(TokenExpiredError, "TokenExpiredError")
	ErrTokenInvalid             = NewCodeError(TokenInvalidError, "TokenInvalidError")
	ErrTokenMalformed           = NewCodeError(TokenMalformedError, "TokenMalformedError")
	ErrTokenNotValidYet         = NewCodeError(TokenNotValidYetError, "TokenNotValidYetError")
	ErrTokenUnknown             = NewCodeError(TokenUnknownError, "TokenUnknownError")
	ErrTokenKicked              = NewCodeError(TokenKickedError, "TokenKickedError")
	ErrTokenNotExist            = NewCodeError(TokenNotExistError, "TokenNotExistError")
	ErrOrgUserNoPermissionError = NewCodeError(OrgUserNoPermissionError, "OrgUserNoPermissionError")
)
