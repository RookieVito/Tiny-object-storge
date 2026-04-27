package s3error

import (
	"encoding/xml"
	"net/http"
)

// S3APIError 是携带 S3 错误码和 HTTP 状态码的错误类型。
type S3APIError struct {
	Code    string
	Message string
	Status  int
}

func (e *S3APIError) Error() string {
	return e.Code + ": " + e.Message
}

// 预定义的 S3 错误。
var (
	ErrNoSuchBucket        = &S3APIError{"NoSuchBucket", "The specified bucket does not exist.", http.StatusNotFound}
	ErrNoSuchKey           = &S3APIError{"NoSuchKey", "The specified key does not exist.", http.StatusNotFound}
	ErrBucketAlreadyExists = &S3APIError{"BucketAlreadyExists", "The requested bucket name already exists.", http.StatusConflict}
	ErrBucketNotEmpty      = &S3APIError{"BucketNotEmpty", "The bucket you tried to delete is not empty.", http.StatusConflict}
	ErrInvalidBucketName   = &S3APIError{"InvalidBucketName", "The specified bucket is not valid.", http.StatusBadRequest}
	ErrInvalidKey          = &S3APIError{"InvalidKey", "The specified key is not valid.", http.StatusBadRequest}
	ErrAccessDenied        = &S3APIError{"AccessDenied", "Access Denied.", http.StatusForbidden}
	ErrSignatureDoesNotMatch = &S3APIError{"SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.", http.StatusForbidden}
	ErrRequestEntityTooLarge = &S3APIError{"RequestEntityTooLarge", "Your proposed upload exceeds the maximum allowed size.", http.StatusRequestEntityTooLarge}
	ErrInsufficientStorage = &S3APIError{"InsufficientStorage", "Not enough available storage shards to complete the operation.", http.StatusServiceUnavailable}
	ErrWriteQuorumFailed = &S3APIError{"WriteQuorumFailed", "Failed to achieve write quorum. Not enough replicas available.", http.StatusServiceUnavailable}
	ErrReadQuorumFailed  = &S3APIError{"ReadQuorumFailed", "Failed to achieve read quorum. Not enough replicas available.", http.StatusServiceUnavailable}

	// Multipart upload 错误。
	ErrNoSuchUpload      = &S3APIError{"NoSuchUpload", "The specified upload does not exist.", http.StatusNotFound}
	ErrInvalidPart       = &S3APIError{"InvalidPart", "The specified part does not exist or ETag does not match.", http.StatusBadRequest}
	ErrEntityTooSmall    = &S3APIError{"EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.", http.StatusBadRequest}
	ErrInvalidPartOrder  = &S3APIError{"InvalidPartOrder", "The list of parts was not in ascending order. Parts must be ordered by part number.", http.StatusBadRequest}
	ErrInvalidPartNumber = &S3APIError{"InvalidPartNumber", "The part number must be between 1 and 10000, inclusive.", http.StatusBadRequest}
	ErrNotImplemented    = &S3APIError{"NotImplemented", "This functionality is not implemented.", http.StatusNotImplemented}

	// Range 请求错误。
	ErrInvalidRange = &S3APIError{"InvalidRange", "The requested range is not satisfiable.", http.StatusRequestedRangeNotSatisfiable}

	// Sig V4 认证错误。
	ErrRequestTimeTooSkewed  = &S3APIError{"RequestTimeTooSkewed", "The difference between the request time and the server time is too large.", http.StatusForbidden}
	ErrMissingSecurityHeader = &S3APIError{"MissingSecurityHeader", "Your request is missing a required header.", http.StatusBadRequest}

	// Presigned URL 错误。
	ErrExpiredPresign  = &S3APIError{"AccessDenied", "Request has expired.", http.StatusForbidden}
	ErrInvalidExpires  = &S3APIError{"InvalidArgument", "X-Amz-Expires must be between 1 and 604800.", http.StatusBadRequest}
)

// s3ErrorResponse 是 S3 错误的 XML 信封。
type s3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

// WriteS3Error 写入 S3 格式的 XML 错误响应。
func WriteS3Error(w http.ResponseWriter, code string, message string, status int, resource string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	resp := s3ErrorResponse{
		Code:      code,
		Message:   message,
		Resource:  resource,
		RequestID: "tiny-req-id",
	}
	xml.NewEncoder(w).Encode(resp)
}

// WriteS3Err 将 S3APIError 写入 XML 响应。
func WriteS3Err(w http.ResponseWriter, err *S3APIError, resource string) {
	WriteS3Error(w, err.Code, err.Message, err.Status, resource)
}
