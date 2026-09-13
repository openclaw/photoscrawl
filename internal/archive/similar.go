package archive

import (
	"context"
	"database/sql"
	"encoding/json"
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
	similarEventWindow      = 12 * time.Hour
	similarCandidateChunk   = 500
	// similarShortlist caps how many candidates load full asset and quality
	// data; broad matches otherwise pull in tens of thousands of photos.
	similarShortlist = 400
)

var similarNoisePolicy = struct {
	maxDocumentFraction float64
	zeroWeightTerms     []string
}{
	maxDocumentFraction: 0.05,
	zeroWeightTerms: []string{
		"arm", "face", "finger", "hand", "man", "people", "person", "tattoo", "tattooed", "woman",
	},
}

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

type similarSeedTerm struct {
	termType string
	term     string
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
	documentCount, err := loadSimilarDocumentCount(ctx, db.DB())
	if err != nil {
		return SimilarResult{}, err
	}
	seedTerms, err := loadSimilarSeedTerms(ctx, db.DB(), id)
	if err != nil {
		return SimilarResult{}, err
	}
	weightedTerms, err := loadSimilarTermWeights(ctx, db.DB(), seedTerms, documentCount)
	if err != nil {
		return SimilarResult{}, err
	}
	features := map[string]map[string]similarFeature{}
	if err := addSharedTermFeatures(ctx, db.DB(), id, weightedTerms, features); err != nil {
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

	features = pruneSimilarFeatures(features, documentCount, 0)
	assets, err := loadSimilarAssets(ctx, db.DB(), similarCandidateIDs(features))
	if err != nil {
		return SimilarResult{}, err
	}
	excluded, err := readExcluded(opts.ExcludeIDsFile)
	if err != nil {
		return SimilarResult{}, err
	}
	assetByID := make(map[string]fullAsset, len(assets))
	eligibleFeatures := make(map[string]map[string]similarFeature, len(assets))
	for _, asset := range assets {
		if !includeSimilarAssetCandidate(seed, asset, excluded, opts.IncludeSameEvent) {
			continue
		}
		assetByID[asset.ID] = asset
		eligibleFeatures[asset.ID] = features[asset.ID]
	}
	features = shortlistSimilarFeatures(eligibleFeatures, similarShortlist)
	shortlistIDs := similarCandidateIDs(features)
	signalSet, err := loadSimilarSignals(ctx, db.DB(), shortlistIDs)
	if err != nil {
		return SimilarResult{}, err
	}

	candidates := make([]SimilarAsset, 0, len(shortlistIDs))
	qualityInput := make([]fullAsset, 0, len(shortlistIDs))
	qualityAssets := make(map[string]fullAsset, len(features))
	for _, candidateID := range shortlistIDs {
		asset := assetByID[candidateID]
		if !includeSimilarSignalCandidate(asset, signalSet) {
			continue
		}
		qualityAssets[asset.ID] = asset
		qualityInput = append(qualityInput, asset)
		candidates = append(candidates, similarAsset(asset, features[asset.ID]))
	}
	signalSet = signalSet.withQualityMedians(qualityInput)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		a := qualityAssets[candidates[i].ID]
		b := qualityAssets[candidates[j].ID]
		if quality := compareQuality(a, b, signalSet, nil); quality != 0 {
			return quality < 0
		}
		return false
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

func loadSimilarDocumentCount(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*) from asset where deleted_at is null and media_type = 'image'`).Scan(&count)
	return count, err
}

func loadSimilarSeedTerms(ctx context.Context, db *sql.DB, id string) ([]similarSeedTerm, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(similarTermTypes)), ",")
	args := []any{id}
	for _, termType := range similarTermTypes {
		args = append(args, termType)
	}
	rows, err := db.QueryContext(ctx, `
select observation_id, term_type, trim(term)
from observation_term
where asset_id = ? and term_type in (`+placeholders+`) and trim(term) <> ''
`, args...)
	if err != nil {
		return nil, fmt.Errorf("load similar seed terms: %w", err)
	}
	defer rows.Close()
	type occurrence struct {
		observationID string
		similarSeedTerm
	}
	var occurrences []occurrence
	compounds := map[string]map[string]bool{}
	for rows.Next() {
		var item occurrence
		if err := rows.Scan(&item.observationID, &item.termType, &item.term); err != nil {
			return nil, err
		}
		occurrences = append(occurrences, item)
		if strings.Contains(item.term, "_") {
			if compounds[item.observationID] == nil {
				compounds[item.observationID] = map[string]bool{}
			}
			for _, component := range strings.Split(strings.ToLower(item.term), "_") {
				compounds[item.observationID][component] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	terms := make([]similarSeedTerm, 0, len(occurrences))
	for _, item := range occurrences {
		normalized := strings.ToLower(item.term)
		if similarZeroWeightTerm(normalized) || (!strings.Contains(normalized, "_") && compounds[item.observationID][normalized]) {
			continue
		}
		key := similarTermKey(item.termType, item.term)
		if !seen[key] {
			seen[key] = true
			terms = append(terms, item.similarSeedTerm)
		}
	}
	return terms, nil
}

func loadSimilarTermWeights(ctx context.Context, db *sql.DB, terms []similarSeedTerm, documentCount int) (map[string]float64, error) {
	weights := map[string]float64{}
	for _, term := range terms {
		var frequency int
		err := db.QueryRowContext(ctx, `
select count(distinct observation_term.asset_id)
from observation_term
join asset on asset.id = observation_term.asset_id
where observation_term.term = ? and observation_term.term_type = ?
  and asset.deleted_at is null and asset.media_type = 'image'
`, term.term, term.termType).Scan(&frequency)
		if err != nil {
			return nil, fmt.Errorf("load frequency for similar term %q: %w", term.term, err)
		}
		if documentCount == 0 || float64(frequency)/float64(documentCount) > similarNoisePolicy.maxDocumentFraction {
			continue
		}
		weights[similarTermKey(term.termType, term.term)] = math.Log(float64(documentCount+1)/float64(frequency+1)) + 1
	}
	return weights, nil
}

func addSharedTermFeatures(ctx context.Context, db *sql.DB, id string, weights map[string]float64, features map[string]map[string]similarFeature) error {
	for key, weight := range weights {
		termType, term, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		query := `
select distinct asset_id
from observation_term
where term = ? and term_type = ? and asset_id <> ?
`
		if !strings.Contains(term, "_") {
			query = `
select distinct target.asset_id
from observation_term target
where target.term = ? and target.term_type = ? and target.asset_id <> ?
  and not exists (
    select 1
    from observation_term compound
    where compound.asset_id = target.asset_id
      and compound.observation_id = target.observation_id
      and instr(compound.term, '_') > 0
      and instr('_' || lower(trim(compound.term)) || '_', '_' || lower(trim(target.term)) || '_') > 0
  )
`
		}
		rows, err := db.QueryContext(ctx, query, term, termType, id)
		if err != nil {
			return fmt.Errorf("load candidates for similar term %q: %w", term, err)
		}
		for rows.Next() {
			var assetID string
			if err := rows.Scan(&assetID); err != nil {
				rows.Close()
				return err
			}
			addSimilarFeature(features, assetID, similarFeature{key: "term:" + key, display: term, weight: weight})
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
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

// pruneSimilarFeatures drops shared labels and people that appear on more than
// the noise fraction of the library (Apple labels such as "Food" or the
// library owner's own face). Terms are already frequency-capped when their
// weights are computed. If requested, the shortlist is applied after pruning.
func pruneSimilarFeatures(features map[string]map[string]similarFeature, documentCount, shortlist int) map[string]map[string]similarFeature {
	keyCounts := map[string]int{}
	for _, assetFeatures := range features {
		for key := range assetFeatures {
			keyCounts[key]++
		}
	}
	limit := similarNoisePolicy.maxDocumentFraction * float64(documentCount)
	pruned := make(map[string]map[string]similarFeature, len(features))
	for id, assetFeatures := range features {
		kept := map[string]similarFeature{}
		for key, feature := range assetFeatures {
			if !strings.HasPrefix(key, "term:") && float64(keyCounts[key]) > limit {
				continue
			}
			kept[key] = feature
		}
		if len(kept) == 0 {
			continue
		}
		pruned[id] = kept
	}
	return shortlistSimilarFeatures(pruned, shortlist)
}

func shortlistSimilarFeatures(features map[string]map[string]similarFeature, shortlist int) map[string]map[string]similarFeature {
	if shortlist <= 0 || len(features) <= shortlist {
		return features
	}
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(features))
	for id, assetFeatures := range features {
		score := 0.0
		for _, feature := range assetFeatures {
			score += feature.weight
		}
		ranked = append(ranked, scored{id: id, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	shortlisted := make(map[string]map[string]similarFeature, shortlist)
	for _, entry := range ranked[:shortlist] {
		shortlisted[entry.id] = features[entry.id]
	}
	return shortlisted
}

func addSimilarFeature(features map[string]map[string]similarFeature, assetID string, feature similarFeature) {
	if features[assetID] == nil {
		features[assetID] = map[string]similarFeature{}
	}
	features[assetID][feature.key] = feature
}

func similarZeroWeightTerm(term string) bool {
	for _, stopped := range similarNoisePolicy.zeroWeightTerms {
		if term == stopped {
			return true
		}
	}
	return false
}

func similarCandidateIDs(features map[string]map[string]similarFeature) []string {
	ids := make([]string, 0, len(features))
	for id := range features {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func loadSimilarAssets(ctx context.Context, db *sql.DB, ids []string) ([]fullAsset, error) {
	assets := make([]fullAsset, 0, len(ids))
	for start := 0; start < len(ids); start += similarCandidateChunk {
		end := min(start+similarCandidateChunk, len(ids))
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, `
select id, local_identifier, creation_date, media_type, hidden, favorite, width, height,
       burst_identifier, media_subtypes, timezone_name
from asset
where deleted_at is null and id in (`+placeholders+`)
`, args...)
		if err != nil {
			return nil, fmt.Errorf("load similar candidate assets: %w", err)
		}
		for rows.Next() {
			var asset fullAsset
			var created string
			if err := rows.Scan(&asset.ID, &asset.LocalIdentifier, &created, &asset.MediaType, &asset.hidden, &asset.favorite, &asset.width, &asset.height, &asset.burst, &asset.subtypes, &asset.tz); err != nil {
				rows.Close()
				return nil, err
			}
			asset.CreationDate = created
			asset.created, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("parse creation date for asset %q: %w", asset.ID, err)
			}
			assets = append(assets, asset)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return assets, nil
}

func loadSimilarSignals(ctx context.Context, db *sql.DB, ids []string) (signals, error) {
	s := signals{faces: map[string][]faceSignal{}, aesthetic: map[string]sql.NullFloat64{}, focus: map[string]sql.NullFloat64{}, blurriness: map[string]sql.NullFloat64{}}
	for start := 0; start < len(ids); start += similarCandidateChunk {
		end := min(start+similarCandidateChunk, len(ids))
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, `select asset_id, person_label, quality, eyes_closed from face_observation where trim(person_label) <> '' and asset_id in (`+placeholders+`)`, args...)
		if err != nil {
			return s, fmt.Errorf("load similar candidate face signals: %w", err)
		}
		for rows.Next() {
			var id string
			var face faceSignal
			if err := rows.Scan(&id, &face.label, &face.quality, &face.closed); err != nil {
				rows.Close()
				return s, err
			}
			s.faces[id] = append(s.faces[id], face)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return s, err
		}
		if err := rows.Close(); err != nil {
			return s, err
		}
		rows, err = db.QueryContext(ctx, `select asset_id, value_json from model_observation where observation_type = 'apple_quality_scores' and asset_id in (`+placeholders+`)`, args...)
		if err != nil {
			return s, fmt.Errorf("load similar candidate quality signals: %w", err)
		}
		for rows.Next() {
			var id, value string
			if err := rows.Scan(&id, &value); err != nil {
				rows.Close()
				return s, err
			}
			var scores map[string]any
			if err := json.Unmarshal([]byte(value), &scores); err != nil {
				rows.Close()
				return s, fmt.Errorf("parse quality scores for asset %q: %w", id, err)
			}
			for _, pair := range []struct {
				name   string
				values *map[string]sql.NullFloat64
			}{
				{"overall_aesthetic", &s.aesthetic},
				{"sharply_focused_subject", &s.focus},
				{"media_blurriness", &s.blurriness},
			} {
				if value, ok := number(scores[pair.name]); ok {
					(*pair.values)[id] = sql.NullFloat64{Float64: value, Valid: true}
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return s, err
		}
		if err := rows.Close(); err != nil {
			return s, err
		}
	}
	return s, nil
}

func includeSimilarAssetCandidate(seed, candidate fullAsset, excluded map[string]bool, includeSameEvent bool) bool {
	if candidate.ID == seed.ID || candidate.MediaType != "image" || candidate.hidden != 0 || excludedMatch(candidate, excluded) {
		return false
	}
	if !includeSameEvent {
		if seed.burst != "" && candidate.burst == seed.burst {
			return false
		}
		delta := candidate.created.Sub(seed.created)
		if delta < 0 {
			delta = -delta
		}
		if delta <= similarEventWindow {
			return false
		}
		location := time.UTC
		if seed.tz != "" {
			if candidateLocation, err := time.LoadLocation(seed.tz); err == nil {
				location = candidateLocation
			}
		}
		if candidate.created.In(location).Format("2006-01-02") == seed.created.In(location).Format("2006-01-02") {
			return false
		}
	}
	if isScreenshotSubtype(candidate.subtypes) {
		return false
	}
	return true
}

func includeSimilarSignalCandidate(candidate fullAsset, signalSet signals) bool {
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
