// Typed domain errors for the AiAssistJob aggregate. Adapters wrap these
// with HTTP status codes at the boundary; the domain layer surfaces
// semantics only.
package aiassist

import "errors"

// ErrNotFound is the canonical sentinel for a missing-or-cross-tenant
// AiAssistJob row. Adapters convert to HTTP 404.
var ErrNotFound = errors.New("aiassist: not found")
