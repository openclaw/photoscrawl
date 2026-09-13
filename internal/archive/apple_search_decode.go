package archive

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
)

type appleSearchRow struct {
	searchVariant    string
	gaRowID          int64
	uuid0            int64
	uuid1            int64
	assetUUID        string
	groupID          int64
	category         int
	owningGroupID    sql.NullInt64
	contentString    string
	normalizedString string
	lookupIdentifier string
	score            sql.NullFloat64
}

type appleSearchCategory struct {
	ID           int
	Name         string
	SemanticKind string
}

func loadPSISearchRows(ctx context.Context, db *sql.DB) ([]appleSearchRow, error) {
	rows, err := db.QueryContext(ctx, `
select ga.rowid,
       assets.uuid_0,
       assets.uuid_1,
       groups.rowid as groupid,
       groups.category,
       groups.owning_groupid,
       coalesce(groups.content_string, ''),
       coalesce(groups.normalized_string, ''),
       coalesce(groups.lookup_identifier, ''),
       cast(groups.score as real)
from ga
join groups on groups.rowid = ga.groupid
join assets on ga.assetid = assets.rowid
order by ga.rowid
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple search index rows: %w", err)
	}
	defer rows.Close()
	out := []appleSearchRow{}
	for rows.Next() {
		var row appleSearchRow
		if err := rows.Scan(
			&row.gaRowID,
			&row.uuid0,
			&row.uuid1,
			&row.groupID,
			&row.category,
			&row.owningGroupID,
			&row.contentString,
			&row.normalizedString,
			&row.lookupIdentifier,
			&row.score,
		); err != nil {
			return nil, err
		}
		row.assetUUID = psiIntsToUUID(row.uuid0, row.uuid1)
		row.searchVariant = "psi"
		row.contentString = stripAppleSearchString(row.contentString)
		row.normalizedString = stripAppleSearchString(row.normalizedString)
		row.lookupIdentifier = stripAppleSearchString(row.lookupIdentifier)
		out = append(out, row)
	}
	return out, rows.Err()
}

type leoLexeme struct {
	category int
	content  string
}

func loadLeoSearchRows(ctx context.Context, db *sql.DB) ([]appleSearchRow, error) {
	lexemeRows, err := db.QueryContext(ctx, `
select lexeme_id, type, category, coalesce(content, '')
from lexicon
order by lexeme_id
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple leo lexicon: %w", err)
	}
	lexemes := map[uint32]leoLexeme{}
	for lexemeRows.Next() {
		var id uint32
		var lexemeType, category int
		var content string
		if err := lexemeRows.Scan(&id, &lexemeType, &category, &content); err != nil {
			lexemeRows.Close()
			return nil, err
		}
		mappedCategory, ok := leoCategoryToPhotos8(category)
		content = stripAppleSearchString(content)
		if ok && lexemeType == 1 && content != "" {
			lexemes[id] = leoLexeme{category: mappedCategory, content: content}
		}
	}
	if err := lexemeRows.Close(); err != nil {
		return nil, err
	}
	if err := lexemeRows.Err(); err != nil {
		return nil, err
	}

	itemRows, err := db.QueryContext(ctx, `
select rowid, coalesce(identifier, ''), lexeme_ids
from items
where type = 1
order by rowid
`)
	if err != nil {
		return nil, fmt.Errorf("load Apple leo items: %w", err)
	}
	defer itemRows.Close()
	out := []appleSearchRow{}
	var observationRowID int64
	for itemRows.Next() {
		var itemRowID int64
		var identifier string
		var lexemeIDs []byte
		if err := itemRows.Scan(&itemRowID, &identifier, &lexemeIDs); err != nil {
			return nil, err
		}
		assetUUID := strings.ToUpper(stripAppleSearchString(identifier))
		if assetUUID == "" {
			continue
		}
		for _, lexemeID := range decodeLeoLexemeIDs(lexemeIDs) {
			lexeme, ok := lexemes[lexemeID]
			if !ok {
				continue
			}
			observationRowID++
			out = append(out, appleSearchRow{
				searchVariant:    "leo",
				gaRowID:          observationRowID,
				assetUUID:        assetUUID,
				groupID:          int64(lexemeID),
				category:         lexeme.category,
				contentString:    lexeme.content,
				normalizedString: strings.ToLower(lexeme.content),
			})
		}
	}
	return out, itemRows.Err()
}

func decodeLeoLexemeIDs(data []byte) []uint32 {
	count := len(data) / 4
	out := make([]uint32, 0, count)
	for offset := 0; offset+4 <= len(data); offset += 4 {
		out = append(out, binary.LittleEndian.Uint32(data[offset:offset+4]))
	}
	return out
}

func leoCategoryToPhotos8(category int) (int, bool) {
	// Apple changed category ids with leo.sqlite. Keep this mapping aligned with
	// osxphotos/photosdb/_photosdb_process_searchinfo.py:LEO_CATEGORY_TO_PHOTOS8.
	mapped, ok := map[int]int{
		1010: 1100,
		1020: 1101,
		1030: 1103,
		1040: 1104,
		1050: 1106,
		1060: 1107,
		2030: 1701,
		2050: 2,
		2060: 1,
		2070: 3,
		2090: 5,
		2110: 7,
		2140: 10,
		2150: 11,
		2160: 12,
		3001: 1300,
		4000: 1500,
		4010: 1510,
		4120: 1203,
		5000: 1900,
		5020: 1902,
		6000: 2300,
		7000: 1201,
		7010: 1400,
		8000: 2000,
		8050: 2100,
		8070: 1200,
		8080: 1202,
	}[category]
	return mapped, ok
}

func appleSearchCategoryForID(id int) appleSearchCategory {
	if category, ok := appleSearchCategoriesPhotos8()[id]; ok {
		return category
	}
	return appleSearchCategory{ID: id, Name: "UNKNOWN", SemanticKind: fmt.Sprintf("apple_search_category_%d", id)}
}

// Category ids come from osxphotos/_constants.py:SearchCategory_Photos8.
// Missing ids deliberately fall through to UNKNOWN so new Apple categories still import.
func appleSearchCategoriesPhotos8() map[int]appleSearchCategory {
	categories := map[int]appleSearchCategory{
		1:    {ID: 1, Name: "PLACE_NAME", SemanticKind: "apple_place"},
		2:    {ID: 2, Name: "STREET", SemanticKind: "apple_place"},
		3:    {ID: 3, Name: "NEIGHBORHOOD", SemanticKind: "apple_place"},
		4:    {ID: 4, Name: "LOCALITY_4", SemanticKind: "apple_place"},
		5:    {ID: 5, Name: "CITY", SemanticKind: "apple_place"},
		6:    {ID: 6, Name: "SUB_LOCALITY_6", SemanticKind: "apple_place"},
		7:    {ID: 7, Name: "NAMED_AREA", SemanticKind: "apple_place"},
		8:    {ID: 8, Name: "LOCALITY_8", SemanticKind: "apple_place"},
		10:   {ID: 10, Name: "STATE", SemanticKind: "apple_place"},
		11:   {ID: 11, Name: "STATE_ABBREVIATION", SemanticKind: "apple_place"},
		12:   {ID: 12, Name: "COUNTRY", SemanticKind: "apple_place"},
		14:   {ID: 14, Name: "BODY_OF_WATER", SemanticKind: "apple_place"},
		1000: {ID: 1000, Name: "HOME", SemanticKind: "apple_place"},
		1001: {ID: 1001, Name: "WORK", SemanticKind: "apple_place"},
		1100: {ID: 1100, Name: "MONTH", SemanticKind: "apple_date"},
		1101: {ID: 1101, Name: "YEAR", SemanticKind: "apple_date"},
		1103: {ID: 1103, Name: "HOLIDAY", SemanticKind: "apple_date"},
		1104: {ID: 1104, Name: "SEASON", SemanticKind: "apple_date"},
		1106: {ID: 1106, Name: "TIME_OF_DAY", SemanticKind: "apple_date"},
		1107: {ID: 1107, Name: "WEEKPART", SemanticKind: "apple_date"},
		1200: {ID: 1200, Name: "KEYWORDS", SemanticKind: "apple_keyword"},
		1201: {ID: 1201, Name: "TITLE", SemanticKind: "apple_title"},
		1202: {ID: 1202, Name: "DESCRIPTION", SemanticKind: "apple_description"},
		1203: {ID: 1203, Name: "DETECTED_TEXT", SemanticKind: "apple_text"},
		1205: {ID: 1205, Name: "TEXT_FOUND", SemanticKind: "apple_text"},
		1300: {ID: 1300, Name: "PERSON", SemanticKind: "apple_person"},
		1400: {ID: 1400, Name: "ALBUM", SemanticKind: "apple_album"},
		1500: {ID: 1500, Name: "LABEL", SemanticKind: "apple_label"},
		1510: {ID: 1510, Name: "RICH_LABEL", SemanticKind: "apple_label"},
		1600: {ID: 1600, Name: "ACTIVITY", SemanticKind: "apple_activity"},
		1700: {ID: 1700, Name: "VENUE", SemanticKind: "apple_venue"},
		1701: {ID: 1701, Name: "VENUE_TYPE", SemanticKind: "apple_venue"},
		1900: {ID: 1900, Name: "PHOTO_TYPE_PHOTO", SemanticKind: "apple_media_type"},
		1901: {ID: 1901, Name: "PHOTO_TYPE_VIDEO", SemanticKind: "apple_media_type"},
		1902: {ID: 1902, Name: "PHOTO_TYPE_RAW", SemanticKind: "apple_media_type"},
		1905: {ID: 1905, Name: "PHOTO_TYPE_SLOMO", SemanticKind: "apple_media_type"},
		1906: {ID: 1906, Name: "PHOTO_TYPE_LIVE", SemanticKind: "apple_media_type"},
		1907: {ID: 1907, Name: "PHOTO_TYPE_SCREENSHOT", SemanticKind: "apple_media_type"},
		1908: {ID: 1908, Name: "PHOTO_TYPE_PANORAMA", SemanticKind: "apple_media_type"},
		1909: {ID: 1909, Name: "PHOTO_TYPE_TIMELAPSE", SemanticKind: "apple_media_type"},
		1912: {ID: 1912, Name: "PHOTO_TYPE_ANIMATED", SemanticKind: "apple_media_type"},
		1913: {ID: 1913, Name: "PHOTO_TYPE_BURSTS", SemanticKind: "apple_media_type"},
		1914: {ID: 1914, Name: "PHOTO_TYPE_PORTRAIT", SemanticKind: "apple_media_type"},
		1915: {ID: 1915, Name: "PHOTO_TYPE_SELFIES", SemanticKind: "apple_media_type"},
		1916: {ID: 1916, Name: "PHOTO_TYPE_SCREENRECORDINGS", SemanticKind: "apple_media_type"},
		2000: {ID: 2000, Name: "PHOTO_TYPE_FAVORITES", SemanticKind: "apple_media_type"},
		2100: {ID: 2100, Name: "PHOTO_NAME", SemanticKind: "apple_photo_name"},
		2200: {ID: 2200, Name: "SOURCE", SemanticKind: "apple_source"},
		2300: {ID: 2300, Name: "CAMERA", SemanticKind: "apple_camera"},
	}
	return categories
}

func psiIntsToUUID(uuid0, uuid1 int64) string {
	var bytes [16]byte
	binary.LittleEndian.PutUint64(bytes[0:8], uint64(uuid0))
	binary.LittleEndian.PutUint64(bytes[8:16], uint64(uuid1))
	return fmt.Sprintf("%X-%X-%X-%X-%X", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func stripAppleSearchString(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
}
