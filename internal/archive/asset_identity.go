package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openclaw/photoscrawl/internal/photos"
)

type assetIdentityCandidate struct {
	id, identifier string
	seen, trusted  bool
	conflicting    bool
}

type assetIdentityResolver struct {
	sourceID string
	exact    map[string]*assetIdentityCandidate
	byUUID   map[string][]*assetIdentityCandidate
}

func newAssetIdentityResolver(ctx context.Context, tx *sql.Tx, sourceID string) (*assetIdentityResolver, error) {
	r := &assetIdentityResolver{
		sourceID: sourceID, exact: map[string]*assetIdentityCandidate{},
		byUUID: map[string][]*assetIdentityCandidate{},
	}
	byID := map[string]*assetIdentityCandidate{}
	rows, err := tx.QueryContext(ctx, `
select a.id, a.local_identifier,
       coalesce(first.source_library_id = a.source_library_id
                and last.source_library_id = a.source_library_id, 0)
from asset a
left join crawl_seen_asset seen on seen.asset_id = a.id and seen.source_library_id = a.source_library_id
left join crawl_snapshot first on first.id = seen.first_seen_snapshot_id
left join crawl_snapshot last on last.id = seen.last_seen_snapshot_id
where a.source_library_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		candidate := &assetIdentityCandidate{}
		if err := rows.Scan(&candidate.id, &candidate.identifier, &candidate.seen); err != nil {
			rows.Close()
			return nil, err
		}
		byID[candidate.id] = candidate
		r.add(candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Evidence retains each provider's observed identifier across alternation.
	// Latest asset/library metadata alone cannot establish this relationship.
	rows, err = tx.QueryContext(ctx, `
select e.asset_id, e.source, e.pointer, e.evidence_kind
from evidence_ref e join asset a on a.id = e.asset_id
where a.source_library_id = ? and e.evidence_kind in ('asset_metadata', 'asset_tombstone')
  and exists (select 1 from crawl_snapshot s
              where s.source_library_id = a.source_library_id and s.provider = e.source)`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID, provider, pointer, kind string
		if err := rows.Scan(&assetID, &provider, &pointer, &kind); err != nil {
			return nil, err
		}
		candidate := byID[assetID]
		key, canonicalProvider := recognizedAssetIdentifier(candidate.identifier)
		if key == "" || (provider != "photokit" && provider != "photos_sqlite_snapshot") {
			continue
		}
		observed := strings.TrimPrefix(pointer, "asset:")
		if kind == "asset_tombstone" {
			observed, _, _ = strings.Cut(observed, "/tombstone:")
		}
		observedKey, observedProvider := recognizedAssetIdentifier(observed)
		if !strings.HasPrefix(pointer, "asset:") || observedKey != key || observedProvider != provider {
			candidate.conflicting = true
			continue
		}
		if candidate.seen && provider == canonicalProvider && observed == candidate.identifier {
			candidate.trusted = true
		}
	}
	return r, rows.Err()
}

func (r *assetIdentityResolver) add(candidate *assetIdentityCandidate) {
	r.exact[candidate.identifier] = candidate
	if key, _ := recognizedAssetIdentifier(candidate.identifier); key != "" {
		r.byUUID[key] = append(r.byUUID[key], candidate)
	}
}

func (r *assetIdentityResolver) resolve(provider string, asset photos.Asset) (string, string, error) {
	observed := asset.LocalIdentifier
	key, shapeProvider := recognizedAssetIdentifier(observed)
	if key == "" || shapeProvider != provider {
		if exact := r.exact[observed]; exact != nil {
			return exact.id, exact.identifier, nil
		}
		return stableID("asset", r.sourceID, observed), observed, nil
	}
	if provider == "photokit" && asset.Metadata["photokit_local_identifier"] != observed {
		return "", "", errors.New("PhotoKit asset identity lacks matching native identifier evidence")
	}
	if provider == "photos_sqlite_snapshot" && asset.Metadata["schema_source"] != "ZASSET" {
		return "", "", errors.New("SQLite asset identity lacks ZASSET provider evidence")
	}
	candidates := r.byUUID[key]
	// Check the entire recognized candidate set before returning an exact match:
	// legacy full-ID plus bare-UUID duplicates require rollback, never a winner.
	if len(candidates) > 1 {
		return "", "", fmt.Errorf("ambiguous asset identity in this library for %q", observed)
	}
	if len(candidates) == 0 {
		return stableID("asset", r.sourceID, observed), observed, nil
	}
	candidate := candidates[0]
	if candidate.conflicting {
		return "", "", fmt.Errorf("conflicting provider evidence for asset %q", observed)
	}
	if candidate.identifier == observed {
		return candidate.id, candidate.identifier, nil
	}
	_, previousProvider := recognizedAssetIdentifier(candidate.identifier)
	if !candidate.trusted || previousProvider == provider {
		return "", "", fmt.Errorf("asset identity lacks unambiguous cross-provider evidence for %q", observed)
	}
	canonical := candidate.identifier
	if provider == "photokit" {
		canonical = observed
	}
	return candidate.id, canonical, nil
}

func (r *assetIdentityResolver) remember(id, canonical, provider string, observed photos.Asset) {
	key, _ := recognizedAssetIdentifier(canonical)
	for _, candidate := range r.byUUID[key] {
		if candidate.id != id {
			continue
		}
		delete(r.exact, candidate.identifier)
		candidate.identifier = canonical
		candidate.seen = true
		if _, canonicalProvider := recognizedAssetIdentifier(canonical); canonicalProvider == provider {
			candidate.trusted = true
		}
		r.exact[canonical] = candidate
		return
	}
	observedKey, observedProvider := recognizedAssetIdentifier(observed.LocalIdentifier)
	r.add(&assetIdentityCandidate{
		id: id, identifier: canonical, seen: true,
		trusted: key != "" && observedKey == key && observedProvider == provider,
	})
}

// Only the UUID and native tail present in supported provider fixtures are
// related. Other identifiers, including arbitrary slash-bearing values, are opaque.
func recognizedAssetIdentifier(identifier string) (string, string) {
	provider := "photos_sqlite_snapshot"
	uuid := identifier
	if strings.HasSuffix(identifier, "/L0/001") {
		provider = "photokit"
		uuid = strings.TrimSuffix(identifier, "/L0/001")
	}
	if len(uuid) != 36 {
		return "", ""
	}
	for i := 0; i < len(uuid); i++ {
		switch i {
		case 8, 13, 18, 23:
			if uuid[i] != '-' {
				return "", ""
			}
		default:
			b := uuid[i]
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
				return "", ""
			}
		}
	}
	return strings.ToLower(uuid), provider
}
