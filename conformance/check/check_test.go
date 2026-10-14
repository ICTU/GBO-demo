package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a repository with one Go test file, an ID list and the
// matrix, and runs the check on it.
func fixture(t *testing.T, matrix string) (code int, stdout, stderr string) {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services/asip/verify_test.go", "package asip\n\nfunc TestVerify(t *testing.T) {}\n\nfunc TestRetrieve(t *testing.T) {}\n")
	write("conformance/ids.txt", "# IDs only\nREQ-ASIP-6.1.1.1-01\nREQ-ASIP-6.1.1.1-02\nREQ-AZSP-6.1.3.1-02\nREQ-ASIP-6.2.1-01\nREQ-ASIP-6.2.1-02\n")
	write("conformance/matrix.yaml", matrix)
	var out, errs bytes.Buffer
	code = run(filepath.Join(root, "conformance/matrix.yaml"), root, &out, &errs)
	return code, out.String(), errs.String()
}

const valid = `spec: ETSI TS 119 478 V1.1.1
ids: ids.txt
groups:
  - prefix: REQ-ASIP-6.2.
    status: not-applicable
    reason: The ASIP offers the HTTP interface, not ISO 15000.
requirements:
  REQ-ASIP-6.1.1.1-01:
    status: passed
    tests:
      - services/asip:TestVerify
  REQ-ASIP-6.1.1.1-02:
    status: passed
    tests:
      - services/asip:TestVerify/the request body
      - services/asip:TestRetrieve
  REQ-AZSP-6.1.3.1-02:
    status: failing
    reason: The user is identified by a test-person chooser.
    slice: "#336 slice 7"
`

// replace changes one line of the valid matrix.
func replace(old, new string) string {
	if !strings.Contains(valid, old) {
		panic(old)
	}
	return strings.Replace(valid, old, new, 1)
}

func expectFailure(t *testing.T, matrix string, want ...string) {
	t.Helper()
	code, _, stderr := fixture(t, matrix)
	if code == 0 {
		t.Fatalf("the check passed; want it to fail with %q", want)
	}
	for _, w := range want {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr does not name %q:\n%s", w, stderr)
		}
	}
}

// K1: an ID of the list without an entry or a group.
func TestAnIDWithoutAnEntryFails(t *testing.T) {
	expectFailure(t, replace("  REQ-ASIP-6.1.1.1-02:\n    status: passed\n    tests:\n      - services/asip:TestVerify/the request body\n      - services/asip:TestRetrieve\n", ""),
		"REQ-ASIP-6.1.1.1-02: no entry and no group")
	expectFailure(t, replace("  - prefix: REQ-ASIP-6.2.\n    status: not-applicable\n    reason: The ASIP offers the HTTP interface, not ISO 15000.\n", ""),
		"REQ-ASIP-6.2.1-01: no entry and no group", "REQ-ASIP-6.2.1-02: no entry and no group")
}

// K2: a test that does not exist, in a directory or a function.
func TestATestThatDoesNotExistFails(t *testing.T) {
	expectFailure(t, replace("services/asip:TestRetrieve", "services/asip:TestRetreive"), "test services/asip:TestRetreive does not exist")
	expectFailure(t, replace("services/asip:TestRetrieve", "services/nope:TestRetrieve"), "test services/nope:TestRetrieve does not exist")
	expectFailure(t, replace("services/asip:TestRetrieve", "TestRetrieve"), `test "TestRetrieve" is not <dir>:<TestName>`)
}

// K3: an unknown status, or a status without what it requires.
func TestAStatusWithoutWhatItRequiresFails(t *testing.T) {
	expectFailure(t, replace("status: not-applicable", "status: n/a"), `status "n/a" is not one of`)
	expectFailure(t, replace("    reason: The ASIP offers the HTTP interface, not ISO 15000.\n", ""), "group REQ-ASIP-6.2.: not-applicable without a reason")
	expectFailure(t, replace("    reason: The user is identified by a test-person chooser.\n", ""), "REQ-AZSP-6.1.3.1-02: failing without a reason")
	expectFailure(t, replace("    status: passed\n    tests:\n      - services/asip:TestVerify\n", "    status: passed\n"), "REQ-ASIP-6.1.1.1-01: passed without tests")
	expectFailure(t, replace("    status: passed\n    tests:\n      - services/asip:TestVerify\n", "    status: stub\n    tests:\n      - services/asip:TestVerify\n"), "REQ-ASIP-6.1.1.1-01: stub without a reason")
	expectFailure(t, replace("    status: passed\n    tests:\n      - services/asip:TestVerify\n", "    status: passed\n    test: services/asip:TestVerify\n"), "field test not found")
}

// K4: an entry or a group for an ID that is not in the list (a typo), and
// an ID given twice.
func TestAnIDThatIsNotInTheListFails(t *testing.T) {
	expectFailure(t, replace("  REQ-ASIP-6.1.1.1-01:", "  REQ-ASIP-6.1.1.1-01:\n    status: passed\n    tests:\n      - services/asip:TestVerify\n  REQ-ASIP-6.1.1.1-10:"), "REQ-ASIP-6.1.1.1-10: not a requirement ID of the list")
	expectFailure(t, replace("prefix: REQ-ASIP-6.2.", "prefix: REQ-ASIP-6.3."), "group REQ-ASIP-6.3.: covers no requirement ID of the list")
	expectFailure(t, replace("  REQ-ASIP-6.1.1.1-02:", "  REQ-ASIP-6.1.1.1-01:"), "already defined")
}

// K5: a complete, valid matrix passes with a summary per role.
func TestAValidMatrixPassesWithASummaryPerRole(t *testing.T) {
	code, stdout, stderr := fixture(t, valid)
	if code != 0 {
		t.Fatalf("the check failed:\n%s", stderr)
	}
	for _, want := range []string{
		"ETSI TS 119 478 V1.1.1: 5 requirements",
		"ASIP         2     0               2        0",
		"AZSP         0     0               0        1",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary lacks %q:\n%s", want, stdout)
		}
	}
	// An entry overrides its group.
	m := replace("requirements:\n", "requirements:\n  REQ-ASIP-6.2.1-01:\n    status: passed\n    tests:\n      - services/asip:TestVerify\n")
	if code, stdout, _ := fixture(t, m); code != 0 || !strings.Contains(stdout, "ASIP         3     0               1        0") {
		t.Errorf("an entry did not override its group:\n%s", stdout)
	}
}

// K6: failing without the slice that will meet it; a slice on anything else.
func TestFailingWithoutASliceFails(t *testing.T) {
	expectFailure(t, replace("    slice: \"#336 slice 7\"\n", ""), "REQ-AZSP-6.1.3.1-02: failing without the slice that will meet it")
	expectFailure(t, replace("      - services/asip:TestVerify\n  REQ-ASIP-6.1.1.1-02:", "      - services/asip:TestVerify\n    slice: \"#336 slice 7\"\n  REQ-ASIP-6.1.1.1-02:"), "REQ-ASIP-6.1.1.1-01: a slice is only for failing")
}
