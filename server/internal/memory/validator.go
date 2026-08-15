package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrInvalidCandidate = errors.New("invalid memory candidate")
	ErrSecretContent    = errors.New("secret content rejected")
	ErrMissingEvidence  = errors.New("evidence binding required")
)

const maxContentBytes = 8 * 1024

// secretPatterns are the fail-closed detectors for secrets/private data.
// safe content may mention the word "key" but never a credential assignment,
// a connection string, or a private-key block.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|jwt|password|passwd|secret|authorization)\s*[:=]\s*[^\s]+`),
	regexp.MustCompile(`(?i)postgres(ql)?://[^\s]+`),
	regexp.MustCompile(`(?i)mongodb(\+srv)?://[^\s]+`),
	regexp.MustCompile(`(?i)redis://[^\s]+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// Validate enforces the strict candidate contract. It is the single gate
// every candidate must pass; there is no bypass.
func Validate(c MemoryCandidate) error {
	if c.ID == "" {
		return fmt.Errorf("%w: id required", ErrInvalidCandidate)
	}
	if c.EmployeeID == "" {
		return fmt.Errorf("%w: employee namespace required", ErrInvalidCandidate)
	}
	if c.Kind != KindEpisodic && c.Kind != KindExperience {
		return fmt.Errorf("%w: invalid kind %q", ErrInvalidCandidate, c.Kind)
	}
	if strings.TrimSpace(c.Content) == "" {
		return fmt.Errorf("%w: empty content", ErrInvalidCandidate)
	}
	if len(c.Content) > maxContentBytes {
		return fmt.Errorf("%w: content exceeds %d bytes", ErrInvalidCandidate, maxContentBytes)
	}
	if c.AuthorID == "" {
		return fmt.Errorf("%w: author required", ErrInvalidCandidate)
	}
	if len(c.Evidence) == 0 {
		return ErrMissingEvidence
	}
	// Experience is induction over multiple works: require >= 2 evidence refs.
	if c.Kind == KindExperience && len(c.Evidence) < 2 {
		return fmt.Errorf("%w: experience requires at least 2 evidence refs", ErrInvalidCandidate)
	}
	for _, e := range c.Evidence {
		if e.ID == "" || !validEvidenceType(e.Type) {
			return fmt.Errorf("%w: invalid evidence ref", ErrInvalidCandidate)
		}
	}
	for _, p := range secretPatterns {
		if p.MatchString(c.Content) {
			return ErrSecretContent
		}
	}
	return nil
}

func validEvidenceType(t string) bool {
	switch t {
	case "task", "run", "outcome":
		return true
	default:
		return false
	}
}
