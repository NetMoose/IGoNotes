package git

import "testing"

func TestGitConflictSafeErrors(t *testing.T) {
	tests := []struct {
		err  *SafeError
		code ErrorCode
		text string
	}{
		{ErrConflictNotFound, CodeConflictNotFound, "Git conflict was not found"},
		{ErrConflictStale, CodeConflictStale, "Git conflict changed; refresh and try again"},
		{ErrConflictUnresolved, CodeConflictUnresolved, "Git conflict still has unresolved paths"},
		{ErrConflictUnsupported, CodeConflictUnsupported, "Git conflict type is unsupported"},
		{ErrMergeNotInProgress, CodeMergeNotInProgress, "Git merge is not in progress"},
		{ErrRecoveryRequired, CodeRecoveryRequired, "Git repository requires recovery"},
		{ErrGitPaused, CodePaused, "Git synchronization is paused"},
	}
	for _, test := range tests {
		if test.err.Code != test.code || test.err.Message != test.text || test.err.Field != "" {
			t.Errorf("safe error = %#v, want %q/%q without field", test.err, test.code, test.text)
		}
	}
	field := newConflictFieldError("Invalid conflict action", "action")
	if field == ErrConflictUnsupported || field.Code != CodeConflictUnsupported || field.Field != "action" {
		t.Fatalf("field error = %#v, want fresh safe action error", field)
	}
}
