package place

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func ensureBackfillDirs(outDir string) error {
	for _, name := range []string{"outputs", "errors", "attempts", "logs"} {
		if err := os.MkdirAll(filepath.Join(outDir, name), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func backfillJobs(outDir string, keys []backfillKey, attempt int) ([]backfillKey, error) {
	jobs := []backfillKey{}
	for _, key := range keys {
		if outputMatches(filepath.Join(outDir, "outputs", key.filename()+".json"), key) {
			continue
		}
		attempts, err := countAttempts(filepath.Join(outDir, "attempts", key.filename()+".jsonl"))
		if err != nil {
			return nil, err
		}
		if attempts == attempt-1 {
			jobs = append(jobs, key)
		}
	}
	return jobs, nil
}

func countAttempts(path string) (int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count, scanner.Err()
}

func countMatchingOutputs(outDir string, keys []backfillKey) int {
	count := 0
	for _, key := range keys {
		if outputMatches(filepath.Join(outDir, "outputs", key.filename()+".json"), key) {
			count++
		}
	}
	return count
}

func countSuccessOutputs(outDir string) int {
	matches, err := filepath.Glob(filepath.Join(outDir, "outputs", "*.json"))
	if err != nil {
		return 0
	}
	return len(matches)
}

func countFinalErrors(outDir string) int {
	matches, err := filepath.Glob(filepath.Join(outDir, "errors", "*.json"))
	if err != nil {
		return 0
	}
	count := 0
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var failure backfillError
		if json.Unmarshal(data, &failure) == nil && failure.Final {
			count++
		}
	}
	return count
}

func loadBackfillKeys(ctx context.Context, dbPath string) ([]backfillKey, int, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, 0, err
	}
	db, err := sql.Open("sqlite", readOnlySQLiteDSN(dbPath))
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	assetFilter := ""
	hasDeletedAt, err := sqliteTableHasColumn(ctx, db, "asset", "deleted_at")
	if err != nil {
		return nil, 0, err
	}
	if hasDeletedAt {
		assetFilter = "a.deleted_at is null and "
	}
	rows, err := db.QueryContext(ctx, `
select a.id, a.creation_date, l.latitude, l.longitude, coalesce(l.horizontal_accuracy, 0)
from location_observation l
join asset a on a.id = l.asset_id
where `+assetFilter+`(l.latitude != 0 or l.longitude != 0)
order by l.latitude, l.longitude, coalesce(l.horizontal_accuracy, 0), a.creation_date, a.id
`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	byKey := map[string]*backfillKey{}
	locatedAssets := 0
	for rows.Next() {
		var assetID, takenAt string
		var lat, lon, accuracy float64
		if err := rows.Scan(&assetID, &takenAt, &lat, &lon, &accuracy); err != nil {
			return nil, 0, err
		}
		locatedAssets++
		key := coordinateKey(lat, lon, accuracy)
		existing := byKey[key]
		if existing == nil {
			byKey[key] = &backfillKey{
				Latitude:              lat,
				Longitude:             lon,
				AccuracyMeters:        accuracy,
				AssetCount:            1,
				RepresentativeAssetID: assetID,
				FirstSeen:             takenAt,
				LastSeen:              takenAt,
				key:                   key,
			}
			continue
		}
		existing.AssetCount++
		if takenAt < existing.FirstSeen || existing.FirstSeen == "" {
			existing.FirstSeen = takenAt
		}
		if takenAt > existing.LastSeen {
			existing.LastSeen = takenAt
			existing.RepresentativeAssetID = assetID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	keys := make([]backfillKey, 0, len(byKey))
	for _, key := range byKey {
		keys = append(keys, *key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].key < keys[j].key
	})
	for i := range keys {
		keys[i].Index = i
	}
	return keys, locatedAssets, nil
}

func sqliteTableHasColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info(\""+table+"\")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func readOnlySQLiteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	return u.String()
}

func coordinateKey(lat, lon, accuracy float64) string {
	return fmt.Sprintf("%.8f|%.8f|%.1f", lat, lon, accuracy)
}

func (key backfillKey) filename() string {
	return fmt.Sprintf("%06d", key.Index)
}

func outputMatches(path string, key backfillKey) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return false
	}
	if err := validateComplete(result); err != nil {
		return false
	}
	return sameCoordinate(result.Input.Location.Latitude, key.Latitude) &&
		sameCoordinate(result.Input.Location.Longitude, key.Longitude) &&
		math.Abs(result.Input.AccuracyMeters-key.AccuracyMeters) < 0.1
}

func sameCoordinate(a, b float64) bool {
	return math.Abs(a-b) < 0.00000001
}

func backfillRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 2:
		return 2 * time.Minute
	case 3:
		return 10 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func appendAttempt(path string, attempt backfillAttempt) error {
	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func appendProgress(path string, startedAt time.Time, result BackfillResult) error {
	elapsed := time.Since(startedAt).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(result.ProviderAttempts) / elapsed
	}
	line := fmt.Sprintf("%s\tattempts=%d\tok=%d\tattempt_failures=%d\tcooldowns=%d\trate=%.2f/s\n",
		time.Now().UTC().Format(time.RFC3339),
		result.ProviderAttempts,
		result.Successes,
		result.AttemptFailures,
		result.ProviderCooldowns,
		rate,
	)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(line)
	return err
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
