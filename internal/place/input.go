package place

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

func loadInput(path string) (Input, error) {
	reader, closeReader, err := inputReader(path)
	if err != nil {
		return Input{}, err
	}
	if closeReader != nil {
		defer closeReader()
	}
	var raw map[string]any
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return Input{}, fmt.Errorf("read place input: %w", err)
	}
	input := parseInput(raw)
	return input, validateInput(input)
}

func inputReader(path string) (io.Reader, func(), error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) == "-" {
		return os.Stdin, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func parseInput(raw map[string]any) Input {
	input := Input{
		AssetID:        firstString(raw, "asset_id", "assetID", "local_identifier", "localIdentifier"),
		ImagePath:      firstString(raw, "image_path", "imagePath"),
		TakenAt:        firstString(raw, "taken_at", "takenAt", "creation_date", "creationDate"),
		AccuracyMeters: firstFloat(raw, "accuracy_meters", "accuracyMeters", "horizontal_accuracy", "horizontalAccuracy"),
	}
	if location, ok := objectValue(raw, "location"); ok {
		input.Location = coordinateFrom(location)
		if input.AccuracyMeters == 0 {
			input.AccuracyMeters = firstFloat(location, "accuracy_meters", "accuracyMeters", "horizontal_accuracy", "horizontalAccuracy")
		}
	}
	if asset, ok := objectValue(raw, "asset"); ok {
		if input.AssetID == "" {
			input.AssetID = firstString(asset, "local_identifier", "localIdentifier", "asset_id", "assetID")
		}
		if input.TakenAt == "" {
			input.TakenAt = firstString(asset, "creation_date", "creationDate", "taken_at", "takenAt")
		}
		if location, ok := objectValue(asset, "location"); ok {
			input.Location = coordinateFrom(location)
			if input.AccuracyMeters == 0 {
				input.AccuracyMeters = firstFloat(location, "accuracy_meters", "accuracyMeters", "horizontal_accuracy", "horizontalAccuracy")
			}
		}
	}
	if input.Location.Latitude == 0 && input.Location.Longitude == 0 {
		input.Location = Coordinate{
			Latitude:  firstFloat(raw, "latitude", "lat"),
			Longitude: firstFloat(raw, "longitude", "lon", "lng"),
		}
	}
	return input
}

func validateInput(input Input) error {
	if math.IsNaN(input.Location.Latitude) || math.IsNaN(input.Location.Longitude) {
		return errors.New("latitude and longitude must be finite")
	}
	if input.Location.Latitude < -90 || input.Location.Latitude > 90 {
		return fmt.Errorf("latitude out of range: %f", input.Location.Latitude)
	}
	if input.Location.Longitude < -180 || input.Location.Longitude > 180 {
		return fmt.Errorf("longitude out of range: %f", input.Location.Longitude)
	}
	if input.Location.Latitude == 0 && input.Location.Longitude == 0 {
		return errors.New("latitude and longitude are required")
	}
	if math.IsNaN(input.AccuracyMeters) || math.IsInf(input.AccuracyMeters, 0) {
		return errors.New("accuracy must be finite")
	}
	return nil
}

func coordinateFrom(raw map[string]any) Coordinate {
	return Coordinate{
		Latitude:  firstFloat(raw, "latitude", "lat"),
		Longitude: firstFloat(raw, "longitude", "lon", "lng"),
	}
}

func objectValue(raw map[string]any, key string) (map[string]any, bool) {
	value, ok := raw[key]
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func firstFloat(raw map[string]any, keys ...string) float64 {
	invalid := false
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v
		case json.Number:
			f, err := v.Float64()
			if err == nil {
				return f
			}
			invalid = true
		case string:
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
			invalid = true
		}
	}
	if invalid {
		// Preserve malformed values for validation instead of replacing them with zero.
		return math.NaN()
	}
	return 0
}
