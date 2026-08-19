package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/store"
)

const preciseLocationAccuracyMeters = 100

type TimelineOptions struct {
	From             string
	To               string
	IncludeUnlocated bool
}

type TimelineResult struct {
	From         string                `json:"from"`
	To           string                `json:"to"`
	Count        int                   `json:"count"`
	Observations []TimelineObservation `json:"observations"`
}

type TimelineObservation struct {
	AssetID               string          `json:"asset_id"`
	LocationObservationID string          `json:"location_observation_id,omitempty"`
	SourceRef             string          `json:"source_ref"`
	CreatedAt             string          `json:"created_at"`
	MediaType             string          `json:"media_type"`
	SourceType            int             `json:"source_type"`
	Albums                []TimelineAlbum `json:"albums,omitempty"`
	Latitude              *float64        `json:"lat,omitempty"`
	Longitude             *float64        `json:"lng,omitempty"`
	AccuracyMeters        *float64        `json:"accuracy_m,omitempty"`
	IsPrecise             *bool           `json:"is_precise,omitempty"`
}

type TimelineAlbum struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

func Timeline(ctx context.Context, paths Paths, opts TimelineOptions) (TimelineResult, error) {
	from, err := timelineBound("from", opts.From)
	if err != nil {
		return TimelineResult{}, err
	}
	to, err := timelineBound("to", opts.To)
	if err != nil {
		return TimelineResult{}, err
	}
	if !from.Before(to) {
		return TimelineResult{}, errors.New("from must be before to")
	}

	fromText := from.UTC().Format(time.RFC3339Nano)
	toText := to.UTC().Format(time.RFC3339Nano)
	db, err := store.OpenReadOnly(ctx, paths.Database)
	if err != nil {
		return TimelineResult{}, err
	}
	defer db.Close()

	locationJoin := "join location_observation on location_observation.asset_id = asset.id"
	if opts.IncludeUnlocated {
		locationJoin = "left join location_observation on location_observation.asset_id = asset.id"
	}
	rows, err := db.DB().QueryContext(ctx, `
select asset.id, location_observation.id, asset.creation_date, asset.media_type,
       coalesce(cast(json_extract(asset.metadata_json, '$.source_type') as integer), 0),
       coalesce((
         select json_group_array(json_object('id', album_id, 'title', album_title, 'kind', album_kind))
         from album_membership
         where album_membership.asset_id = asset.id
       ), '[]'),
       location_observation.latitude, location_observation.longitude,
       location_observation.horizontal_accuracy
from asset
`+locationJoin+`
where julianday(asset.creation_date) >= julianday(?)
  and julianday(asset.creation_date) < julianday(?)
  and not exists (
    select 1
    from classification_queue
    where classification_queue.asset_id = asset.id
      and classification_queue.state = 'deleted'
  )
order by julianday(asset.creation_date), asset.creation_date, asset.id, location_observation.id
`, fromText, toText)
	if err != nil {
		return TimelineResult{}, fmt.Errorf("query photo timeline: %w", err)
	}
	defer rows.Close()

	result := TimelineResult{From: fromText, To: toText, Observations: []TimelineObservation{}}
	for rows.Next() {
		var observation TimelineObservation
		var locationID sql.NullString
		var albumsJSON string
		var latitude, longitude, accuracy sql.NullFloat64
		if err := rows.Scan(
			&observation.AssetID,
			&locationID,
			&observation.CreatedAt,
			&observation.MediaType,
			&observation.SourceType,
			&albumsJSON,
			&latitude,
			&longitude,
			&accuracy,
		); err != nil {
			return TimelineResult{}, err
		}
		if err := json.Unmarshal([]byte(albumsJSON), &observation.Albums); err != nil {
			return TimelineResult{}, fmt.Errorf("decode timeline albums: %w", err)
		}
		observation.SourceRef = observation.AssetID
		if locationID.Valid {
			observation.LocationObservationID = locationID.String
			observation.SourceRef = locationID.String
		}
		if latitude.Valid && longitude.Valid {
			observation.Latitude = &latitude.Float64
			observation.Longitude = &longitude.Float64
		}
		if accuracy.Valid {
			accuracyMeters := accuracy.Float64
			isPrecise := accuracyMeters >= 0 && accuracyMeters <= preciseLocationAccuracyMeters
			observation.AccuracyMeters = &accuracyMeters
			observation.IsPrecise = &isPrecise
		}
		result.Observations = append(result.Observations, observation)
	}
	if err := rows.Err(); err != nil {
		return TimelineResult{}, err
	}
	result.Count = len(result.Observations)
	return result, nil
}

func timelineBound(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an ISO 8601 timestamp with an offset: %w", name, err)
	}
	return parsed, nil
}
