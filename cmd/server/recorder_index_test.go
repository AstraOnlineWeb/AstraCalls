package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistedRecordingPathUsesIndexAfterBrokerRestart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WACALLS_RECORDINGS_DIR", root)

	path := filepath.Join(root, safePathPart("sid1"), "call1.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("RIFF-test"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := persistRecordingIndex("sid1", "call1", path); err != nil {
		t.Fatal(err)
	}

	broker := NewBroker()
	got, ok := broker.recordingPath("sid1", "call1")
	if !ok {
		t.Fatal("recordingPath should recover from persisted index")
	}
	if got != path {
		t.Fatalf("expected %q, got %q", path, got)
	}
}

func TestPersistedRecordingPathScansAndBackfillsIndex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WACALLS_RECORDINGS_DIR", root)

	path := filepath.Join(root, safePathPart("sid2"), "call2.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("RIFF-test"), 0o640); err != nil {
		t.Fatal(err)
	}

	broker := NewBroker()
	got, ok := broker.recordingPath("sid2", "call2")
	if !ok {
		t.Fatal("recordingPath should scan recordings dir on memory miss")
	}
	if got != path {
		t.Fatalf("expected %q, got %q", path, got)
	}
	if _, err := os.Stat(recordingIndexPath()); err != nil {
		t.Fatalf("scan fallback should backfill index: %v", err)
	}
}

func TestPersistedRecordingPathDoesNotMatchWrongCallID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WACALLS_RECORDINGS_DIR", root)

	path := filepath.Join(root, safePathPart("sid3"), "different.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("RIFF-test"), 0o640); err != nil {
		t.Fatal(err)
	}

	broker := NewBroker()
	if got, ok := broker.recordingPath("sid3", "missing"); ok {
		t.Fatalf("unexpected recording match: %q", got)
	}
}
