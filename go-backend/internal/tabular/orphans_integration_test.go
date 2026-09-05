//go:build integration

// Orphan-sweep tests require a live main Postgres (openMainPool, in
// materializer_integration_test.go) with the tabular schema + files table.

package tabular

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
)

// randomHex32 returns a random 32-hex-char string, formatted the same way
// TableNameForRegion formats a file UUID (dashes stripped) but for a file id
// that does not exist in `files` — used to seed an orphan table.
func randomHex32(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func TestOrphanSweeperDropsOnlyOrphanTables(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)

	fileID, _ := seedFile(t, pool)

	liveTable := TableNameForRegion(fileID, 0, 0)
	liveStage := stagingName(liveTable)
	orphanHex := randomHex32(t)
	orphanTable := fmt.Sprintf("sheet_%s_0_0", orphanHex)
	orphanStage := stagingName(orphanTable)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	mustExec(fmt.Sprintf(`CREATE TABLE tabular.%s (_rowid bigint, a text)`, q(liveTable)))
	mustExec(fmt.Sprintf(`CREATE TABLE tabular.%s (_rowid bigint, a text)`, q(liveStage)))
	mustExec(fmt.Sprintf(`CREATE TABLE tabular.%s (_rowid bigint, a text)`, q(orphanTable)))
	mustExec(fmt.Sprintf(`CREATE TABLE tabular.%s (_rowid bigint, a text)`, q(orphanStage)))
	mustExec(`INSERT INTO tabular_column_values (table_name, column_name, value, row_count) VALUES ($1, 'a', 'x', 1)`, orphanTable)

	t.Cleanup(func() {
		pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%s`, q(liveTable)))
		pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%s`, q(liveStage)))
		pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%s`, q(orphanTable)))
		pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%s`, q(orphanStage)))
		pool.Exec(ctx, `DELETE FROM tabular_column_values WHERE table_name = $1`, orphanTable)
	})

	sweeper := NewOrphanSweeper(pool)
	dropped, err := sweeper.Sweep(ctx, 100)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	droppedSet := map[string]bool{}
	for _, n := range dropped {
		droppedSet[n] = true
	}
	if !droppedSet[orphanTable] {
		t.Errorf("Sweep dropped = %v, want it to include orphan table %q", dropped, orphanTable)
	}
	if !droppedSet[orphanStage] {
		t.Errorf("Sweep dropped = %v, want it to include orphan staging table %q", dropped, orphanStage)
	}
	if droppedSet[liveTable] {
		t.Errorf("Sweep dropped the live table %q, want it untouched", liveTable)
	}
	if droppedSet[liveStage] {
		t.Errorf("Sweep dropped the live staging table %q, want it untouched", liveStage)
	}

	var liveExists, liveStageExists, orphanExists, orphanStageExists bool
	checkExists := func(name string) bool {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='tabular' AND table_name=$1)`,
			name).Scan(&exists); err != nil {
			t.Fatalf("exists check for %q: %v", name, err)
		}
		return exists
	}
	liveExists = checkExists(liveTable)
	liveStageExists = checkExists(liveStage)
	orphanExists = checkExists(orphanTable)
	orphanStageExists = checkExists(orphanStage)

	if !liveExists {
		t.Errorf("live table %q was dropped, want it to survive", liveTable)
	}
	if !liveStageExists {
		t.Errorf("live staging table %q was dropped, want it to survive", liveStage)
	}
	if orphanExists {
		t.Errorf("orphan table %q still exists, want it dropped", orphanTable)
	}
	if orphanStageExists {
		t.Errorf("orphan staging table %q still exists, want it dropped", orphanStage)
	}

	var valueRowCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name = $1`, orphanTable).Scan(&valueRowCount); err != nil {
		t.Fatalf("value row count query: %v", err)
	}
	if valueRowCount != 0 {
		t.Errorf("tabular_column_values rows for orphan table = %d, want 0", valueRowCount)
	}
}
