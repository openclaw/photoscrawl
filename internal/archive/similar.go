package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	similarAppleLabelWeight = 1.25
	similarPersonWeight     = 0.5
	similarPlaceWeight      = 0.25
	similarEventWindow      = 2 * time.Hour
)

var similarTermTypes = []string{
	"scene",
	"object_or_food",
	"place_type_candidate",
	"landmark_or_place_name_candidate",
	"cluster_feature",
}

var similarAppleLabelTypes = []string{
	"apple_label",
	"apple_activity",
	"apple_venue",
}

type SimilarOptions struct {
	ID               string
	ExcludeIDsFile   string
	Limit            int
	IncludeSameEvent bool
}

type SimilarResult struct {
	ID     string         `json:"id"`
	Limit  int            `json:"limit"`
	Assets []SimilarAsset `json:"assets"`
}

type SimilarAsset struct {
	AssetRow
	Score  float64  `json:"score"`
	Shared []string `json:"shared"`
}

type similarFeature struct {
	key     string
	display string
	weight  float64
}

// Similar ranks images by shared local-model terms and Apple metadata labels.
// It is label similarity, not pixel similarity.
func Similar(ctx context.Context, paths Paths, opts SimilarOptions) (SimilarResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return SimilarResult{}, errors.New("id is required")
	}
	limit := bounded(opts.Limit, 30, 100)
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return SimilarResult{}, err
	}
	defer db.Close()

	seed, err := loadSimilarSeed(ctx, db.DB(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return SimilarResult{}, fmt.Errorf("asset not found: %s", id)
	}
	if err != nil {
		return SimilarResult{}, err
	}
	idf, err := loadSimilarIDF(ctx, db.DB())
	if err != nil {
		return SimilarResult{}, err
	}
	features := map[string]map[string]similarFeature{}
	if err := addSharedTermFeatures(ctx, db.DB(), id, idf, features); err != nil {
		return SimilarResult{}, err
	}
	if err := addSharedVisualFeatures(ctx, db.DB(), id, similarAppleLabelTypes, similarAppleLabelWeight, "apple", features); err != nil {
		return SimilarResult{}, err
	}
	if err := addSharedPeopleFeatures(ctx, db.DB(), id, features); err != nil {
		return SimilarResult{}, err
	}
	if err := addSharedVisualFeatures(ctx, db.DB(), id, []string{"apple_place"}, similarPlaceWeight, "place", features); err != nil {
		return SimilarResult{}, err
	}

	assets, err := loadAssets(ctx, db.DB())
	if err != nil {
		return SimilarResult{}, err
	}
	signalSet, err := loadSignals(ctx, db.DB())
	if err != nil {
		return SimilarResult{}, err
	}
	excluded, err := readExcluded(opts.ExcludeIDsFile)
	if err != nil {
		return SimilarResult{}, err
	}

	candidates := make([]SimilarAsset, 0, len(features))
	qualityAssets := make(map[string]fullAsset, len(features))
	for _, asset := range assets {
		if _, ok := features[asset.ID]; !ok {
			continue
		}
		if !includeSimilarCandidate(seed, asset, signalSet, excluded, opts.IncludeSameEvent) {
			continue
		}
		qualityAssets[asset.ID] = asset
		candidates = append(candidates, similarAsset(asset, features[asset.ID]))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		a := qualityAssets[candidates[i].ID]
		b := qualityAssets[candidates[j].ID]
		if quality := compareQuality(a, b, signalSet, nil); quality != 0 {
			return quality < 0
		}
		if !a.created.Equal(b.created) {
			return a.created.After(b.created)
		}
		return a.ID < b.ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return SimilarResult{ID: id, Limit: limit, Assets: candidates}, nil
}

func loadSimilarSeed(ctx context.Context, db *sql.DB, id string) (fullAsset, error) {
	var asset fullAsset
	var created string
	err := db.QueryRowContext(ctx, `
select id, local_identifier, creation_date, media_type, hidden, favorite, width, height,
       burst_identifier, media_subtypes, timezone_name
from asset
where id = ? and deleted_at is null
`, id).Scan(&asset.ID, &asset.LocalIdentifier, &created, &asset.MediaType, &asset.hidden, &asset.favorite, &asset.width, &asset.height, &asset.burst, &asset.subtypes, &asset.tz)
	if err != nil {
		return fullAsset{}, err
	}
	asset.CreationDate = created
	asset.created, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return fullAsset{}, fmt.Errorf("parse creation date for asset %q: %w", asset.ID, err)
	}
	return asset, nil
}

func loadSimilarIDF(ctx context.Context, db *sql.DB) (map[string]float64, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(similarTermTypes)), ",")
	args := make([]any, 0, len(similarTermTypes))
	for _, termType := range similarTermTypes {
		args = append(args, termType)
	}
	query := `
with eligible as (
  select distinct observation_term.asset_id, observation_term.term_type, observation_term.term
  from observation_term
  join asset on asset.id = observation_term.asset_id
  where observation_term.term_type in (` + placeholders + `)
    and trim(observation_term.term) <> ''
    and asset.deleted_at is null
)
select term_type, term, count(*) as document_frequency,
       (select count(distinct asset_id) from eligible) as document_count
from eligible
group by term_type, term
`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load similar term frequencies: %w", err)
	}
	defer rows.Close()
	idf := map[string]float64{}
	for rows.Next() {
		var termType, term string
		var frequency, count int
		if err := rows.Scan(&termType, &term, &frequency, &count); err != nil {
			return nil, err
		}
		idf[similarTermKey(termType, term)] = math.Log(float64(count+1)/float64(frequency+1)) + 1
	}
	return idf, rows.Err()
}

func addSharedTermFeatures(ctx context.Context, db *sql.DB, id string, idf map[string]float64, features map[string]map[string]similarFeature) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(similarTermTypes)), ",")
	args := []any{id}
	for _, termType := range similarTermTypes {
		args = append(args, termType)
	}
	rows, err := db.QueryContext(ctx, `
select distinct target.asset_id, source.term_type, source.term
from observation_term source
join observation_term target
  on target.term_type = source.term_type and target.term = source.term
where source.asset_id = ?
  and source.term_type in (`+placeholders+`)
  and trim(source.term) <> ''
  and target.asset_id <> source.asset_id
`, args...)
	if err != nil {
		return fmt.Errorf("load shared similar terms: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID, termType, term string
		if err := rows.Scan(&assetID, &termType, &term); err != nil {
			return err
		}
		key := similarTermKey(termType, term)
		weight, ok := idf[key]
		if !ok {
			continue
		}
		addSimilarFeature(features, assetID, similarFeature{key: "term:" + key, display: term, weight: weight})
	}
	return rows.Err()
}

func addSharedVisualFeatures(ctx context.Context, db *sql.DB, id string, observationTypes []string, weight float64, keyPrefix string, features map[string]map[string]similarFeature) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(observationTypes)), ",")
	args := []any{id}
	for _, observationType := range observationTypes {
		args = append(args, observationType)
	}
	rows, err := db.QueryContext(ctx, `
select distinct target.asset_id, source.observation_type, source.label
from visual_observation source
join visual_observation target
  on target.observation_type = source.observation_type
 and target.label = source.label collate nocase
where source.asset_id = ?
  and source.observation_type in (`+placeholders+`)
  and trim(source.label) <> ''
  and target.asset_id <> source.asset_id
`, args...)
	if err != nil {
		return fmt.Errorf("load shared visual labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID, observationType, label string
		if err := rows.Scan(&assetID, &observationType, &label); err != nil {
			return err
		}
		key := keyPrefix + ":" + observationType + ":" + strings.ToLower(strings.TrimSpace(label))
		addSimilarFeature(features, assetID, similarFeature{key: key, display: label, weight: weight})
	}
	return rows.Err()
}

func addSharedPeopleFeatures(ctx context.Context, db *sql.DB, id string, features map[string]map[string]similarFeature) error {
	rows, err := db.QueryContext(ctx, `
select distinct target.asset_id, source.person_label
from face_observation source
join face_observation target on target.person_label = source.person_label collate nocase
where source.asset_id = ?
  and trim(source.person_label) <> ''
  and target.asset_id <> source.asset_id
`, id)
	if err != nil {
		return fmt.Errorf("load shared named people: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID, label string
		if err := rows.Scan(&assetID, &label); err != nil {
			return err
		}
		key := "person:" + strings.ToLower(strings.TrimSpace(label))
		addSimilarFeature(features, assetID, similarFeature{key: key, display: label, weight: similarPersonWeight})
	}
	return rows.Err()
}

func addSimilarFeature(features map[string]map[string]similarFeature, assetID string, feature similarFeature) {
	if features[assetID] == nil {
		features[assetID] = map[string]similarFeature{}
	}
	features[assetID][feature.key] = feature
}

func includeSimilarCandidate(seed, candidate fullAsset, signalSet signals, excluded map[string]bool, includeSameEvent bool) bool {
	if candidate.ID == seed.ID || candidate.MediaType != "image" || excludedMatch(candidate, excluded) {
		return false
	}
	if seed.burst != "" && candidate.burst == seed.burst {
		return false
	}
	if !includeSameEvent {
		delta := candidate.created.Sub(seed.created)
		if delta < 0 {
			delta = -delta
		}
		if delta <= similarEventWindow {
			return false
		}
	}
	if isScreenshotSubtype(candidate.subtypes) {
		return false
	}
	if blur, ok := signalSet.blurriness[candidate.ID]; ok && blur.Valid && blur.Float64 <= defaultBlurMax {
		return false
	}
	return true
}

func similarAsset(asset fullAsset, features map[string]similarFeature) SimilarAsset {
	ordered := make([]similarFeature, 0, len(features))
	score := 0.0
	for _, feature := range features {
		ordered = append(ordered, feature)
		score += feature.weight
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].weight != ordered[j].weight {
			return ordered[i].weight > ordered[j].weight
		}
		if ordered[i].display != ordered[j].display {
			return ordered[i].display < ordered[j].display
		}
		return ordered[i].key < ordered[j].key
	})
	shared := make([]string, 0, 5)
	seen := map[string]bool{}
	for _, feature := range ordered {
		normalized := strings.ToLower(strings.TrimSpace(feature.display))
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		shared = append(shared, feature.display)
		if len(shared) == 5 {
			break
		}
	}
	return SimilarAsset{AssetRow: asset.AssetRow, Score: score, Shared: shared}
}

func similarTermKey(termType, term string) string {
	return termType + "\x00" + term
}
