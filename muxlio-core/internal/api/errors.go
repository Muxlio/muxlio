package api

import "github.com/muxlio/muxlio-core/internal/diagnostic"

// Stable MUXLIO_* error codes for the HTTP API.
//
// Every response with a non-2xx status includes a JSON body:
//
//	{
//	  "code":    "MUXLIO_...",
//	  "message": "human-readable description",
//	  "fix":     "actionable repair instruction",       // omitted when not applicable
//	  "details": [ { "code": "...", "message": "...", "fix": "..." }, ... ] // omitted when empty
//	}
//
// Coding agents should match on `code`, not on `message` or HTTP status, since
// messages may be refined across minor releases while codes remain stable.

// API-level error codes (request/protocol layer).
const (
	// 400
	CodeBadRequest            = "MUXLIO_BAD_REQUEST"
	CodeInvalidJSON           = "MUXLIO_INVALID_JSON"
	CodeMissingRunID          = "MUXLIO_MISSING_RUN_ID"
	CodeUnsupportedProtocol   = "MUXLIO_UNSUPPORTED_PROTOCOL"
	CodeInvalidRegistration   = "MUXLIO_INVALID_REGISTRATION"
	CodeInvalidCompletion     = "MUXLIO_INVALID_COMPLETION"
	CodeInvalidArtifactDigest = "MUXLIO_INVALID_ARTIFACT_DIGEST"
	CodeBadEventCursor        = "MUXLIO_BAD_EVENT_CURSOR"

	// 401
	CodeUnauthorized = "MUXLIO_UNAUTHORIZED"

	// 404
	CodeNotFound = "MUXLIO_NOT_FOUND"

	// 405
	CodeMethodNotAllowed = "MUXLIO_METHOD_NOT_ALLOWED"

	// 409
	CodeWorkerNotRegistered = "MUXLIO_WORKER_NOT_REGISTERED"
	CodeStaleAssignment     = "MUXLIO_STALE_ASSIGNMENT"
	CodeRunControlRejected  = "MUXLIO_RUN_CONTROL_REJECTED"
	CodeInputRequestClosed  = "MUXLIO_INPUT_REQUEST_CLOSED"

	// 422 – semantic / validation failures
	CodeValidationError   = "MUXLIO_VALIDATION_ERROR"
	CodeDigestMismatch    = "MUXLIO_DIGEST_MISMATCH"
	CodeArtifactIntegrity = "MUXLIO_ARTIFACT_INTEGRITY"

	// 500 / 503 – server-side
	CodeStoreError             = "MUXLIO_STORE_ERROR"
	CodeRunError               = "MUXLIO_RUN_ERROR"
	CodeRunnerUnavailable      = "MUXLIO_RUNNER_UNAVAILABLE"
	CodeRunControlUnavailable  = "MUXLIO_RUN_CONTROL_UNAVAILABLE"
	CodeArtifactUploadFailed   = "MUXLIO_ARTIFACT_UPLOAD_FAILED"
	CodeArtifactDownloadFailed = "MUXLIO_ARTIFACT_DOWNLOAD_FAILED"
	CodeStreamUnsupported      = "MUXLIO_STREAM_UNSUPPORTED"
	CodeCoordinatorError       = "MUXLIO_COORDINATOR_ERROR"
	CodeWorkerProtocolError    = "MUXLIO_WORKER_PROTOCOL_UNAVAILABLE"
)

// fix messages paired with their codes.
var codeFix = map[string]string{
	CodeBadRequest:                            "Check the request body and URL parameters; resend a well-formed request.",
	CodeInvalidJSON:                           "Ensure the request body is a single, valid JSON object.",
	CodeMissingRunID:                          "Include a run ID in the URL path, e.g. /v1/runs/<runId>.",
	CodeUnsupportedProtocol:                   "Set protocolVersion to \"v1\" in the worker registration payload.",
	CodeInvalidRegistration:                   "Provide a non-empty workerId, a positive capacity, and trimmed unique capability names.",
	CodeInvalidCompletion:                     "Supply assignment identity fields, a positive coordinatorEpoch, positive generation, and a terminal state (succeeded|failed|cancelled).",
	CodeInvalidArtifactDigest:                 "Use a well-formed digest in the URL, e.g. sha256:<64 hex chars>.",
	CodeBadEventCursor:                        "Set the after query parameter to a non-negative integer sequence number.",
	CodeUnauthorized:                          "Provide a valid Bearer token in the Authorization header.",
	CodeNotFound:                              "Verify the ID exists; use GET /v1/runs to list known runs.",
	CodeMethodNotAllowed:                      "Consult the Allow response header for the supported HTTP methods on this endpoint.",
	CodeWorkerNotRegistered:                   "Call POST /v1/workers/register before polling for assignments or sending heartbeats.",
	CodeStaleAssignment:                       "Fetch a fresh assignment via GET /v1/workers/<workerId>/assignments.",
	CodeRunControlRejected:                    "The run may already be terminal or in an incompatible state; check GET /v1/runs/<runId>.",
	CodeInputRequestClosed:                    "List input requests to inspect their state; retry accepted delivery with its original Idempotency-Key.",
	"MUXLIO_INPUT_RESPONSE_VALIDATION_FAILED": "Submit a JSON value matching the input request's schema.",
	CodeValidationError:                       "Correct the plan validation errors listed in details[] and resubmit.",
	CodeDigestMismatch:                        "Recompile the workflow to regenerate a consistent plan digest before submitting.",
	CodeArtifactIntegrity:                     "Recompute the artifact digest and reupload the correct bytes.",
	CodeStoreError:                            "Retry the request; if the problem persists inspect coordinator logs.",
	CodeRunError:                              "Inspect the run events via GET /v1/runs/<runId>/events for failure details.",
	CodeRunnerUnavailable:                     "The coordinator has no runner configured; ensure the server was started correctly.",
	CodeRunControlUnavailable:                 "Run pause/resume/cancel controls require a coordinator started with run control support.",
	CodeArtifactUploadFailed:                  "Verify the uploaded bytes match the declared digest and retry.",
	CodeArtifactDownloadFailed:                "The artifact may not have been uploaded yet; check the digest and retry.",
	CodeStreamUnsupported:                     "The reverse proxy or transport does not support streaming; use GET /v1/runs/<runId>/events for paginated events instead.",
	CodeCoordinatorError:                      "An unexpected coordinator error occurred; inspect coordinator logs.",
	CodeWorkerProtocolError:                   "Worker protocol is not configured on this coordinator.",
}

// APIError is the structured error envelope returned by every non-2xx
// API response.
type APIError struct {
	// Code is a stable MUXLIO_* identifier. Coding agents should branch
	// on this field to detect and repair failures.
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Phase    string `json:"phase"`
	Code     string `json:"code"`
	// Message is a human-readable explanation of what went wrong.
	Message string `json:"message"`
	// Fix is an actionable suggestion for how to resolve the error.
	// Omitted when no specific guidance is available.
	Fix string `json:"fix,omitempty"`
	// Details carries sub-errors, e.g. individual plan validation failures.
	// Omitted when empty.
	Details []ErrorDetail `json:"details,omitempty"`
}

// ErrorDetail is a single structured sub-error within an APIError.
type ErrorDetail = diagnostic.Diagnostic
