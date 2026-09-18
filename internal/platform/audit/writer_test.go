package audit_test

import (
	"strings"
	"testing"

	"malaka/internal/platform/audit"
)

func TestComputeHash(t *testing.T) {
	prev := audit.GenesisHash
	action := "CREATE"
	entityType := "incoming_letter"
	entityID := "018f3a5b-7c82-7def-89ab-cdef01234568"
	oldJSON := []byte(`{}`)
	newJSON := []byte(`{"title":"Surat Keputusan"}`)
	ts := "2026-09-07T09:00:00Z"

	h1 := audit.ComputeHash(prev, action, entityType, entityID, oldJSON, newJSON, ts)
	if len(h1) != 64 {
		t.Fatalf("expected 64 char hex hash, got %d", len(h1))
	}

	h2 := audit.ComputeHash(prev, action, entityType, entityID, oldJSON, newJSON, ts)
	if h1 != h2 {
		t.Fatalf("expected deterministic hash output, got %s != %s", h1, h2)
	}

	tamperedJSON := []byte(`{"title":"Surat Keputusan Palsu"}`)
	hTampered := audit.ComputeHash(prev, action, entityType, entityID, oldJSON, tamperedJSON, ts)
	if h1 == hTampered {
		t.Fatalf("expected different hash for modified payload")
	}

	hDifferentTS := audit.ComputeHash(prev, action, entityType, entityID, oldJSON, newJSON, "2026-09-07T09:00:01Z")
	if h1 == hDifferentTS {
		t.Fatalf("expected different hash for modified timestamp")
	}

	if strings.Contains(h1, " ") {
		t.Fatalf("hash must not contain spaces")
	}
}
