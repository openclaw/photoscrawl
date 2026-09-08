package photos

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestCopySQLitePrivateRollbackRecovery(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	writer, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err := writer.Exec(`pragma journal_mode=DELETE; pragma cache_size=1;
create table item(id integer primary key, value integer, payload blob);
with recursive n(i) as (select 1 union all select i+1 from n where i<32)
insert into item select i, 1, zeroblob(4096) from n;`); err != nil {
		t.Fatal(err)
	}
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update item set value=2, payload=zeroblob(8192)`); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, suffix := range []string{"", "-journal"} {
		before[suffix], err = os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
	}
	private, cleanup, err := CopySQLite(ctx, source, "photoscrawl-journal-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	reader, err := store.OpenReadOnly(ctx, private)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count, minimum, maximum int
	if err := reader.DB().QueryRow("select count(*), min(value), max(value) from item").Scan(&count, &minimum, &maximum); err != nil {
		t.Fatal(err)
	}
	if count != 32 || minimum != 1 || maximum != 1 {
		t.Fatalf("uncommitted state leaked: count=%d min=%d max=%d", count, minimum, maximum)
	}
	for suffix, want := range before {
		got, err := os.ReadFile(source + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("source rollback recovery mutated %q: %v", suffix, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(source + suffix); !os.IsNotExist(err) {
			t.Fatalf("source sidecar %s created: %v", suffix, err)
		}
	}
}

func TestCopySQLiteFinalVerificationCancellation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(source, []byte("synthetic verification input"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	_, cleanup, err := copySQLiteWithVerifier(ctx, source, "photoscrawl-cancel-test-", func(context.Context, string) error {
		attempts++
		if attempts == 5 {
			cancel()
			return ctx.Err()
		}
		return errors.New("synthetic retry")
	})
	cleanup()
	if attempts != 5 || !errors.Is(err, context.Canceled) {
		t.Fatalf("attempts=%d cancellation=%v", attempts, err)
	}
	if !strings.Contains(err.Error(), source) {
		t.Fatalf("cancellation lacks source path: %v", err)
	}
}
