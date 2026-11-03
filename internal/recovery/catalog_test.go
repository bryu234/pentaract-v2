package recovery

import (
	"bytes"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"testing"
)

func TestCatalogAuthentication(t *testing.T) {
	key, _ := cryptostore.RandomKey()
	c := Catalog{Format: 1, StorageID: "test", Revision: 1, Nodes: []byte(`[]`), Chunks: []byte(`[]`)}
	b, e := Encode(c, key)
	if e != nil {
		t.Fatal(e)
	}
	out, e := Decode(b, key)
	if e != nil || out.StorageID != c.StorageID {
		t.Fatal("roundtrip", e)
	}
	wrong, _ := cryptostore.RandomKey()
	if _, e = Decode(b, wrong); e == nil {
		t.Fatal("accepted wrong key")
	}
	b[len(b)-1] ^= 1
	if _, e = Decode(b, key); e == nil {
		t.Fatal("accepted corruption")
	}
	if bytes.Contains(b, []byte(`storage_id`)) {
		t.Fatal("plaintext metadata")
	}
}
