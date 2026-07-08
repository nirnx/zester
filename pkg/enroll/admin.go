package enroll

import "errors"

// AdminRequest is the MessagePack wire type for operator-initiated
// enrollment admin operations. It rides the request/reply subjects
// bus.SubjectAdminEnrollApprove, bus.SubjectAdminEnrollReject, and
// bus.SubjectAdminEnrollRevoke: the subject selects the operation, the
// payload carries its arguments, and the master's admin queue group answers
// with an AdminResponse. Fields are additive-only (msgpack tags, omitempty
// for optional fields) so mixed CLI/master versions interoperate.
type AdminRequest struct {
	// ID is the enrollment record ID (e.g. "enr-...").
	ID string `msgpack:"id"`

	// Operator identifies who performed the action; it is recorded on the
	// enrollment record as DecidedBy.
	Operator string `msgpack:"operator"`

	// Reason is an optional human-readable justification, recorded as
	// RejectReason for reject and revoke operations.
	Reason string `msgpack:"reason,omitempty"`

	// Force overrides the refusal to approve a trust-mismatched record
	// (a possible first-contact MITM). Additive; pre-feature clients omit it.
	Force bool `msgpack:"force,omitempty"`
}

// Validate reports whether the request carries the required fields.
func (r *AdminRequest) Validate() error {
	if r.ID == "" {
		return errors.New("enroll: admin request: id is required")
	}
	if r.Operator == "" {
		return errors.New("enroll: admin request: operator is required")
	}
	return nil
}

// AdminResponse is the MessagePack reply to an AdminRequest: the updated
// enrollment record on success, or a non-empty error string on failure.
type AdminResponse struct {
	// Record is the enrollment record after the state transition.
	Record *Record `msgpack:"record,omitempty"`

	// Err is a non-empty error message when the operation failed.
	Err string `msgpack:"err,omitempty"`
}

// AsError converts a failed response into an error; nil on success.
func (r *AdminResponse) AsError() error {
	if r.Err == "" {
		return nil
	}
	return errors.New(r.Err)
}
