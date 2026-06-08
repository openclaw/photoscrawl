package place

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func Run(ctx context.Context, opts Options) (Result, error) {
	input, err := loadInput(opts.InputPath)
	if err != nil {
		return Result{}, err
	}
	radius := opts.RadiusMeters
	if radius <= 0 {
		radius = defaultRadiusMeters
	}
	cacheDir := strings.TrimSpace(opts.CacheDir)
	if cacheDir == "" {
		return Result{}, errors.New("place context cache dir is required")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return Result{}, err
	}
	cachePath, err := cachePath(cacheDir, input, radius)
	if err != nil {
		return Result{}, err
	}
	if data, err := os.ReadFile(cachePath); err == nil {
		var cached Result
		if err := json.Unmarshal(data, &cached); err == nil {
			if err := validateComplete(cached); err == nil {
				cached.Cached = true
				return cached, nil
			}
		}
	}

	result, err := rawAppleResult(ctx, input, radius)
	if err != nil {
		return Result{}, err
	}
	if result.POITotal == 0 {
		result.POITotal = len(result.POICandidates)
	}
	result.POICandidates = calibrateCandidates(input, radius, result.POICandidates)

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(cachePath, append(data, '\n'), 0o600); err != nil {
		return Result{}, err
	}
	return result, nil
}

func LoadResult(path string) (Result, error) {
	reader, closeReader, err := inputReader(path)
	if err != nil {
		return Result{}, err
	}
	if closeReader != nil {
		defer closeReader()
	}
	var result Result
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&result); err != nil {
		return Result{}, err
	}
	if result.POITotal == 0 {
		result.POITotal = len(result.POICandidates)
	}
	if err := validatePOIStatus(result.POIStatus); err != nil {
		return Result{}, err
	}
	return result, nil
}

func rawAppleResult(ctx context.Context, input Input, radius float64) (Result, error) {
	result, err := applePlaceContext(ctx, input, radius)
	if err != nil {
		return Result{}, err
	}
	result.Input = input
	result.Provider = "apple"
	result.Source = "apple_corelocation_mapkit"
	result.RadiusMeters = radius
	result.GeneratedAt = time.Now().UTC()
	result.Area = areaFromAddress(result.Address)
	result.POITotal = len(result.POICandidates)
	result.POIStatus = poiStatus(result)
	if err := validateComplete(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateComplete(result Result) error {
	if result.Address == nil {
		return errors.New("Apple place context incomplete: missing reverse-geocoded address")
	}
	if err := validatePOIStatus(result.POIStatus); err != nil {
		return err
	}
	return nil
}

func poiStatus(result Result) string {
	if strings.TrimSpace(result.POIStatus) != "" {
		return result.POIStatus
	}
	if len(result.POICandidates) > 0 {
		return POIStatusFound
	}
	return POIStatusNone
}

func validatePOIStatus(status string) error {
	status = strings.TrimSpace(status)
	if status == "" {
		return nil
	}
	switch status {
	case POIStatusFound, POIStatusNone, POIStatusProviderError:
		return nil
	default:
		return fmt.Errorf("invalid poi_status %q", status)
	}
}

func areaFromAddress(address *Address) []AreaLevel {
	if address == nil {
		return nil
	}
	levels := []struct {
		level string
		name  string
	}{
		{"country", address.Country},
		{"administrative_area", address.AdministrativeArea},
		{"sub_administrative_area", address.SubAdministrativeArea},
		{"locality", address.Locality},
		{"sub_locality", address.SubLocality},
	}
	out := []AreaLevel{}
	for _, level := range levels {
		if strings.TrimSpace(level.name) == "" {
			continue
		}
		out = append(out, AreaLevel{
			Level:  level.level,
			Name:   strings.TrimSpace(level.name),
			Source: address.Source,
		})
	}
	return out
}

func calibrateCandidates(input Input, radius float64, candidates []POICandidate) []POICandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].DistanceM != candidates[j].DistanceM {
			return candidates[i].DistanceM < candidates[j].DistanceM
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	return candidates
}

func cachePath(dir string, input Input, radius float64) (string, error) {
	key := struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Accuracy  float64 `json:"accuracy"`
		Radius    float64 `json:"radius"`
	}{
		Latitude:  input.Location.Latitude,
		Longitude: input.Location.Longitude,
		Accuracy:  input.AccuracyMeters,
		Radius:    radius,
	}
	data, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

func compactStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
