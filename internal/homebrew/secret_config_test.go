package homebrew

import (
	"encoding/json"
	"strings"
	"testing"
)

func secretManifest() Manifest {
	m := manifest("1.0.0")
	m.Config = append(m.Config, Field{Key: "api-key", Label: Text{EN: "API key"}, Type: "secret", Default: "", MaxLength: 64, Help: &Text{EN: "From the dashboard"}, Link: "https://example.com/keys"})
	return m
}

func TestSecretFieldSchema(t *testing.T) {
	if e := ValidateManifest(secretManifest()); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*Field){
		"default":   func(f *Field) { f.Default = "spak_shipped" },
		"no limit":  func(f *Field) { f.MaxLength = 0 },
		"http link": func(f *Field) { f.Link = "http://example.com" },
	} {
		m := secretManifest()
		change(&m.Config[1])
		if ValidateManifest(m) == nil {
			t.Fatalf("%s: invalid secret field accepted", name)
		}
	}
	for _, v := range []any{"with space", "line\nbreak", strings.Repeat("k", 65), float64(1)} {
		if _, e := Config(secretManifest(), map[string]any{"api-key": v}); e == nil {
			t.Fatalf("secret value %q accepted", v)
		}
	}
}

func TestSecretIsKeptAndNeverSnapshotted(t *testing.T) {
	m := manager(t)
	p, e := Parse(packageBytes(t, secretManifest(), minimalWasm(false), ""))
	if e != nil {
		t.Fatal(e)
	}
	staged, e := m.Stage(p.Raw, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Install(staged["ticket"].(string), nil, true); e != nil {
		t.Fatal(e)
	}
	id := p.Manifest.ID
	stored := func() any { m.mu.Lock(); defer m.mu.Unlock(); return m.state.Plugins[id].Config["api-key"] }
	snapshot := func() (string, *Installed) {
		s := m.Snapshot()
		raw, _ := json.Marshal(s)
		return string(raw), s["plugins"].([]*Installed)[0]
	}

	if e = m.SetConfig(id, map[string]any{"brightness": float64(30), "api-key": "spak_test123"}); e != nil {
		t.Fatal(e)
	}
	raw, plugin := snapshot()
	if strings.Contains(raw, "spak_test123") || !plugin.SecretsSet["api-key"] || plugin.Config["api-key"] != "" {
		t.Fatalf("snapshot leaks or hides the secret state: %s", raw)
	}
	// The page sends back what it got: no key, or null. Both keep the secret.
	if e = m.SetConfig(id, map[string]any{"brightness": float64(40)}); e != nil {
		t.Fatal(e)
	}
	if e = m.SetConfig(id, map[string]any{"brightness": float64(41), "api-key": nil}); e != nil {
		t.Fatal(e)
	}
	if stored() != "spak_test123" {
		t.Fatalf("secret lost: %v", stored())
	}
	if e = m.SetConfig(id, map[string]any{"api-key": ""}); e != nil {
		t.Fatal(e)
	}
	if _, plugin = snapshot(); stored() != "" || plugin.SecretsSet["api-key"] {
		t.Fatal("secret was not cleared")
	}
}
