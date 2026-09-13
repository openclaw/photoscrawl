package archive

// Read-only curation queries used by people, find, rank, and junk.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AssetRow struct {
	ID              string `json:"id"`
	LocalIdentifier string `json:"local_identifier"`
	CreationDate    string `json:"creation_date"`
	MediaType       string `json:"media_type"`
}
type PeopleResult struct {
	People []Person `json:"people"`
}
type Person struct {
	Label      string `json:"label"`
	PersonUUID string `json:"person_uuid"`
	Kind       string `json:"kind"`
	FaceCount  int    `json:"face_count"`
	AssetCount int    `json:"asset_count"`
}
type FindOptions struct {
	People                                              []string
	From, To, Place, Query, Media, Rank, ExcludeIDsFile string
	IncludeHidden                                       bool
	Limit                                               int
}
type FindResult struct {
	Assets []AssetRow `json:"assets"`
	Limit  int        `json:"limit"`
}
type RankOptions struct {
	FindOptions
	IDsFile, Group       string
	GapSeconds, PerGroup int
}
type RankAsset struct {
	AssetRow
	Reasons        []string       `json:"reasons"`
	ScoreBreakdown map[string]any `json:"score_breakdown"`
}
type RankGroup struct {
	Key    string      `json:"key"`
	Assets []RankAsset `json:"assets"`
	BestID string      `json:"best_id"`
}
type RankResult struct {
	Groups []RankGroup `json:"groups"`
}
type JunkOptions struct {
	Kind, OlderThan, ExcludeIDsFile string
	Limit                           int
	// BlurMax is the highest Photos blurriness score still flagged as
	// blurry; zero selects the default.
	BlurMax float64
}

const defaultBlurMax = 0.3

func (o JunkOptions) blurMax() float64 {
	if o.BlurMax <= 0 {
		return defaultBlurMax
	}
	return o.BlurMax
}

type JunkCandidate struct {
	AssetRow
	Kind        string         `json:"kind"`
	Reason      string         `json:"reason"`
	Signals     map[string]any `json:"signals"`
	KeepInstead string         `json:"keep_instead,omitempty"`
}
type JunkResult struct {
	Candidates  []JunkCandidate    `json:"candidates"`
	Percentiles map[string]float64 `json:"percentiles,omitempty"`
}

type faceSignal struct {
	label   string
	quality sql.NullFloat64
	closed  sql.NullInt64
}
type signals struct {
	faces            map[string][]faceSignal
	aesthetic, focus map[string]sql.NullFloat64
	blurriness       map[string]sql.NullFloat64
}
type fullAsset struct {
	AssetRow
	hidden, favorite, width, height int
	burst, subtypes, tz             string
	created                         time.Time
}

func People(ctx context.Context, paths Paths) (PeopleResult, error) {
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return PeopleResult{}, err
	}
	defer db.Close()
	rs, err := db.DB().QueryContext(ctx, `select person_label, coalesce(person_uuid,''), coalesce(person_kind,''), count(*), count(distinct asset_id) from face_observation where source = ? and trim(person_label) <> '' group by person_label, person_uuid, person_kind order by count(distinct asset_id) desc, person_label`, photosLibraryDBFaceSource)
	if err != nil {
		return PeopleResult{}, err
	}
	defer rs.Close()
	out := PeopleResult{People: []Person{}}
	for rs.Next() {
		var p Person
		if err = rs.Scan(&p.Label, &p.PersonUUID, &p.Kind, &p.FaceCount, &p.AssetCount); err != nil {
			return PeopleResult{}, err
		}
		out.People = append(out.People, p)
	}
	return out, rs.Err()
}

func Find(ctx context.Context, paths Paths, o FindOptions) (FindResult, error) {
	if o.Rank != "" && o.Rank != "date" && o.Rank != "quality" {
		return FindResult{}, errors.New("rank must be quality or date")
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return FindResult{}, err
	}
	defer db.Close()
	as, err := loadAssets(ctx, db.DB())
	if err != nil {
		return FindResult{}, err
	}
	s, err := loadSignals(ctx, db.DB())
	if err != nil {
		return FindResult{}, err
	}
	excluded, err := readExcluded(o.ExcludeIDsFile)
	if err != nil {
		return FindResult{}, err
	}
	as, err = filterAssets(ctx, db.DB(), as, s, o, excluded)
	if err != nil {
		return FindResult{}, err
	}
	sortAssets(as, s, o.People, o.Rank == "quality")
	limit := bounded(o.Limit, 50, 500)
	out := FindResult{Limit: limit, Assets: []AssetRow{}}
	for i, a := range as {
		if i == limit {
			break
		}
		out.Assets = append(out.Assets, a.AssetRow)
	}
	return out, nil
}

func Rank(ctx context.Context, paths Paths, o RankOptions) (RankResult, error) {
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return RankResult{}, err
	}
	defer db.Close()
	as, err := loadAssets(ctx, db.DB())
	if err != nil {
		return RankResult{}, err
	}
	s, err := loadSignals(ctx, db.DB())
	if err != nil {
		return RankResult{}, err
	}
	ex, err := readExcluded(o.ExcludeIDsFile)
	if err != nil {
		return RankResult{}, err
	}
	if o.IDsFile != "" {
		ids, e := readIDs(o.IDsFile)
		if e != nil {
			return RankResult{}, e
		}
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		as = filterIDAssets(as, wanted)
	} else {
		as, err = filterAssets(ctx, db.DB(), as, s, o.FindOptions, ex)
		if err != nil {
			return RankResult{}, err
		}
	}
	as = excludeAssets(as, ex)
	sortAssets(as, s, o.People, true)
	return groupRank(as, s, o)
}

func Junk(ctx context.Context, paths Paths, o JunkOptions) (JunkResult, error) {
	kind := o.Kind
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "screenshots" && kind != "blurry" && kind != "eyes-closed" && kind != "duplicates" {
		return JunkResult{}, errors.New("kind must be screenshots, blurry, eyes-closed, duplicates, or all")
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return JunkResult{}, err
	}
	defer db.Close()
	as, err := loadAssets(ctx, db.DB())
	if err != nil {
		return JunkResult{}, err
	}
	s, err := loadSignals(ctx, db.DB())
	if err != nil {
		return JunkResult{}, err
	}
	ex, err := readExcluded(o.ExcludeIDsFile)
	if err != nil {
		return JunkResult{}, err
	}
	as = excludeAssets(as, ex)
	older, err := parseDuration(o.OlderThan)
	if err != nil {
		return JunkResult{}, err
	}
	cutoff := time.Now().Add(-older)
	out := JunkResult{Candidates: []JunkCandidate{}}
	limit := bounded(o.Limit, 200, 2000)
	add := func(c JunkCandidate) {
		if len(out.Candidates) < limit {
			out.Candidates = append(out.Candidates, c)
		}
	}
	if kind == "screenshots" || kind == "all" {
		for _, a := range as {
			if a.MediaType == "image" && !a.favoriteBool() && (strings.Contains(a.subtypes, "kind_subtype:10") || isScreenshotSubtype(a.subtypes)) && a.created.Before(cutoff) {
				add(JunkCandidate{AssetRow: a.AssetRow, Kind: "screenshots", Reason: "old non-favorite screenshot", Signals: map[string]any{"media_subtypes": a.subtypes}})
			}
		}
	}
	if kind == "blurry" || kind == "all" {
		// Photos' media-analysis blurriness is 1 for sharp photos and near 0
		// for visibly blurred ones. Low aesthetic or subject-focus scores are
		// not blur: they also flag sharp but ordinary photos.
		faceMedian := namedFaceQualityMedian(as, s)
		out.Percentiles = map[string]float64{"media_blurriness_max": o.blurMax(), "named_face_quality_median": faceMedian}
		for _, a := range as {
			b, ok := s.blurriness[a.ID]
			if a.MediaType != "image" || !ok || b.Float64 > o.blurMax() || hasFaceQualityAbove(s.faces[a.ID], faceMedian) {
				continue
			}
			add(JunkCandidate{AssetRow: a.AssetRow, Kind: "blurry", Reason: fmt.Sprintf("Photos blurriness score %.2f (1 is sharp)", b.Float64), Signals: map[string]any{"media_blurriness": b.Float64}})
		}
	}
	if kind == "eyes-closed" || kind == "all" {
		for _, pair := range closedEyeSiblings(as, s) {
			a := pair.closed
			add(JunkCandidate{AssetRow: a.AssetRow, Kind: "eyes-closed", Reason: "all named faces have eyes closed", Signals: map[string]any{"named_faces": len(s.faces[a.ID])}, KeepInstead: pair.open.ID})
		}
	}
	if kind == "duplicates" || kind == "all" {
		groups, err := duplicateGroups(ctx, db.DB(), as)
		if err != nil {
			return JunkResult{}, err
		}
		for _, g := range groups {
			sort.Slice(g, func(i, j int) bool { return keeperBefore(g[i], g[j], s) })
			for _, a := range g[1:] {
				add(JunkCandidate{AssetRow: a.AssetRow, Kind: "duplicates", Reason: "duplicate group", Signals: map[string]any{}, KeepInstead: g[0].ID})
			}
		}
	}
	return out, nil
}

func loadAssets(ctx context.Context, db *sql.DB) ([]fullAsset, error) {
	rs, e := db.QueryContext(ctx, `select id,local_identifier,creation_date,media_type,hidden,favorite,width,height,burst_identifier,media_subtypes,timezone_name from asset where deleted_at is null`)
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	out := []fullAsset{}
	for rs.Next() {
		var a fullAsset
		var d string
		if e = rs.Scan(&a.ID, &a.LocalIdentifier, &d, &a.MediaType, &a.hidden, &a.favorite, &a.width, &a.height, &a.burst, &a.subtypes, &a.tz); e != nil {
			return nil, e
		}
		a.CreationDate = d
		a.created, e = time.Parse(time.RFC3339Nano, d)
		if e != nil {
			return nil, fmt.Errorf("parse creation date for asset %q: %w", a.ID, e)
		}
		out = append(out, a)
	}
	return out, rs.Err()
}
func loadSignals(ctx context.Context, db *sql.DB) (signals, error) {
	s := signals{faces: map[string][]faceSignal{}, aesthetic: map[string]sql.NullFloat64{}, focus: map[string]sql.NullFloat64{}, blurriness: map[string]sql.NullFloat64{}}
	r, e := db.QueryContext(ctx, `select asset_id,person_label,quality,eyes_closed from face_observation where trim(person_label)<>''`)
	if e != nil {
		return s, e
	}
	for r.Next() {
		var id string
		var f faceSignal
		if e = r.Scan(&id, &f.label, &f.quality, &f.closed); e != nil {
			r.Close()
			return s, e
		}
		s.faces[id] = append(s.faces[id], f)
	}
	if e = r.Err(); e != nil {
		r.Close()
		return s, e
	}
	if e = r.Close(); e != nil {
		return s, e
	}
	r, e = db.QueryContext(ctx, `select asset_id,value_json from model_observation where observation_type='apple_quality_scores'`)
	if e != nil {
		return s, e
	}
	for r.Next() {
		var id, v string
		if e = r.Scan(&id, &v); e != nil {
			r.Close()
			return s, e
		}
		var m map[string]any
		if e = json.Unmarshal([]byte(v), &m); e != nil {
			r.Close()
			return s, fmt.Errorf("parse quality scores for asset %q: %w", id, e)
		}
		for _, pair := range []struct {
			k string
			p *map[string]sql.NullFloat64
		}{{"overall_aesthetic", &s.aesthetic}, {"sharply_focused_subject", &s.focus}, {"media_blurriness", &s.blurriness}} {
			if x, ok := number(m[pair.k]); ok {
				(*pair.p)[id] = sql.NullFloat64{Float64: x, Valid: true}
			}
		}
	}
	if e = r.Err(); e != nil {
		r.Close()
		return s, e
	}
	return s, r.Close()
}
func filterAssets(ctx context.Context, db *sql.DB, as []fullAsset, s signals, o FindOptions, ex map[string]bool) ([]fullAsset, error) {
	from, e := parseDate(o.From, false)
	if e != nil {
		return nil, e
	}
	to, e := parseDate(o.To, true)
	if e != nil {
		return nil, e
	}
	media := o.Media
	if media == "" {
		media = "image"
	}
	if media != "image" && media != "video" && media != "any" {
		return nil, errors.New("media must be image, video, or any")
	}
	textIDs := map[string]bool{}
	if strings.TrimSpace(o.Query) != "" {
		rs, e := db.QueryContext(ctx, `select id from asset_fts where asset_fts match ? union select distinct asset_id from observation_fts where observation_fts match ?`, ftsQuery(o.Query), ftsQuery(o.Query))
		if e != nil {
			return nil, e
		}
		for rs.Next() {
			var id string
			if e = rs.Scan(&id); e != nil {
				rs.Close()
				return nil, e
			}
			textIDs[id] = true
		}
		if e = rs.Err(); e != nil {
			rs.Close()
			return nil, e
		}
		if e = rs.Close(); e != nil {
			return nil, e
		}
	}
	placeIDs := map[string]bool{}
	if o.Place != "" {
		rs, e := db.QueryContext(ctx, `select distinct asset_id from visual_observation where observation_type in ('apple_place','apple_venue') and lower(label) like lower(?)`, "%"+o.Place+"%")
		if e != nil {
			return nil, e
		}
		for rs.Next() {
			var id string
			if e = rs.Scan(&id); e != nil {
				rs.Close()
				return nil, e
			}
			placeIDs[id] = true
		}
		if e = rs.Err(); e != nil {
			rs.Close()
			return nil, e
		}
		if e = rs.Close(); e != nil {
			return nil, e
		}
	}
	out := []fullAsset{}
	for _, a := range as {
		if excludedMatch(a, ex) || (!o.IncludeHidden && a.hidden != 0) || (media != "any" && a.MediaType != media) || (from.Valid && a.created.Before(from.Time)) || (to.Valid && a.created.After(to.Time)) || (o.Query != "" && !textIDs[a.ID]) || !peopleMatch(s.faces[a.ID], o.People) {
			continue
		}
		if o.Place != "" && !placeIDs[a.ID] {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}
func sortAssets(as []fullAsset, s signals, people []string, quality bool) {
	sort.SliceStable(as, func(i, j int) bool {
		if quality {
			if c := compareQuality(as[i], as[j], s, people); c != 0 {
				return c < 0
			}
		}
		if !as[i].created.Equal(as[j].created) {
			return as[i].created.After(as[j].created)
		}
		return as[i].ID < as[j].ID
	})
}
func compareQuality(a, b fullAsset, s signals, people []string) int {
	if len(people) > 0 {
		if x, y := openRequested(s.faces[a.ID], people), openRequested(s.faces[b.ID], people); x != y {
			return y - x
		}
	}
	if x, xok := closedCountKnown(s.faces[a.ID]); xok {
		if y, yok := closedCountKnown(s.faces[b.ID]); yok && x != y {
			return x - y
		}
	}
	if x, xok := lowestQuality(s.faces[a.ID]); xok {
		if y, yok := lowestQuality(s.faces[b.ID]); yok && x != y {
			return cmpFloatDesc(x, y)
		}
	}
	for _, m := range []map[string]sql.NullFloat64{s.aesthetic, s.focus} {
		x, xok := m[a.ID]
		y, yok := m[b.ID]
		if xok && yok && x.Float64 != y.Float64 {
			return cmpFloatDesc(x.Float64, y.Float64)
		}
	}
	return 0
}
func groupRank(as []fullAsset, s signals, o RankOptions) (RankResult, error) {
	g := o.Group
	if g == "" {
		g = "none"
	}
	if g != "none" && g != "burst" && g != "time" && g != "day" {
		return RankResult{}, errors.New("group must be none, burst, time, or day")
	}
	buckets := [][]fullAsset{}
	if g == "none" {
		buckets = append(buckets, as)
	} else if g == "burst" {
		m := map[string][]fullAsset{}
		for _, a := range as {
			k := a.burst
			if k == "" {
				k = "asset:" + a.ID
			}
			m[k] = append(m[k], a)
		}
		for _, b := range m {
			buckets = append(buckets, b)
		}
	} else {
		sort.Slice(as, func(i, j int) bool { return as[i].created.Before(as[j].created) })
		var cur []fullAsset
		for _, a := range as {
			split := len(cur) > 0 && (g == "day" && dayKey(cur[len(cur)-1]) != dayKey(a) || g == "time" && a.created.Sub(cur[len(cur)-1].created) > time.Duration(defaultInt(o.GapSeconds, 90))*time.Second)
			if split {
				buckets = append(buckets, cur)
				cur = nil
			}
			cur = append(cur, a)
		}
		if len(cur) > 0 {
			buckets = append(buckets, cur)
		}
	}
	out := RankResult{Groups: []RankGroup{}}
	for _, b := range buckets {
		sortAssets(b, s, o.People, true)
		n := o.PerGroup
		if n > 0 && n < len(b) {
			b = b[:n]
		}
		if len(b) == 0 {
			continue
		}
		rg := RankGroup{Key: groupKey(g, earliestAsset(b)), BestID: b[0].ID, Assets: []RankAsset{}}
		for _, a := range b {
			rg.Assets = append(rg.Assets, rankAsset(a, s, o.People))
		}
		out.Groups = append(out.Groups, rg)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Key < out.Groups[j].Key })
	return out, nil
}
func rankAsset(a fullAsset, s signals, p []string) RankAsset {
	z := RankAsset{AssetRow: a.AssetRow, Reasons: []string{}, ScoreBreakdown: map[string]any{}}
	if v := openRequested(s.faces[a.ID], p); len(p) > 0 {
		z.ScoreBreakdown["requested_people_open"] = v
		if v == len(p) {
			z.Reasons = append(z.Reasons, fmt.Sprintf("all %d requested people have eyes open", v))
		} else {
			z.Reasons = append(z.Reasons, fmt.Sprintf("%d requested people have eyes open", v))
		}
	}
	z.ScoreBreakdown["named_faces_eyes_closed"] = closedCount(s.faces[a.ID])
	if v := closedCount(s.faces[a.ID]); v > 0 {
		word := "faces"
		if v == 1 {
			word = "face"
		}
		z.Reasons = append(z.Reasons, fmt.Sprintf("%d named %s have eyes closed", v, word))
	}
	if v, ok := lowestQuality(s.faces[a.ID]); ok {
		z.ScoreBreakdown["lowest_named_face_quality"] = v
		z.Reasons = append(z.Reasons, fmt.Sprintf("lowest named-face quality %.2f", v))
	}
	if v, ok := s.aesthetic[a.ID]; ok {
		z.ScoreBreakdown["overall_aesthetic"] = v.Float64
		z.Reasons = append(z.Reasons, fmt.Sprintf("overall aesthetic %.2f", v.Float64))
	}
	if v, ok := s.focus[a.ID]; ok {
		z.ScoreBreakdown["sharply_focused_subject"] = v.Float64
		z.Reasons = append(z.Reasons, fmt.Sprintf("sharply focused subject %.2f", v.Float64))
	}
	return z
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, e := x.Float64()
		return f, e == nil
	case string:
		f, e := strconv.ParseFloat(x, 64)
		return f, e == nil
	}
	return 0, false
}
func bounded(v, d, max int) int {
	if v <= 0 {
		return d
	}
	if v > max {
		return max
	}
	return v
}
func defaultInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}
func parseDate(x string, end bool) (sql.NullTime, error) {
	if strings.TrimSpace(x) == "" {
		return sql.NullTime{}, nil
	}
	if t, e := time.Parse(time.RFC3339, x); e == nil {
		return sql.NullTime{Time: t, Valid: true}, nil
	}
	if t, e := time.Parse("2006-01-02", x); e == nil {
		if end {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return sql.NullTime{Time: t, Valid: true}, nil
	}
	return sql.NullTime{}, fmt.Errorf("invalid date %q (use RFC 3339 or YYYY-MM-DD)", x)
}
func parseDuration(x string) (time.Duration, error) {
	if x == "" {
		x = "30d"
	}
	if strings.HasSuffix(x, "d") {
		n, e := strconv.ParseInt(strings.TrimSuffix(x, "d"), 10, 64)
		if e != nil || n < 0 {
			return 0, fmt.Errorf("invalid day duration %q", x)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, e := time.ParseDuration(x)
	if e != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q", x)
	}
	return d, nil
}
func readIDs(path string) ([]string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var ids []string
	if json.Unmarshal(b, &ids) == nil {
		return ids, nil
	}
	for _, x := range strings.Split(string(b), "\n") {
		if x = strings.TrimSpace(x); x != "" {
			ids = append(ids, x)
		}
	}
	return ids, nil
}
func readExcluded(path string) (map[string]bool, error) {
	m := map[string]bool{}
	if path == "" {
		return m, nil
	}
	ids, e := readIDs(path)
	for _, id := range ids {
		m[id] = true
	}
	return m, e
}
func excludedMatch(a fullAsset, m map[string]bool) bool {
	if m[a.ID] || m[a.LocalIdentifier] {
		return true
	}
	return m[normalizeAssetLocalIdentifier(a.LocalIdentifier)]
}
func excludeAssets(as []fullAsset, m map[string]bool) []fullAsset {
	o := []fullAsset{}
	for _, a := range as {
		if !excludedMatch(a, m) {
			o = append(o, a)
		}
	}
	return o
}
func filterIDAssets(as []fullAsset, w map[string]bool) []fullAsset {
	o := []fullAsset{}
	for _, a := range as {
		if w[a.ID] || w[a.LocalIdentifier] || w[normalizeAssetLocalIdentifier(a.LocalIdentifier)] {
			o = append(o, a)
		}
	}
	return o
}
func peopleMatch(fs []faceSignal, ps []string) bool {
	for _, p := range ps {
		found := false
		for _, f := range fs {
			if strings.EqualFold(strings.TrimSpace(f.label), strings.TrimSpace(p)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func openRequested(fs []faceSignal, ps []string) int {
	n := 0
	for _, p := range ps {
		for _, f := range fs {
			if strings.EqualFold(f.label, p) && (!f.closed.Valid || f.closed.Int64 == 0) {
				n++
				break
			}
		}
	}
	return n
}
func closedCount(fs []faceSignal) int {
	n := 0
	for _, f := range fs {
		if f.closed.Valid && f.closed.Int64 == 1 {
			n++
		}
	}
	return n
}
func closedCountKnown(fs []faceSignal) (int, bool) {
	n := 0
	known := false
	for _, f := range fs {
		if f.closed.Valid {
			known = true
			if f.closed.Int64 == 1 {
				n++
			}
		}
	}
	return n, known
}
func lowestQuality(fs []faceSignal) (float64, bool) {
	var x float64
	ok := false
	for _, f := range fs {
		if f.quality.Valid && (!ok || f.quality.Float64 < x) {
			x = f.quality.Float64
			ok = true
		}
	}
	return x, ok
}
func cmpFloatDesc(x, y float64) int {
	if x > y {
		return -1
	}
	if x < y {
		return 1
	}
	return 0
}
func (a fullAsset) favoriteBool() bool { return a.favorite != 0 }
func isScreenshotSubtype(x string) bool {
	n, e := strconv.ParseUint(strings.TrimSpace(x), 0, 64)
	return e == nil && n&(1<<2) != 0
}
func allClosed(fs []faceSignal) bool {
	if len(fs) == 0 {
		return false
	}
	for _, f := range fs {
		if !f.closed.Valid || f.closed.Int64 != 1 {
			return false
		}
	}
	return true
}
func allOpen(fs []faceSignal) bool {
	if len(fs) == 0 {
		return false
	}
	known := false
	for _, f := range fs {
		if !f.closed.Valid {
			return false
		}
		known = true
		if f.closed.Int64 == 1 {
			return false
		}
	}
	return known
}

type eyeSibling struct {
	closed fullAsset
	open   fullAsset
}

func closedEyeSiblings(as []fullAsset, s signals) []eyeSibling {
	bursts := map[string][]fullAsset{}
	images := []fullAsset{}
	for _, a := range as {
		if a.MediaType != "image" {
			continue
		}
		images = append(images, a)
		if a.burst != "" {
			bursts[a.burst] = append(bursts[a.burst], a)
		}
	}
	sort.Slice(images, func(i, j int) bool { return images[i].created.Before(images[j].created) })
	out := []eyeSibling{}
	for _, a := range images {
		if !allClosed(s.faces[a.ID]) {
			continue
		}
		if sibling, ok := openBurstSibling(a, bursts[a.burst], s); ok {
			out = append(out, eyeSibling{closed: a, open: sibling})
			continue
		}
		i := sort.Search(len(images), func(i int) bool {
			return !images[i].created.Before(a.created.Add(-90 * time.Second))
		})
		for ; i < len(images) && !images[i].created.After(a.created.Add(90*time.Second)); i++ {
			if images[i].ID != a.ID && allOpen(s.faces[images[i].ID]) {
				out = append(out, eyeSibling{closed: a, open: images[i]})
				break
			}
		}
	}
	return out
}

func openBurstSibling(a fullAsset, burst []fullAsset, s signals) (fullAsset, bool) {
	if a.burst == "" {
		return fullAsset{}, false
	}
	for _, b := range burst {
		if b.ID != a.ID && allOpen(s.faces[b.ID]) {
			return b, true
		}
	}
	return fullAsset{}, false
}
func hasFaceQualityAbove(fs []faceSignal, x float64) bool {
	for _, f := range fs {
		if f.quality.Valid && f.quality.Float64 > x {
			return true
		}
	}
	return false
}
func namedFaceQualityMedian(as []fullAsset, s signals) float64 {
	var face []float64
	for _, a := range as {
		if a.MediaType != "image" {
			continue
		}
		for _, x := range s.faces[a.ID] {
			if x.quality.Valid {
				face = append(face, x.quality.Float64)
			}
		}
	}
	return percentile(face, .5)
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	i := int(float64(len(v)-1) * p)
	return v[i]
}
func duplicateGroups(ctx context.Context, db *sql.DB, as []fullAsset) ([][]fullAsset, error) {
	by := map[string]fullAsset{}
	for _, a := range as {
		by[a.ID] = a
	}
	m := map[string][]fullAsset{}
	r, err := db.QueryContext(ctx, `select asset_id,value_json from model_observation where observation_type='apple_duplicate'`)
	if err != nil {
		return nil, err
	}
	for r.Next() {
		var id, v string
		if err = r.Scan(&id, &v); err != nil {
			r.Close()
			return nil, err
		}
		var x map[string]any
		if err = json.Unmarshal([]byte(v), &x); err != nil {
			r.Close()
			return nil, fmt.Errorf("parse duplicate observation for asset %q: %w", id, err)
		}
		for _, k := range []string{"metadata_group", "perceptual_group"} {
			if z := fmt.Sprint(x[k]); z != "<nil>" && z != "" {
				if a, ok := by[id]; ok {
					m[k+":"+z] = append(m[k+":"+z], a)
				}
			}
		}
	}
	if err = r.Err(); err != nil {
		r.Close()
		return nil, err
	}
	if err = r.Close(); err != nil {
		return nil, err
	}
	r, err = db.QueryContext(ctx, `select asset_id, sha256 from asset_resource where deleted_at is null`)
	if err != nil {
		return nil, err
	}
	for r.Next() {
		var id string
		var h sql.NullString
		if err = r.Scan(&id, &h); err != nil {
			r.Close()
			return nil, err
		}
		if h.Valid && h.String != "" {
			if a, ok := by[id]; ok {
				m["hash:"+h.String] = append(m["hash:"+h.String], a)
			}
		}
	}
	if err = r.Err(); err != nil {
		r.Close()
		return nil, err
	}
	if err = r.Close(); err != nil {
		return nil, err
	}
	out := [][]fullAsset{}
	seen := map[string]bool{}
	for _, g := range m {
		u := []fullAsset{}
		for _, a := range g {
			if !seen[a.ID] {
				seen[a.ID] = true
				u = append(u, a)
			}
		}
		if len(u) > 1 {
			out = append(out, u)
		}
	}
	return out, nil
}
func keeperBefore(a, b fullAsset, s signals) bool {
	aa, bb := a.width*a.height, b.width*b.height
	if aa != bb {
		return aa > bb
	}
	if a.favorite != b.favorite {
		return a.favorite > b.favorite
	}
	x, xok := s.aesthetic[a.ID]
	y, yok := s.aesthetic[b.ID]
	if xok && yok && x.Float64 != y.Float64 {
		return x.Float64 > y.Float64
	}
	if xok != yok {
		return xok
	}
	return a.created.Before(b.created)
}
func dayKey(a fullAsset) string {
	l := time.UTC
	if a.tz != "" {
		if z, e := time.LoadLocation(a.tz); e == nil {
			l = z
		}
	}
	return a.created.In(l).Format("2006-01-02")
}
func groupKey(g string, a fullAsset) string {
	if g == "burst" {
		if a.burst != "" {
			return a.burst
		}
		return "asset:" + a.ID
	}
	if g == "day" {
		return dayKey(a)
	}
	if g == "time" {
		return a.created.Format(time.RFC3339)
	}
	return "all"
}

func earliestAsset(as []fullAsset) fullAsset {
	earliest := as[0]
	for _, a := range as[1:] {
		if a.created.Before(earliest.created) {
			earliest = a
		}
	}
	return earliest
}
