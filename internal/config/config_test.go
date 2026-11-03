package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKeySources(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	p := filepath.Join(t.TempDir(), "key")
	t.Setenv("PENTARACT_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	if _, e := LoadMasterKey(p); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p, key, 0600)
	if _, e := LoadMasterKey(p); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p, bytes.Repeat([]byte{8}, 32), 0600)
	if _, e := LoadMasterKey(p); e == nil {
		t.Fatal("conflicting key accepted")
	}
	t.Setenv("PENTARACT_MASTER_KEY", "")
	os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0600)
	if _, e := LoadMasterKey(p); e != nil {
		t.Fatal(e)
	}
}
