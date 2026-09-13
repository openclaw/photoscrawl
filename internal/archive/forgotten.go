package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

const defaultForgottenGapHours = 3

type ForgottenOptions struct {
	From, To, ExcludeIDsFile string
	Limit                    int
	GapHours                 float64
}

type ForgottenAsset struct {
	AssetRow
	Reasons []string `json:"reasons"`
}

type ForgottenResult struct {
	Threshold        float64          `json:"threshold"`
	GroupsConsidered int              `json:"groups_considered"`
	Assets           []ForgottenAsset `json:"assets"`
}

// Forgotten returns strong, uncurated images with at most one result from
// each time-separated moment. It reads only the archive.
func Forgotten(ctx context.Context, paths Paths, o ForgottenOptions) (ForgottenResult, error) {
	if o.GapHours < 0 {
		return ForgottenResult{}, errors.New("gap hours must not be negative")
	}
	gapHours := o.GapHours
	if gapHours == 0 {
		gapHours = defaultForgottenGapHours
	}
	from, err := parseDate(o.From, false)
	if err != nil {
		return ForgottenResult{}, err
	}
	to, err := parseDate(o.To, true)
	if err != nil {
		return ForgottenResult{}, err
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return ForgottenResult{}, err
	}
	defer db.Close()
	assets, err := loadAssets(ctx, db.DB())
	if err != nil {
		return ForgottenResult{}, err
	}
	s, err := loadSignals(ctx, db.DB())
	if err != nil {
		return ForgottenResult{}, err
	}
	excluded, err := readExcluded(o.ExcludeIDsFile)
	if err != nil {
		return ForgottenResult{}, err
	}
	inAlbums, err := loadUserAlbumAssets(ctx, db.DB())
	if err != nil {
		return ForgottenResult{}, err
	}

	var scored []float64
	for _, asset := range assets {
		if asset.MediaType == "image" {
			if score, ok := s.aesthetic[asset.ID]; ok && score.Valid {
				scored = append(scored, score.Float64)
			}
		}
	}
	threshold := percentile(scored, .9)
	result := ForgottenResult{Threshold: threshold, Assets: []ForgottenAsset{}}
	if len(scored) == 0 {
		return result, nil
	}

	var candidates []fullAsset
	for _, asset := range assets {
		aesthetic, scoredAsset := s.aesthetic[asset.ID]
		blurriness, hasBlurriness := s.blurriness[asset.ID]
		if asset.MediaType != "image" || asset.hidden != 0 || asset.favoriteBool() {
			continue
		}
		if excludedMatch(asset, excluded) || inAlbums[asset.ID] {
			continue
		}
		if isScreenshotSubtype(asset.subtypes) || hasBlurriness && blurriness.Float64 <= defaultBlurMax {
			continue
		}
		if allClosed(s.faces[asset.ID]) || !scoredAsset || !aesthetic.Valid || aesthetic.Float64 < threshold {
			continue
		}
		if from.Valid && asset.created.Before(from.Time) || to.Valid && asset.created.After(to.Time) {
			continue
		}
		candidates = append(candidates, asset)
	}
	s = s.withQualityMedians(candidates)

	groups := groupForgottenCandidates(candidates, time.Duration(gapHours*float64(time.Hour)))
	result.GroupsConsidered = len(groups)
	type selectedAsset struct {
		asset     fullAsset
		groupSize int
	}
	selected := make([]selectedAsset, 0, len(groups))
	for _, group := range groups {
		sortAssets(group, s, nil, true)
		selected = append(selected, selectedAsset{asset: group[0], groupSize: len(group)})
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return compareQuality(selected[i].asset, selected[j].asset, s, nil) < 0
	})
	limit := bounded(o.Limit, 12, 500)
	if len(selected) > limit {
		selected = selected[:limit]
	}
	for _, item := range selected {
		aesthetic := s.aesthetic[item.asset.ID].Float64
		result.Assets = append(result.Assets, ForgottenAsset{
			AssetRow: item.asset.AssetRow,
			Reasons: []string{
				fmt.Sprintf("overall aesthetic %.2f (90th percentile %.2f)", aesthetic, threshold),
				fmt.Sprintf("best of %d qualifying photos in moment", item.groupSize),
				"not favorited and not in a user or shared album",
			},
		})
	}
	return result, nil
}

func loadUserAlbumAssets(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `select distinct asset_id from album_membership where album_kind glob 'album:1:*'`)
	if err != nil {
		return nil, fmt.Errorf("load user and shared album memberships: %w", err)
	}
	defer rows.Close()
	assets := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		assets[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, nil
}

func groupForgottenCandidates(assets []fullAsset, gap time.Duration) [][]fullAsset {
	sort.SliceStable(assets, func(i, j int) bool {
		if !assets[i].created.Equal(assets[j].created) {
			return assets[i].created.Before(assets[j].created)
		}
		return assets[i].ID < assets[j].ID
	})
	groups := [][]fullAsset{}
	var current []fullAsset
	for _, asset := range assets {
		if len(current) > 0 && asset.created.Sub(current[len(current)-1].created) > gap {
			groups = append(groups, current)
			current = nil
		}
		current = append(current, asset)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}
