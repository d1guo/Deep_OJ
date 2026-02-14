package repository

import (
	"regexp"
	"strings"
	"testing"
)

func TestClaimPendingOutboxSQLContract(t *testing.T) {
	if !regexp.MustCompile(`LEAST\(\s*\$4::numeric`).MatchString(claimPendingOutboxSQL) {
		t.Fatalf("claimPendingOutboxSQL must cap retry delay with LEAST(..., $4::numeric)")
	}
	if !strings.Contains(claimPendingOutboxSQL, "random()") {
		t.Fatalf("claimPendingOutboxSQL must include jitter via random()")
	}
	if !strings.Contains(claimPendingOutboxSQL, "interval '1 millisecond'") {
		t.Fatalf("claimPendingOutboxSQL must use interval '1 millisecond' syntax")
	}
	if !strings.Contains(claimPendingOutboxSQL, "LEAST(o.attempts, 30)") {
		t.Fatalf("claimPendingOutboxSQL must clamp exponent with LEAST(o.attempts, 30)")
	}
}
