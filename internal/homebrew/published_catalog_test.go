package homebrew

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// docs/ is this fork's built-in catalog. It must verify with the pinned key,
// every package must be present, and it must not be close to expiring.
func TestPublishedForkCatalog(t *testing.T) {
	docs := filepath.Join("..", "..", "docs")
	if _, e := os.Stat(filepath.Join(docs, "catalog")); os.IsNotExist(e) {
		t.Skip("catalog site is not part of this copy (for example Station Mini Root's bundle)")
	}
	for _, name := range []string{"v1.json", "v2.json", "v3.json"} {
		raw, e := os.ReadFile(filepath.Join(docs, "catalog", name))
		if e != nil {
			t.Fatal(e)
		}
		c, e := VerifyCatalog(raw, ForkCatalogKey, 0)
		if e != nil {
			t.Fatal(name, e)
		}
		if time.Until(c.Expires) < 30*24*time.Hour {
			t.Fatalf("%s expires on %s: re-export the catalog", name, c.Expires.Format("2006-01-02"))
		}
		for _, entry := range c.Entries {
			b, e := os.ReadFile(filepath.Join(docs, "packages", entry.SHA256+".aliceplugin"))
			if e != nil || Digest(b) != entry.SHA256 || len(b) != entry.Bytes {
				t.Fatal("missing or altered package", entry.Manifest.ID, e)
			}
			p, e := Parse(b)
			if e != nil || ValidateModule(p) != nil || p.Manifest.ID != entry.Manifest.ID {
				t.Fatal("invalid published package", entry.Manifest.ID, e)
			}
		}
	}
	key, e := os.ReadFile(filepath.Join(docs, "public-key.txt"))
	if e != nil || string(key) != ForkCatalogKey+"\n" && string(key) != ForkCatalogKey+"\r\n" {
		t.Fatal("docs/public-key.txt does not match ForkCatalogKey")
	}
}
