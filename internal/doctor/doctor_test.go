package doctor

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// mockCheck is a test check that can be configured to return any status.
type mockCheck struct {
	BaseCheck
	status   CheckStatus
	fixable  bool
	fixError error
	fixCount int
}

func newMockCheck(name string, status CheckStatus) *mockCheck {
	return &mockCheck{
		BaseCheck: BaseCheck{
			CheckName:        name,
			CheckDescription: "Test check: " + name,
		},
		status: status,
	}
}

func (m *mockCheck) Run(ctx *CheckContext) *CheckResult {
	return &CheckResult{
		Name:    m.CheckName,
		Status:  m.status,
		Message: "mock result",
	}
}

func (m *mockCheck) CanFix() bool {
	return m.fixable
}

func (m *mockCheck) Fix(ctx *CheckContext) error {
	m.fixCount++
	if m.fixError != nil {
		return m.fixError
	}
	// Simulate successful fix by changing status
	m.status = StatusOK
	return nil
}

func TestCheckStatus_String(t *testing.T) {
	tests := []struct {
		status CheckStatus
		want   string
	}{
		{StatusOK, "OK"},
		{StatusWarning, "Warning"},
		{StatusError, "Error"},
		{CheckStatus(99), "Unknown"},
	}

	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("CheckStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestCheckContext_RigPath(t *testing.T) {
	tests := []struct {
		name     string
		ctx      CheckContext
		wantPath string
	}{
		{
			name:     "empty rig name",
			ctx:      CheckContext{TownRoot: "/town"},
			wantPath: "",
		},
		{
			name:     "with rig name",
			ctx:      CheckContext{TownRoot: "/town", RigName: "myrig"},
			wantPath: "/town/myrig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ctx.RigPath()
			if got != tt.wantPath {
				t.Errorf("RigPath() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestNewReport(t *testing.T) {
	r := NewReport()

	if r.Timestamp.IsZero() {
		t.Error("NewReport() should set Timestamp")
	}
	if len(r.Checks) != 0 {
		t.Error("NewReport() should have empty Checks slice")
	}
	if r.Summary.Total != 0 {
		t.Error("NewReport() should have zero Total")
	}
}

func TestReport_Add(t *testing.T) {
	r := NewReport()

	// Add an OK result
	r.Add(&CheckResult{Name: "test1", Status: StatusOK})
	if r.Summary.Total != 1 || r.Summary.OK != 1 {
		t.Errorf("After adding OK: Total=%d, OK=%d", r.Summary.Total, r.Summary.OK)
	}

	// Add a warning
	r.Add(&CheckResult{Name: "test2", Status: StatusWarning})
	if r.Summary.Total != 2 || r.Summary.Warnings != 1 {
		t.Errorf("After adding Warning: Total=%d, Warnings=%d", r.Summary.Total, r.Summary.Warnings)
	}

	// Add an error
	r.Add(&CheckResult{Name: "test3", Status: StatusError})
	if r.Summary.Total != 3 || r.Summary.Errors != 1 {
		t.Errorf("After adding Error: Total=%d, Errors=%d", r.Summary.Total, r.Summary.Errors)
	}
}

func TestReport_HasErrors(t *testing.T) {
	r := NewReport()
	if r.HasErrors() {
		t.Error("Empty report should not have errors")
	}

	r.Add(&CheckResult{Status: StatusOK})
	if r.HasErrors() {
		t.Error("Report with only OK should not have errors")
	}

	r.Add(&CheckResult{Status: StatusWarning})
	if r.HasErrors() {
		t.Error("Report with only OK/Warning should not have errors")
	}

	r.Add(&CheckResult{Status: StatusError})
	if !r.HasErrors() {
		t.Error("Report with Error should have errors")
	}
}

func TestReport_HasWarnings(t *testing.T) {
	r := NewReport()
	if r.HasWarnings() {
		t.Error("Empty report should not have warnings")
	}

	r.Add(&CheckResult{Status: StatusOK})
	if r.HasWarnings() {
		t.Error("Report with only OK should not have warnings")
	}

	r.Add(&CheckResult{Status: StatusWarning})
	if !r.HasWarnings() {
		t.Error("Report with Warning should have warnings")
	}
}

func TestReport_IsHealthy(t *testing.T) {
	tests := []struct {
		name    string
		results []CheckStatus
		want    bool
	}{
		{"empty", nil, true},
		{"all OK", []CheckStatus{StatusOK, StatusOK}, true},
		{"has warning", []CheckStatus{StatusOK, StatusWarning}, false},
		{"has error", []CheckStatus{StatusOK, StatusError}, false},
		{"mixed", []CheckStatus{StatusOK, StatusWarning, StatusError}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReport()
			for _, status := range tt.results {
				r.Add(&CheckResult{Status: status})
			}
			if got := r.IsHealthy(); got != tt.want {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReport_Print(t *testing.T) {
	r := NewReport()
	r.Add(&CheckResult{
		Name:    "TestCheck",
		Status:  StatusOK,
		Message: "All good",
	})
	r.Add(&CheckResult{
		Name:    "WarningCheck",
		Status:  StatusWarning,
		Message: "Minor issue",
		FixHint: "Run fix command",
	})

	var buf bytes.Buffer
	r.Print(&buf, false, 0)

	output := buf.String()
	if output == "" {
		t.Error("Print() should produce output")
	}
	// Basic checks that key elements are present
	if !bytes.Contains(buf.Bytes(), []byte("TestCheck")) {
		t.Error("Output should contain check name")
	}
	// New summary format: "✓ N passed  ⚠ N warnings  ✖ N failed"
	if !bytes.Contains(buf.Bytes(), []byte("1 passed")) {
		t.Error("Output should contain summary with passed count")
	}
	if !bytes.Contains(buf.Bytes(), []byte("1 warnings")) {
		t.Error("Output should contain summary with warnings count")
	}
}

func TestNewDoctor(t *testing.T) {
	d := NewDoctor()
	if d == nil {
		t.Fatal("NewDoctor() returned nil")
	}
	if len(d.Checks()) != 0 {
		t.Error("NewDoctor() should have no checks registered")
	}
}

func TestDoctor_Register(t *testing.T) {
	d := NewDoctor()

	check1 := newMockCheck("check1", StatusOK)
	check2 := newMockCheck("check2", StatusOK)

	d.Register(check1)
	if len(d.Checks()) != 1 {
		t.Error("Register() should add one check")
	}

	d.Register(check2)
	if len(d.Checks()) != 2 {
		t.Error("Register() should add another check")
	}
}

func TestDoctor_RegisterAll(t *testing.T) {
	d := NewDoctor()

	check1 := newMockCheck("check1", StatusOK)
	check2 := newMockCheck("check2", StatusOK)
	check3 := newMockCheck("check3", StatusOK)

	d.RegisterAll(check1, check2, check3)
	if len(d.Checks()) != 3 {
		t.Errorf("RegisterAll() should add 3 checks, got %d", len(d.Checks()))
	}
}

func TestDoctor_Run(t *testing.T) {
	d := NewDoctor()
	d.Register(newMockCheck("ok", StatusOK))
	d.Register(newMockCheck("warn", StatusWarning))
	d.Register(newMockCheck("error", StatusError))

	ctx := &CheckContext{TownRoot: "/test"}
	report := d.Run(ctx)

	if report.Summary.Total != 3 {
		t.Errorf("Run() Total = %d, want 3", report.Summary.Total)
	}
	if report.Summary.OK != 1 {
		t.Errorf("Run() OK = %d, want 1", report.Summary.OK)
	}
	if report.Summary.Warnings != 1 {
		t.Errorf("Run() Warnings = %d, want 1", report.Summary.Warnings)
	}
	if report.Summary.Errors != 1 {
		t.Errorf("Run() Errors = %d, want 1", report.Summary.Errors)
	}
}

func TestDoctor_Fix(t *testing.T) {
	d := NewDoctor()

	okCheck := newMockCheck("ok", StatusOK)
	d.Register(okCheck)

	fixableCheck := newMockCheck("fixable", StatusError)
	fixableCheck.fixable = true
	d.Register(fixableCheck)

	unfixableCheck := newMockCheck("unfixable", StatusError)
	unfixableCheck.fixable = false
	d.Register(unfixableCheck)

	ctx := &CheckContext{TownRoot: "/test"}
	report := d.Fix(ctx)

	// OK check should remain OK
	if report.Checks[0].Status != StatusOK {
		t.Error("OK check should remain OK")
	}

	// Fixable check should be fixed
	if fixableCheck.fixCount != 1 {
		t.Error("Fixable check should have Fix() called once")
	}
	if report.Checks[1].Status != StatusOK {
		t.Error("Fixable check should be OK after fix")
	}

	// Unfixable check should remain error
	if unfixableCheck.fixCount != 0 {
		t.Error("Unfixable check should not have Fix() called")
	}
	if report.Checks[2].Status != StatusError {
		t.Error("Unfixable check should remain Error")
	}
}

func TestDoctor_FixStreamingReportsFixFailure(t *testing.T) {
	d := NewDoctor()
	check := newMockCheck("broken-fix", StatusWarning)
	check.fixable = true
	check.fixError = fmt.Errorf("permission denied")
	d.Register(check)

	var buf bytes.Buffer
	report := d.FixStreaming(&CheckContext{TownRoot: "/test"}, &buf, 0)
	report.PrintSummaryOnly(&buf, false, 0)
	if got := buf.String(); !strings.Contains(got, "Fix failed: permission denied") {
		t.Fatalf("streaming doctor hid fix error:\n%s", got)
	}
}

func TestBaseCheck(t *testing.T) {
	b := &BaseCheck{
		CheckName:        "test",
		CheckDescription: "Test description",
	}

	if b.Name() != "test" {
		t.Errorf("Name() = %q, want %q", b.Name(), "test")
	}
	if b.Description() != "Test description" {
		t.Errorf("Description() = %q, want %q", b.Description(), "Test description")
	}
	if b.CanFix() {
		t.Error("BaseCheck.CanFix() should return false")
	}
	if err := b.Fix(nil); err != ErrCannotFix {
		t.Errorf("BaseCheck.Fix() should return ErrCannotFix, got %v", err)
	}
}

// panicCheck is a test check whose Fix panics.
type panicCheck struct {
	FixableCheck
}

func (p *panicCheck) Run(ctx *CheckContext) *CheckResult {
	return &CheckResult{Name: p.CheckName, Status: StatusError, Message: "needs fix"}
}

func (p *panicCheck) Fix(ctx *CheckContext) error {
	panic("dolt nil pointer dereference")
}

func TestSafeFixCheck_RecoversPanic(t *testing.T) {
	check := &panicCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "panic-check",
				CheckDescription: "A check that panics during fix",
			},
		},
	}

	err := safeFixCheck(check, &CheckContext{TownRoot: "/test"})
	if err == nil {
		t.Fatal("safeFixCheck should return error when Fix panics")
	}
	if !strings.Contains(err.Error(), "fix panicked") {
		t.Errorf("error should mention 'fix panicked', got: %v", err)
	}
	if !strings.Contains(err.Error(), "nil pointer") {
		t.Errorf("error should contain panic message, got: %v", err)
	}
}

func TestSafeFixCheck_NormalError(t *testing.T) {
	check := newMockCheck("failing-fix", StatusError)
	check.fixable = true
	check.fixError = fmt.Errorf("some fix error")

	err := safeFixCheck(check, &CheckContext{TownRoot: "/test"})
	if err == nil {
		t.Fatal("safeFixCheck should return the Fix error")
	}
	if err.Error() != "some fix error" {
		t.Errorf("expected 'some fix error', got: %v", err)
	}
}

func TestSafeFixCheck_Success(t *testing.T) {
	check := newMockCheck("good-fix", StatusError)
	check.fixable = true

	err := safeFixCheck(check, &CheckContext{TownRoot: "/test"})
	if err != nil {
		t.Fatalf("safeFixCheck should return nil on success, got: %v", err)
	}
}

func TestFixableCheck(t *testing.T) {
	f := &FixableCheck{
		BaseCheck: BaseCheck{
			CheckName:        "fixable",
			CheckDescription: "Fixable check",
		},
	}

	if !f.CanFix() {
		t.Error("FixableCheck.CanFix() should return true")
	}
}
