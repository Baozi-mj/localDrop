package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Movies", "a.mp4"), []byte("video"))
	writeTestFile(t, filepath.Join(dir, "Movies", "b.mkv"), []byte("video2"))
	writeTestFile(t, filepath.Join(dir, "笔记.txt"), []byte("hello"))
	writeTestFile(t, filepath.Join(dir, ".hidden"), []byte("secret"))
	writeTestFile(t, filepath.Join(dir, "Movies", ".DS_Store"), []byte("junk"))

	files, warnings, err := collectFiles([]string{filepath.Join(dir, "Movies")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].RelPath != "Movies/a.mp4" || files[1].RelPath != "Movies/b.mkv" {
		t.Fatalf("rel paths wrong: %q %q", files[0].RelPath, files[1].RelPath)
	}

	// duplicate selection dedupes rel paths
	files2, _, err := collectFiles([]string{
		filepath.Join(dir, "笔记.txt"),
		filepath.Join(dir, "笔记.txt"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files2) != 2 {
		t.Fatalf("got %d files, want 2", len(files2))
	}
	if files2[0].RelPath == files2[1].RelPath {
		t.Fatalf("dedupe failed: %q %q", files2[0].RelPath, files2[1].RelPath)
	}

	// relative path rejected
	if _, _, err := collectFiles([]string{"relative/path"}, nil); err == nil {
		t.Fatal("relative path should be rejected")
	}
}

func TestStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))
	store := NewStore("tok")

	sess, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Code) != 10 {
		t.Fatalf("code %q", sess.Code)
	}
	if store.ByCode(sess.Code) == nil {
		t.Fatal("ByCode should find active session")
	}
	if _, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", false); err == nil {
		t.Fatal("second create should fail without replace")
	}
	sess2, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", true)
	if err != nil {
		t.Fatalf("replace failed: %v", err)
	}
	if store.ByCode(sess.Code) != nil {
		t.Fatal("old code should be gone after replace")
	}
	if store.Active() == nil || store.Active().Code != sess2.Code {
		t.Fatal("new session should be active")
	}
	store.Active().stop()
	if store.Active() != nil {
		t.Fatal("stopped session should not be active")
	}
}

func TestAddRemoveFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))
	writeTestFile(t, filepath.Join(dir, "b.txt"), []byte("bbb"))
	store := NewStore("tok")
	sess, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	n, err := sess.addFiles([]string{filepath.Join(dir, "b.txt")})
	if err != nil || n != 1 {
		t.Fatalf("addFiles: n=%d err=%v", n, err)
	}
	if len(sess.filesSnapshot()) != 2 {
		t.Fatalf("want 2 files, got %d", len(sess.filesSnapshot()))
	}
	// re-adding the same file dedupes against existing rel paths
	if _, err := sess.addFiles([]string{filepath.Join(dir, "a.txt")}); err != nil {
		t.Fatal(err)
	}
	files := sess.filesSnapshot()
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	if files[0].RelPath == files[1].RelPath {
		t.Fatalf("dedupe failed: %q %q", files[0].RelPath, files[1].RelPath)
	}
	if removed := sess.removeFiles([]int{1}); removed != 1 {
		t.Fatalf("removeFiles removed %d", removed)
	}
	if len(sess.filesSnapshot()) != 2 {
		t.Fatalf("want 2 files after remove")
	}
}

func TestReceivers(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))
	store := NewStore("tok")
	sess, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	count, isNew := sess.touchReceiver("1.2.3.4", "UA1")
	if count != 1 || !isNew {
		t.Fatalf("count=%d isNew=%v", count, isNew)
	}
	count, isNew = sess.touchReceiver("1.2.3.4", "UA1")
	if count != 1 || isNew {
		t.Fatalf("second touch: count=%d isNew=%v", count, isNew)
	}
	if count, _ := sess.touchReceiver("5.6.7.8", ""); count != 2 {
		t.Fatalf("count=%d", count)
	}
	sess.mu.Lock()
	sess.receivers["1.2.3.4"].LastSeen = time.Now().Add(-time.Minute)
	sess.mu.Unlock()
	if got := sess.gcReceivers(); got != 1 {
		t.Fatalf("gcReceivers=%d, want 1", got)
	}
}

func TestSnapshotReceiverHidesPrivate(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), []byte("aaa"))
	store := NewStore("tok")
	sess, err := store.Create([]string{filepath.Join(dir, "a.txt")}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	sess.touchReceiver("1.2.3.4", "UA1")
	admin := sess.snapshot(true)
	if len(admin.Receivers) != 1 {
		t.Fatalf("admin snapshot should include receivers")
	}
	recv := sess.snapshot(false)
	if len(recv.Receivers) != 0 {
		t.Fatalf("receiver snapshot must not include receivers")
	}
	if recv.Files[0].Name != "a.txt" {
		t.Fatalf("files missing in receiver snapshot")
	}
}
