package place

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type backfillIdentity struct {
	Index          int     `json:"index"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyMeters float64 `json:"accuracy_meters"`
}

// Artifact indexes identify persisted coordinate histories, not sorted row positions.
func assignBackfillIndexes(outDir string, keys []backfillKey) ([]backfillIdentity, error) {
	data, err := os.ReadFile(filepath.Join(outDir, "identities.json"))
	if errors.Is(err, os.ErrNotExist) {
		// Bootstrap existing runs without changing their artifact names.
		data, err = os.ReadFile(filepath.Join(outDir, "manifest.json"))
	}
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	previous := []backfillIdentity{}
	if !missing {
		if err := json.Unmarshal(data, &previous); err != nil {
			return nil, fmt.Errorf("read backfill identity manifest: %w", err)
		}
	}
	byCoordinate := map[string]int{}
	byIndex := map[int]backfillIdentity{}
	lastIndex := -1
	for _, key := range previous {
		coordinate := coordinateKey(key.Latitude, key.Longitude, key.AccuracyMeters)
		_, duplicate := byCoordinate[coordinate]
		_, used := byIndex[key.Index]
		if key.Index < 0 || duplicate || used {
			return nil, errors.New("backfill manifest has ambiguous coordinate indexes")
		}
		byCoordinate[coordinate] = key.Index
		byIndex[key.Index] = key
		lastIndex = max(lastIndex, key.Index)
	}
	// Reserve retired artifacts too, so a later run cannot overwrite their history.
	for _, dir := range []string{"outputs", "errors", "attempts"} {
		entries, err := os.ReadDir(filepath.Join(outDir, dir))
		if err != nil {
			return nil, err
		}
		ext := ".json"
		if dir == "attempts" {
			ext = ".jsonl"
		}
		for _, entry := range entries {
			name, ok := strings.CutSuffix(entry.Name(), ext)
			index, err := strconv.Atoi(name)
			if !ok || err != nil || index < 0 || entry.Name() != fmt.Sprintf("%06d%s", index, ext) {
				continue
			}
			if missing {
				return nil, errors.New("backfill state is missing its identity manifests; restore identities.json or manifest.json, or use a new output directory")
			}
			if identity, known := byIndex[index]; known && dir == "outputs" {
				result, err := readBackfillOutput(filepath.Join(outDir, dir, entry.Name()))
				if err != nil {
					return nil, fmt.Errorf("read backfill output identity: %w", err)
				}
				if result.Input.Location.Latitude != identity.Latitude ||
					result.Input.Location.Longitude != identity.Longitude ||
					result.Input.AccuracyMeters != identity.AccuracyMeters {
					return nil, fmt.Errorf("backfill output %s contradicts its recorded coordinate identity", entry.Name())
				}
			}
			lastIndex = max(lastIndex, index)
		}
	}
	for i := range keys {
		coordinate := coordinateKey(keys[i].Latitude, keys[i].Longitude, keys[i].AccuracyMeters)
		if index, ok := byCoordinate[coordinate]; ok {
			keys[i].Index = index
			continue
		}
		if lastIndex == int(^uint(0)>>1) {
			return nil, errors.New("backfill artifact index exhausted")
		}
		lastIndex++
		keys[i].Index = lastIndex
		previous = append(previous, backfillIdentity{
			Index: lastIndex, Latitude: keys[i].Latitude,
			Longitude: keys[i].Longitude, AccuracyMeters: keys[i].AccuracyMeters,
		})
	}
	return previous, nil
}
