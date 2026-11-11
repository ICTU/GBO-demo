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
	return fixtureWith(t, "# IDs only\nREQ-ASIP-6.1.1.1-01\nREQ-ASIP-6.1.1.1-02\nREQ-AZSP-6.1.3.1-02\nREQ-ASIP-6.2.1-01\nREQ-ASIP-6.2.1-02\n", matrix)
}

// fixtureWith is fixture with an ID list of its own.
func fixtureWith(t *testing.T, ids, matrix string) (code int, stdout, stderr string) {
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
	write("services/asip/verify_test.go", "package asip\n\nfunc TestVerify(t *testing.T) {\n\tt.Run(\"the request body\", func(t *testing.T) {})\n}\n\nfunc TestRetrieve(t *testing.T) {}\n")
	write("conformance/ids.txt", ids)
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
	expectFailureWith(t, "", matrix, want...)
}

// expectFailureWith is expectFailure with an ID list of its own; "" is the
// default list.
func expectFailureWith(t *testing.T, ids, matrix string, want ...string) {
	t.Helper()
	var code int
	var stderr string
	if ids == "" {
		code, _, stderr = fixture(t, matrix)
	} else {
		code, _, stderr = fixtureWith(t, ids, matrix)
	}
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
	expectFailure(t, replace("TestVerify/the request body", "TestVerify/this subtest does not exist"), "subtest services/asip:TestVerify/this subtest does not exist does not exist")
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
		"ASIP         2     0               2        0          0",
		"AZSP         0     0               0        1          0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary lacks %q:\n%s", want, stdout)
		}
	}
	// An entry overrides its group.
	m := replace("requirements:\n", "requirements:\n  REQ-ASIP-6.2.1-01:\n    status: passed\n    tests:\n      - services/asip:TestVerify\n")
	if code, stdout, _ := fixture(t, m); code != 0 || !strings.Contains(stdout, "ASIP         3     0               1        0          0") {
		t.Errorf("an entry did not override its group:\n%s", stdout)
	}
}

// K6: failing without the slice that will meet it; a slice on anything else.
func TestFailingWithoutASliceFails(t *testing.T) {
	expectFailure(t, replace("    slice: \"#336 slice 7\"\n", ""), "REQ-AZSP-6.1.3.1-02: failing without the slice that will meet it")
	expectFailure(t, replace("      - services/asip:TestVerify\n  REQ-ASIP-6.1.1.1-02:", "      - services/asip:TestVerify\n    slice: \"#336 slice 7\"\n  REQ-ASIP-6.1.1.1-02:"), "REQ-ASIP-6.1.1.1-01: a slice is only for failing")
}

// rolesIDs is an ID list in the OpenID form: an ID and its role per line.
const rolesIDs = `# IDs and roles
OID4VCI-8.2-01 issuer
OID4VCI-8.2-02 wallet
OID4VCI-8.3-01 wallet
OID4VCI-6.1-01 as
`

const profiled = `spec: OpenID4VCI 1.0
ids: ids.txt
profiles: [nl, eudi, wallet]
groups:
  - role: wallet
    status: not-applicable
    reason: A requirement on the wallet.
requirements:
  OID4VCI-8.2-01:
    status: passed
    tests:
      - services/asip:TestVerify
    profiles:
      nl:
        status: deviation
        reason: The NL Wallet sends no key attestation.
  OID4VCI-6.1-01:
    status: passed
    tests:
      - services/asip:TestRetrieve
`

// K7: the role of an ID comes from the ID list when the line names one, and
// from the ID otherwise; a line with more than two fields is an error.
func TestTheRoleComesFromTheIDList(t *testing.T) {
	code, stdout, stderr := fixtureWith(t, rolesIDs, `spec: OpenID4VCI 1.0
ids: ids.txt
groups:
  - role: wallet
    status: not-applicable
    reason: A requirement on the wallet.
requirements:
  OID4VCI-8.2-01:
    status: passed
    tests:
      - services/asip:TestVerify
  OID4VCI-6.1-01:
    status: passed
    tests:
      - services/asip:TestRetrieve
`)
	if code != 0 {
		t.Fatalf("the check failed:\n%s", stderr)
	}
	for _, want := range []string{
		"as           1     0               0        0          0",
		"issuer       1     0               0        0          0",
		"wallet       0     0               2        0          0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary lacks %q:\n%s", want, stdout)
		}
	}
	expectFailureWith(t, rolesIDs+"OID4VCI-8.4-01 issuer extra\n", profiled, "OID4VCI-8.4-01 issuer extra: more than an ID and a role")
}

// K8: a group by role covers the IDs of that role wherever they are; an
// entry still overrides it; a group without prefix or role, or one that
// covers nothing, is an error.
func TestAGroupByRole(t *testing.T) {
	m := strings.Replace(profiled, "requirements:\n", "requirements:\n  OID4VCI-8.3-01:\n    status: passed\n    tests:\n      - services/asip:TestVerify\n", 1)
	code, stdout, stderr := fixtureWith(t, rolesIDs, m)
	if code != 0 || !strings.Contains(stdout, "eudi     wallet       1     0               1        0          0") {
		t.Errorf("an entry did not override the role group (%d):\n%s%s", code, stdout, stderr)
	}
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "role: wallet", "role: verifier", 1), "group role verifier: covers no requirement ID of the list")
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "  - role: wallet\n", "  - status: not-applicable\n    reason: x\n  - role: wallet\n", 1), "a group needs a prefix or a role")
}

// K9: deviation needs a reason and may name tests.
func TestADeviationNeedsAReason(t *testing.T) {
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "        reason: The NL Wallet sends no key attestation.\n", "", 1), "OID4VCI-8.2-01 profile nl: deviation without a reason")
	withTests := strings.Replace(profiled, "        reason: The NL Wallet sends no key attestation.\n", "        reason: The NL Wallet sends no key attestation.\n        tests:\n          - services/asip:TestRetrieve\n", 1)
	if code, _, stderr := fixtureWith(t, rolesIDs, withTests); code != 0 {
		t.Errorf("a deviation with tests failed:\n%s", stderr)
	}
}

// K10: a status per profile follows the rules of a status, names a profile
// of the matrix, needs the matrix to name its profiles, and does not nest.
func TestAStatusPerProfile(t *testing.T) {
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "      nl:\n", "      ios:\n", 1), `OID4VCI-8.2-01 profile ios: not a profile of the matrix`)
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "profiles: [nl, eudi, wallet]\n", "", 1), "OID4VCI-8.2-01: a status per profile, but the matrix names no profiles")
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "        status: deviation\n        reason: The NL Wallet sends no key attestation.\n", "        status: passed\n", 1), "OID4VCI-8.2-01 profile nl: passed without tests")
	expectFailureWith(t, rolesIDs, strings.Replace(profiled, "        reason: The NL Wallet sends no key attestation.\n", "        reason: The NL Wallet sends no key attestation.\n        profiles:\n          eudi:\n            status: passed\n", 1), "field profiles not found")
}

// K11: with profiles the summary counts per profile and role; a status per
// profile counts only for its profile.
func TestTheSummaryPerProfile(t *testing.T) {
	code, stdout, stderr := fixtureWith(t, rolesIDs, profiled)
	if code != 0 {
		t.Fatalf("the check failed:\n%s", stderr)
	}
	for _, want := range []string{
		"OpenID4VCI 1.0: 4 requirements",
		"profile  role    passed  stub  not-applicable  failing  deviation",
		"eudi     issuer       1     0               0        0          0",
		"nl       issuer       0     0               0        0          1",
		"nl       as           1     0               0        0          0",
		"wallet   wallet       0     0               2        0          0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary lacks %q:\n%s", want, stdout)
		}
	}
}
