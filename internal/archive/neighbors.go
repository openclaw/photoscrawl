package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	nearbyTimeWindowSeconds = 3600.0
	nearbyLocationDegrees   = 0.01
)

type NeighborOptions struct {
	ID    string
	Limit int
}

type NeighborResult struct {
	ID        string        `json:"id"`
	Limit     int           `json:"limit"`
	Neighbors []NeighborHit `json:"neighbors"`
}

type NeighborHit struct {
	ID           string           `json:"id"`
	MediaType    string           `json:"media_type"`
	CreationDate string           `json:"creation_date"`
	Score        float64          `json:"score"`
	Reasons      []NeighborReason `json:"reasons"`
	EvidenceIDs  []string         `json:"evidence_ids"`
}

type NeighborReason struct {
	Type   string         `json:"type"`
	Method string         `json:"method"`
	Weight float64        `json:"weight"`
	Detail map[string]any `json:"detail"`
}

type neighborCandidate struct {
	ID           string
	MediaType    string
	CreationDate string
	Reason       NeighborReason
	EvidenceIDs  []string
}

func Neighbors(ctx context.Context, paths Paths, opts NeighborOptions) (NeighborResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return NeighborResult{}, errors.New("id is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return NeighborResult{}, err
	}
	defer db.Close()

	if _, err := oneRow(ctx, db.DB(), `select id from asset where id = ? and deleted_at is null`, id); errors.Is(err, sql.ErrNoRows) {
		return NeighborResult{}, fmt.Errorf("asset not found: %s", id)
	} else if err != nil {
		return NeighborResult{}, err
	}

	queryLimit := limit * 4
	loaders := []func(context.Context, *sql.DB, string, int) ([]neighborCandidate, error){
		sameBurstNeighbors,
		sameAlbumNeighbors,
		sameResourceHashNeighbors,
		nearbyTimeNeighbors,
		nearbyLocationNeighbors,
		sharedObservationNeighbors,
	}
	candidates := []neighborCandidate{}
	for _, load := range loaders {
		loaded, err := load(ctx, db.DB(), id, queryLimit)
		if err != nil {
			return NeighborResult{}, err
		}
		candidates = append(candidates, loaded...)
	}

	neighbors := aggregateNeighbors(candidates)
	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].Score != neighbors[j].Score {
			return neighbors[i].Score > neighbors[j].Score
		}
		if len(neighbors[i].Reasons) != len(neighbors[j].Reasons) {
			return len(neighbors[i].Reasons) > len(neighbors[j].Reasons)
		}
		if neighbors[i].CreationDate != neighbors[j].CreationDate {
			return neighbors[i].CreationDate < neighbors[j].CreationDate
		}
		return neighbors[i].ID < neighbors[j].ID
	})
	if len(neighbors) > limit {
		neighbors = neighbors[:limit]
	}
	return NeighborResult{ID: id, Limit: limit, Neighbors: neighbors}, nil
}

func sameBurstNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	rows, err := db.QueryContext(ctx, `
select target.id, target.media_type, target.creation_date, target.burst_identifier,
       coalesce(source_evidence.id, ''), coalesce(target_evidence.id, '')
from asset source
join asset target on target.burst_identifier = source.burst_identifier and target.id <> source.id
left join evidence_ref source_evidence on source_evidence.asset_id = source.id and source_evidence.evidence_kind = 'asset_metadata'
left join evidence_ref target_evidence on target_evidence.asset_id = target.id and target_evidence.evidence_kind = 'asset_metadata'
where source.id = ? and source.deleted_at is null and target.deleted_at is null and trim(source.burst_identifier) <> ''
order by target.creation_date, target.id
limit ?
`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("load same burst neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var burstID, sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &burstID, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		candidate.Reason = NeighborReason{
			Type:   "same_burst",
			Method: "asset.burst_identifier",
			Weight: 0.95,
			Detail: map[string]any{"burst_identifier_present": burstID != ""},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func sameAlbumNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	rows, err := db.QueryContext(ctx, `
select target.id, target.media_type, target.creation_date, target_membership.album_id, target_membership.album_title,
       coalesce(source_evidence.id, ''), coalesce(target_evidence.id, '')
from album_membership source_membership
join album_membership target_membership on target_membership.album_id = source_membership.album_id and target_membership.asset_id <> source_membership.asset_id
join asset target on target.id = target_membership.asset_id
left join evidence_ref source_evidence on source_evidence.asset_id = source_membership.asset_id and source_evidence.evidence_kind = 'album_membership' and source_evidence.pointer = 'album:' || source_membership.album_id
left join evidence_ref target_evidence on target_evidence.asset_id = target_membership.asset_id and target_evidence.evidence_kind = 'album_membership' and target_evidence.pointer = 'album:' || target_membership.album_id
where source_membership.asset_id = ? and target.deleted_at is null
order by target.creation_date, target.id
limit ?
`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("load same album neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var albumID, albumTitle, sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &albumID, &albumTitle, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		candidate.Reason = NeighborReason{
			Type:   "same_album",
			Method: "album_membership.album_id",
			Weight: 0.8,
			Detail: map[string]any{"album_id": albumID, "album_title": albumTitle},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func sameResourceHashNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	rows, err := db.QueryContext(ctx, `
select distinct target.id, target.media_type, target.creation_date, target_resource.sha256,
       coalesce((select id from evidence_ref where asset_id = source_resource.asset_id and evidence_kind = 'asset_resource' limit 1), ''),
       coalesce((select id from evidence_ref where asset_id = target_resource.asset_id and evidence_kind = 'asset_resource' limit 1), '')
from asset_resource source_resource
join asset_resource target_resource on target_resource.sha256 = source_resource.sha256 and target_resource.asset_id <> source_resource.asset_id
join asset target on target.id = target_resource.asset_id
where source_resource.asset_id = ?
  and source_resource.deleted_at is null
  and target_resource.deleted_at is null
  and target.deleted_at is null
  and trim(source_resource.sha256) <> ''
order by target.creation_date, target.id
limit ?
`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("load same resource hash neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var hash, sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &hash, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		candidate.Reason = NeighborReason{
			Type:   "same_resource_hash",
			Method: "asset_resource.sha256",
			Weight: 1,
			Detail: map[string]any{"hash_present": hash != ""},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func nearbyTimeNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	const delta = `abs((julianday(target.creation_date) - julianday(source.creation_date)) * 86400.0)`
	rows, err := db.QueryContext(ctx, `
select target.id, target.media_type, target.creation_date, `+delta+` as seconds_apart,
       coalesce(source_evidence.id, ''), coalesce(target_evidence.id, '')
from asset source
join asset target on target.id <> source.id
left join evidence_ref source_evidence on source_evidence.asset_id = source.id and source_evidence.evidence_kind = 'asset_metadata'
left join evidence_ref target_evidence on target_evidence.asset_id = target.id and target_evidence.evidence_kind = 'asset_metadata'
where source.id = ?
  and source.deleted_at is null
  and target.deleted_at is null
  and trim(source.creation_date) <> ''
  and trim(target.creation_date) <> ''
  and `+delta+` <= ?
order by seconds_apart, target.creation_date, target.id
limit ?
`, id, nearbyTimeWindowSeconds, limit)
	if err != nil {
		return nil, fmt.Errorf("load nearby time neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var secondsApart float64
		var sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &secondsApart, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		candidate.Reason = NeighborReason{
			Type:   "nearby_time",
			Method: "asset.creation_date",
			Weight: timeNeighborWeight(secondsApart),
			Detail: map[string]any{"seconds_apart": secondsApart, "window_seconds": nearbyTimeWindowSeconds},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func nearbyLocationNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	rows, err := db.QueryContext(ctx, `
select target.id, target.media_type, target.creation_date,
       abs(target_location.latitude - source_location.latitude) as latitude_delta,
       abs(target_location.longitude - source_location.longitude) as longitude_delta,
       coalesce(source_location.evidence_id, ''), coalesce(target_location.evidence_id, '')
from location_observation source_location
join location_observation target_location on target_location.asset_id <> source_location.asset_id
join asset target on target.id = target_location.asset_id
where source_location.asset_id = ?
  and target.deleted_at is null
  and abs(target_location.latitude - source_location.latitude) <= ?
  and abs(target_location.longitude - source_location.longitude) <= ?
order by latitude_delta + longitude_delta, target.creation_date, target.id
limit ?
`, id, nearbyLocationDegrees, nearbyLocationDegrees, limit)
	if err != nil {
		return nil, fmt.Errorf("load nearby location neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var latitudeDelta, longitudeDelta float64
		var sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &latitudeDelta, &longitudeDelta, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		approxMeters := (latitudeDelta + longitudeDelta) * 111000
		candidate.Reason = NeighborReason{
			Type:   "nearby_location",
			Method: "location_observation.raw_gps_window",
			Weight: locationNeighborWeight(approxMeters),
			Detail: map[string]any{
				"latitude_delta":         latitudeDelta,
				"longitude_delta":        longitudeDelta,
				"approx_distance_meters": approxMeters,
			},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func sharedObservationNeighbors(ctx context.Context, db *sql.DB, id string, limit int) ([]neighborCandidate, error) {
	rows, err := db.QueryContext(ctx, `
select target.id, target.media_type, target.creation_date, target_observation.observation_type, target_observation.label,
       min(source_observation.confidence, target_observation.confidence) as shared_confidence,
       coalesce(source_observation.evidence_id, ''), coalesce(target_observation.evidence_id, '')
from visual_observation source_observation
join visual_observation target_observation on target_observation.observation_type = source_observation.observation_type
  and target_observation.label = source_observation.label
  and target_observation.asset_id <> source_observation.asset_id
join asset target on target.id = target_observation.asset_id
where source_observation.asset_id = ?
  and target.deleted_at is null
  and source_observation.observation_type = 'document_signal'
order by shared_confidence desc, target.creation_date, target.id
limit ?
`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("load shared observation neighbors: %w", err)
	}
	defer rows.Close()

	var out []neighborCandidate
	for rows.Next() {
		var candidate neighborCandidate
		var observationType, label string
		var confidence float64
		var sourceEvidenceID, targetEvidenceID string
		if err := rows.Scan(&candidate.ID, &candidate.MediaType, &candidate.CreationDate, &observationType, &label, &confidence, &sourceEvidenceID, &targetEvidenceID); err != nil {
			return nil, err
		}
		candidate.Reason = NeighborReason{
			Type:   "shared_observation",
			Method: "visual_observation.type_label",
			Weight: math.Min(0.75, confidence*0.75),
			Detail: map[string]any{"observation_type": observationType, "label": label},
		}
		candidate.EvidenceIDs = nonEmpty(sourceEvidenceID, targetEvidenceID)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func aggregateNeighbors(candidates []neighborCandidate) []NeighborHit {
	byID := map[string]*NeighborHit{}
	for _, candidate := range candidates {
		hit := byID[candidate.ID]
		if hit == nil {
			hit = &NeighborHit{
				ID:           candidate.ID,
				MediaType:    candidate.MediaType,
				CreationDate: candidate.CreationDate,
			}
			byID[candidate.ID] = hit
		}
		if hasReason(hit.Reasons, candidate.Reason) {
			continue
		}
		hit.Reasons = append(hit.Reasons, candidate.Reason)
		hit.Score += candidate.Reason.Weight
		hit.EvidenceIDs = appendUniqueStrings(hit.EvidenceIDs, candidate.EvidenceIDs...)
	}

	out := make([]NeighborHit, 0, len(byID))
	for _, hit := range byID {
		if hit.Score > 1 {
			hit.Score = 1
		}
		sort.Slice(hit.Reasons, func(i, j int) bool {
			if hit.Reasons[i].Weight != hit.Reasons[j].Weight {
				return hit.Reasons[i].Weight > hit.Reasons[j].Weight
			}
			return hit.Reasons[i].Type < hit.Reasons[j].Type
		})
		sort.Strings(hit.EvidenceIDs)
		out = append(out, *hit)
	}
	return out
}

func hasReason(reasons []NeighborReason, reason NeighborReason) bool {
	for _, existing := range reasons {
		if existing.Type == reason.Type && existing.Method == reason.Method && fmt.Sprint(existing.Detail) == fmt.Sprint(reason.Detail) {
			return true
		}
	}
	return false
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, addition := range additions {
		addition = strings.TrimSpace(addition)
		if addition == "" || seen[addition] {
			continue
		}
		seen[addition] = true
		values = append(values, addition)
	}
	return values
}

func timeNeighborWeight(secondsApart float64) float64 {
	switch {
	case secondsApart <= 60:
		return 0.75
	case secondsApart <= 600:
		return 0.65
	default:
		return 0.45
	}
}

func locationNeighborWeight(approxMeters float64) float64 {
	switch {
	case approxMeters <= 100:
		return 0.75
	case approxMeters <= 500:
		return 0.65
	default:
		return 0.5
	}
}
