package core

import "strings"

// Severity is a normalized, orderable severity. Raw source labels ("critical",
// "warning", "P2", …) are mapped onto it so the pre-filter can apply one
// threshold regardless of source vocabulary.
type Severity int

const (
	SevUnknown Severity = iota
	SevInfo
	SevWarning
	SevHigh
	SevCritical
)

// ParseSeverity maps a raw severity label onto a Severity. Unrecognized values
// return SevUnknown (which never passes the high/critical threshold).
func ParseSeverity(raw string) Severity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "crit", "fatal", "emergency", "page", "p1":
		return SevCritical
	case "high", "error", "err", "major", "p2":
		return SevHigh
	case "warning", "warn", "minor", "p3":
		return SevWarning
	case "info", "information", "informational", "low", "none", "p4", "p5":
		return SevInfo
	default:
		return SevUnknown
	}
}

// AtLeast reports whether s meets or exceeds threshold t.
func (s Severity) AtLeast(t Severity) bool { return s >= t }

// IncidentLevel maps s onto the incidents.severity enum. ok is false when s is
// below "high" — i.e. not worth a notebook.
func (s Severity) IncidentLevel() (level string, ok bool) {
	switch {
	case s >= SevCritical:
		return "critical", true
	case s >= SevHigh:
		return "high", true
	default:
		return "", false
	}
}
