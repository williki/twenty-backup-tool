package main

import "testing"

// TestIncludeObjectRecords pins the backup/restore inclusion contract. The
// regression it guards: workflowVersion is a system object whose records hold a
// workflow's trigger+steps; excluding it restores empty workflow shells.
func TestIncludeObjectRecords(t *testing.T) {
	cases := []struct {
		name string
		obj  ObjectMetadata
		want bool
	}{
		{"custom active", ObjectMetadata{NameSingular: "company", IsActive: true}, true},
		{"custom inactive", ObjectMetadata{NameSingular: "company", IsActive: false}, false},
		{"standard non-system", ObjectMetadata{NameSingular: "workflow", IsActive: true}, true},
		{"allowlisted system", ObjectMetadata{NameSingular: "workflowVersion", IsSystem: true, IsActive: true}, true},
		{"other system", ObjectMetadata{NameSingular: "workflowRun", IsSystem: true, IsActive: true}, false},
		{"allowlisted but inactive", ObjectMetadata{NameSingular: "workflowVersion", IsSystem: true, IsActive: false}, false},
	}
	for _, c := range cases {
		if got := includeObjectRecords(c.obj); got != c.want {
			t.Errorf("%s: includeObjectRecords(%q system=%v active=%v) = %v, want %v",
				c.name, c.obj.NameSingular, c.obj.IsSystem, c.obj.IsActive, got, c.want)
		}
	}
}

// TestWorkflowIsActive guards which workflows the activation pass targets:
// only those whose statuses contain "ACTIVE".
func TestWorkflowIsActive(t *testing.T) {
	cases := []struct {
		name string
		rec  RecordPayload
		want bool
	}{
		{"active", RecordPayload{"statuses": []any{"ACTIVE"}}, true},
		{"draft", RecordPayload{"statuses": []any{"DRAFT"}}, false},
		{"deactivated", RecordPayload{"statuses": []any{"DEACTIVATED"}}, false},
		{"mixed contains active", RecordPayload{"statuses": []any{"DRAFT", "ACTIVE"}}, true},
		{"empty", RecordPayload{"statuses": []any{}}, false},
		{"missing", RecordPayload{}, false},
		{"null", RecordPayload{"statuses": nil}, false},
	}
	for _, c := range cases {
		if got := workflowIsActive(c.rec); got != c.want {
			t.Errorf("%s: workflowIsActive(%v) = %v, want %v", c.name, c.rec["statuses"], got, c.want)
		}
	}
}
