package catalog

// Auditor access (PRD P4 FR-ACC-09 / §16 #18, NFR Audit): the external
// auditor holds a time-bound, read-only role; the role assignment must
// carry an expiry (enforced at authentication) and every read of the
// read-audited modules is written to the audit log.

import "slices"

// AuditorRole is the code of the Auditor role template.
const AuditorRole = "auditor"

// TimeBoundRoles must be assigned with an expiry (validUntil).
var TimeBoundRoles = []string{AuditorRole}

// ReadAuditedRoles have every successful read of ReadAuditedModules logged.
var ReadAuditedRoles = []string{AuditorRole}

// ReadAuditedModules are the modules whose reads are logged for
// ReadAuditedRoles (finance, reports and the audit log itself).
var ReadAuditedModules = []string{"accounting", "reporting", "audit"}

// IsTimeBound reports whether a role must be assigned with an expiry.
func IsTimeBound(code string) bool { return slices.Contains(TimeBoundRoles, code) }
