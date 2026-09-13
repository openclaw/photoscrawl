package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type ShareCheckOptions struct {
	IDsFile        string
	ExcludeIDsFile string
}

type ShareCheckAsset struct {
	AssetRow
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Notes   []string `json:"notes"`
}

type ShareCheckResult struct {
	Assets []ShareCheckAsset `json:"assets"`
	Counts map[string]int    `json:"counts"`
}

type privacyCategory struct {
	name    string
	aliases []string
}

var blockedPrivacyCategories = []privacyCategory{
	{name: "document", aliases: []string{"document", "documents"}},
	{name: "receipt", aliases: []string{"receipt", "receipts"}},
	{name: "invoice", aliases: []string{"invoice", "invoices"}},
	{name: "payment", aliases: []string{"payment", "payments"}},
	{name: "credit card", aliases: []string{"credit card", "credit cards"}},
	{name: "bank", aliases: []string{"bank", "banking"}},
	{name: "identity", aliases: []string{"identity", "identification"}},
	{name: "id card", aliases: []string{"id card", "id cards"}},
	{name: "passport", aliases: []string{"passport", "passports"}},
	{name: "license plate", aliases: []string{"license plate", "license plates"}},
	{name: "address", aliases: []string{"address", "addresses"}},
	{name: "date of birth", aliases: []string{"date of birth", "dates of birth"}},
	{name: "health", aliases: []string{"health"}},
	{name: "medical", aliases: []string{"medical"}},
	{name: "prescription", aliases: []string{"prescription", "prescriptions"}},
	{name: "signature", aliases: []string{"signature", "signatures"}},
	{name: "boarding pass", aliases: []string{"boarding pass", "boarding passes"}},
	{name: "ticket", aliases: []string{"ticket", "tickets"}},
	{name: "driver's license", aliases: []string{"driver's license", "drivers license", "driver license", "driving licence", "driving license"}},
	{name: "social security", aliases: []string{"social security card", "social security number", "ssn"}},
	{name: "password", aliases: []string{"password", "passcode", "pin code", "pin number"}},
	{name: "access credential", aliases: []string{"api key", "access token", "recovery code", "seed phrase"}},
	{name: "bank account", aliases: []string{"account number", "iban", "routing number", "swift", "sort code"}},
	{name: "payment card", aliases: []string{"card number", "debit card", "bank card", "cvv", "cvc"}},
	{name: "identity document", aliases: []string{"tax id", "tax return", "national id", "residence permit", "visa", "work permit", "insurance card"}},
	{name: "sensitive document", aliases: []string{"contract", "bank statement", "pay slip", "payslip"}},
	{name: "qr code", aliases: []string{"qr code"}},
}

var privacyClauseSeparator = regexp.MustCompile(`(?i)\s*,\s*|\s+\band\b\s+`)

var privacyNoteTerms = []string{
	"baby",
	"babies",
	"child",
	"children",
	"face",
	"faces",
	"kid",
	"kids",
	"minor",
	"minors",
	"people",
	"person",
}

func ShareCheck(ctx context.Context, paths Paths, options ShareCheckOptions) (ShareCheckResult, error) {
	if strings.TrimSpace(options.IDsFile) == "" {
		return ShareCheckResult{}, errors.New("--ids-file is required")
	}
	ids, err := readIDs(options.IDsFile)
	if err != nil {
		return ShareCheckResult{}, err
	}
	excluded, err := readExcluded(options.ExcludeIDsFile)
	if err != nil {
		return ShareCheckResult{}, err
	}
	db, err := openArchiveReadOnly(ctx, paths.Database)
	if err != nil {
		return ShareCheckResult{}, err
	}
	defer db.Close()
	assets, err := loadAssets(ctx, db.DB())
	if err != nil {
		return ShareCheckResult{}, err
	}
	byID := make(map[string]fullAsset, len(assets)*2)
	for _, asset := range assets {
		byID[asset.ID] = asset
		byID[asset.LocalIdentifier] = asset
		byID[normalizeAssetLocalIdentifier(asset.LocalIdentifier)] = asset
	}
	result := ShareCheckResult{
		Assets: []ShareCheckAsset{},
		Counts: map[string]int{"pass": 0, "blocked": 0, "unreviewed": 0},
	}
	seen := map[string]bool{}
	for _, requestedID := range ids {
		asset, ok := byID[requestedID]
		if !ok {
			return ShareCheckResult{}, fmt.Errorf("asset %q not found", requestedID)
		}
		if seen[asset.ID] || excludedMatch(asset, excluded) {
			continue
		}
		seen[asset.ID] = true
		row, err := shareCheckAsset(ctx, db.DB(), asset)
		if err != nil {
			return ShareCheckResult{}, err
		}
		result.Assets = append(result.Assets, row)
		result.Counts[row.Status]++
	}
	return result, nil
}

func shareCheckAsset(ctx context.Context, db *sql.DB, asset fullAsset) (ShareCheckAsset, error) {
	row := ShareCheckAsset{
		AssetRow: asset.AssetRow,
		Status:   "pass",
		Reasons:  []string{},
		Notes:    []string{},
	}
	if asset.hidden != 0 {
		row.Reasons = append(row.Reasons, "hidden")
	}
	if isScreenshotSubtype(asset.subtypes) {
		row.Reasons = append(row.Reasons, "screenshot")
	}
	phrases, malformed, err := loadPrivacyPhrases(ctx, db, asset.ID)
	if err != nil {
		return ShareCheckAsset{}, err
	}
	if malformed {
		row.Reasons = append(row.Reasons, "unreadable privacy sensitivity observation")
	}
	blocked := map[string]bool{}
	for _, phrase := range phrases {
		for _, category := range blockedCategoriesInPhrase(phrase) {
			blocked[category] = true
		}
		if isPrivacyNote(phrase) {
			row.Notes = append(row.Notes, phrase)
		}
	}
	categoryNames := make([]string, 0, len(blocked))
	for category := range blocked {
		categoryNames = append(categoryNames, category)
	}
	sort.Strings(categoryNames)
	for _, category := range categoryNames {
		row.Reasons = append(row.Reasons, "privacy category: "+category)
	}
	if len(row.Reasons) > 0 {
		row.Status = "blocked"
		return row, nil
	}
	var state string
	err = db.QueryRowContext(ctx, `select state from classification_queue where asset_id = ?`, asset.ID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		row.Status = "unreviewed"
		row.Reasons = append(row.Reasons, "content classification not completed")
		return row, nil
	}
	if err != nil {
		return ShareCheckAsset{}, err
	}
	if state != "content_classified" {
		row.Status = "unreviewed"
		row.Reasons = append(row.Reasons, "content classification not completed")
		return row, nil
	}
	return row, nil
}

func loadPrivacyPhrases(ctx context.Context, db *sql.DB, assetID string) ([]string, bool, error) {
	rows, err := db.QueryContext(ctx, `select value_json from model_observation where asset_id = ? and observation_type = 'privacy_sensitivity'`, assetID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	phrases := []string{}
	malformed := false
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var value struct {
			Text string `json:"text"`
		}
		if err = json.Unmarshal([]byte(raw), &value); err != nil || strings.TrimSpace(value.Text) == "" {
			malformed = true
			continue
		}
		phrases = append(phrases, strings.TrimSpace(value.Text))
	}
	return phrases, malformed, rows.Err()
}

func blockedCategoriesInPhrase(phrase string) []string {
	blocked := map[string]bool{}
	for _, clause := range privacyClauseSeparator.Split(normalizePrivacyText(phrase), -1) {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		mentions := categoriesInClause(clause)
		if len(mentions) == 0 || whollyNegatedCategoryList(clause) {
			continue
		}
		for _, mention := range mentions {
			if !directlyNegated(clause, mention.start) {
				blocked[mention.name] = true
			}
		}
	}
	out := make([]string, 0, len(blocked))
	for category := range blocked {
		out = append(out, category)
	}
	sort.Strings(out)
	return out
}

type categoryMention struct {
	name  string
	alias string
	start int
}

func categoriesInClause(clause string) []categoryMention {
	mentions := []categoryMention{}
	for _, category := range blockedPrivacyCategories {
		for _, alias := range category.aliases {
			for offset := 0; offset < len(clause); {
				index := strings.Index(clause[offset:], alias)
				if index < 0 {
					break
				}
				index += offset
				end := index + len(alias)
				if wordBoundary(clause, index-1) && wordBoundary(clause, end) {
					mentions = append(mentions, categoryMention{name: category.name, alias: alias, start: index})
					break
				}
				offset = end
			}
		}
	}
	sort.Slice(mentions, func(i, j int) bool {
		if mentions[i].start != mentions[j].start {
			return mentions[i].start < mentions[j].start
		}
		return len(mentions[i].alias) > len(mentions[j].alias)
	})
	deduped := mentions[:0]
	for _, mention := range mentions {
		if len(deduped) > 0 && mention.start == deduped[len(deduped)-1].start {
			continue
		}
		deduped = append(deduped, mention)
	}
	return deduped
}

func whollyNegatedCategoryList(clause string) bool {
	rest, ok := trimNegationPrefix(clause)
	if !ok {
		return false
	}
	for _, item := range strings.Split(rest, " or ") {
		item = strings.TrimSpace(item)
		item = strings.TrimPrefix(item, "a ")
		item = strings.TrimPrefix(item, "an ")
		item = strings.TrimPrefix(item, "any ")
		if !isExactCategoryAlias(item) {
			return false
		}
	}
	return true
}

func trimNegationPrefix(clause string) (string, bool) {
	for _, prefix := range []string{"without ", "not ", "no "} {
		if strings.HasPrefix(clause, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(clause, prefix)), true
		}
	}
	return clause, false
}

func isExactCategoryAlias(value string) bool {
	for _, category := range blockedPrivacyCategories {
		for _, alias := range category.aliases {
			if value == alias {
				return true
			}
		}
	}
	return false
}

func directlyNegated(clause string, start int) bool {
	prefix := strings.TrimSpace(clause[:start])
	words := strings.Fields(prefix)
	if len(words) == 0 {
		return false
	}
	last := words[len(words)-1]
	if last == "no" || last == "not" || last == "without" {
		return true
	}
	if last == "a" || last == "an" || last == "any" {
		if len(words) > 1 {
			beforeArticle := words[len(words)-2]
			return beforeArticle == "no" || beforeArticle == "not" || beforeArticle == "without"
		}
	}
	return false
}

func normalizePrivacyText(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	return strings.Join(strings.Fields(value), " ")
}

func isPrivacyNote(phrase string) bool {
	phrase = normalizePrivacyText(phrase)
	for _, term := range privacyNoteTerms {
		for offset := 0; offset < len(phrase); {
			index := strings.Index(phrase[offset:], term)
			if index < 0 {
				break
			}
			index += offset
			if wordBoundary(phrase, index-1) && wordBoundary(phrase, index+len(term)) {
				return true
			}
			offset = index + len(term)
		}
	}
	return false
}

func wordBoundary(value string, index int) bool {
	if index < 0 || index >= len(value) {
		return true
	}
	character := value[index]
	return character < 'a' || character > 'z'
}
