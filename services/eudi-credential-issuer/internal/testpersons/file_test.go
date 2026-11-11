package testpersons_test

import (
	"context"
	"gbo-demo/eudi-credential-issuer/internal/testpersons"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedPersons(t *testing.T) {
	people, err := testpersons.File("../../../brp-graphql-server/mockdata/personen.json").Load(context.Background())
	if err != nil || len(people) < 2 {
		t.Fatalf("shared fixtures: %v %v", people, err)
	}
	if people[0].BSN != "999991772" || people[0].Name != "Frouke Jansen" {
		t.Fatalf("shared name/BSN: %+v", people[0])
	}
	for _, raw := range []string{`[{"bsn":"999991772","name":"A"},{"bsn":"999991772","name":"B"}]`, `[{"bsn":"wrong","name":"A"}]`, `[{"bsn":"999991772"}]`, `bad`} {
		path := filepath.Join(t.TempDir(), "persons.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := testpersons.File(path).Load(context.Background()); err == nil {
			t.Fatal("invalid persons file accepted")
		}
	}
}
