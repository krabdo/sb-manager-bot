package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(context.Background(), filepath.Join(t.TempDir(), "manager.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func testAccount(id int64, forum string) Account {
	return Account{TelegramID: id, ChatID: id, ForumUserID: forum, CookieCipher: []byte("cipher"), Status: StatusActive, NextPollAt: time.Unix(100, 0)}
}

func TestStoreMigrationConcurrentAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	s, e := OpenStore(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e = s.Bind(ctx, testAccount(1, "1777"), 500, []string{"old"}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if e := s.AddSeen(ctx, 1, fmt.Sprintf("n-%d", i)); e != nil {
				t.Errorf("concurrent write: %v", e)
			}
		}(i)
	}
	wg.Wait()
	s.Close()
	s, e = OpenStore(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	seen, e := s.IsSeen(ctx, 1, "old")
	if e != nil || !seen {
		t.Fatalf("state did not survive restart: %v %v", seen, e)
	}
}

func TestStoreSeenTrimAndUnbindCascade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, e := s.Bind(ctx, testAccount(2, "22"), 500, nil); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2060; i++ {
		if e := s.AddSeen(ctx, 2, fmt.Sprintf("n-%04d", i)); e != nil {
			t.Fatal(e)
		}
	}
	n, e := s.SeenCount(ctx, 2)
	if e != nil || n != 2048 {
		t.Fatalf("seen count=%d err=%v", n, e)
	}
	if e = s.Unbind(ctx, 2); e != nil {
		t.Fatal(e)
	}
	n, e = s.SeenCount(ctx, 2)
	if e != nil || n != 0 {
		t.Fatalf("cascade count=%d err=%v", n, e)
	}
}

func TestStoreChangedUIDClearsHistorySameUIDPreserves(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, e := s.Bind(ctx, testAccount(3, "10"), 500, []string{"base"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Bind(ctx, testAccount(3, "10"), 500, nil); e != nil {
		t.Fatal(e)
	}
	if ok, _ := s.IsSeen(ctx, 3, "base"); !ok {
		t.Fatal("same UID cleared history")
	}
	if _, e := s.Bind(ctx, testAccount(3, "11"), 500, []string{"new-base"}); e != nil {
		t.Fatal(e)
	}
	if ok, _ := s.IsSeen(ctx, 3, "base"); ok {
		t.Fatal("changed UID preserved old history")
	}
	if ok, _ := s.IsSeen(ctx, 3, "new-base"); !ok {
		t.Fatal("new baseline missing")
	}
}

func TestStoreCapacityBanAndFairDueOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := int64(1); i <= 3; i++ {
		a := testAccount(i, fmt.Sprint(i))
		a.NextPollAt = time.Unix(10-i, 0)
		if _, e := s.Bind(ctx, a, 3, nil); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Bind(ctx, testAccount(4, "4"), 3, nil); e != ErrCapacity {
		t.Fatalf("capacity error=%v", e)
	}
	due, e := s.Due(ctx, time.Unix(20, 0), 3)
	if e != nil {
		t.Fatal(e)
	}
	if due[0].TelegramID != 3 || due[1].TelegramID != 2 || due[2].TelegramID != 1 {
		t.Fatalf("unfair order: %#v", due)
	}
	if e = s.Ban(ctx, 2, 99); e != nil {
		t.Fatal(e)
	}
	if banned, _ := s.IsBanned(ctx, 2); !banned {
		t.Fatal("ban missing")
	}
	if _, e = s.Account(ctx, 2); e == nil {
		t.Fatal("ban did not remove account")
	}
	if e = s.Unban(ctx, 2); e != nil {
		t.Fatal(e)
	}
}

func TestValidateCookiesRejectsWrongKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	good, _ := NewCookieCipher(make([]byte, 32))
	blob, _ := good.Encrypt("sid=x", 1, "1")
	a := testAccount(1, "1")
	a.CookieCipher = blob
	if _, e := s.Bind(ctx, a, 10, nil); e != nil {
		t.Fatal(e)
	}
	bad, _ := NewCookieCipher(bytesOf(1, 32))
	if e := s.ValidateCookies(ctx, bad); e == nil {
		t.Fatal("wrong startup key accepted")
	}
}

func TestOpenStoreRejectsCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(context.Background(), path); err == nil {
		store.Close()
		t.Fatal("corrupt database was accepted")
	}
}

func TestUpdateOffsetNeverMovesBackward(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.UpdateOffset(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOffset(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateOffsetValue(ctx)
	if err != nil || got != 200 {
		t.Fatalf("offset=%d err=%v", got, err)
	}
}
func bytesOf(value byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = value
	}
	return b
}
