package memstore_test

import (
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) storetest.Store { return memstore.New() })
}
