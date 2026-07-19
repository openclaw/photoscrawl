package photos

import (
	"context"
	"fmt"
	"strings"
)

func ProviderByName(name string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return NewProvider(), nil
	case "sqlite":
		return SQLiteSnapshotProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown photos provider %q (want auto or sqlite)", name)
	}
}

type FallbackProvider struct {
	Primary   Provider
	Secondary Provider
}

func (p FallbackProvider) Snapshot(ctx context.Context, libraryPath string) (LibrarySnapshot, error) {
	if p.Primary == nil {
		return p.secondary(ctx, libraryPath, nil)
	}
	snapshot, err := p.Primary.Snapshot(ctx, libraryPath)
	if err == nil {
		return snapshot, nil
	}
	return p.secondary(ctx, libraryPath, err)
}

func (p FallbackProvider) secondary(ctx context.Context, libraryPath string, primaryErr error) (LibrarySnapshot, error) {
	if p.Secondary == nil {
		if primaryErr != nil {
			return LibrarySnapshot{}, primaryErr
		}
		return LibrarySnapshot{}, fmt.Errorf("photos provider is not configured")
	}
	snapshot, err := p.Secondary.Snapshot(ctx, libraryPath)
	if err != nil {
		if primaryErr != nil {
			return LibrarySnapshot{}, fmt.Errorf("primary provider failed: %v; fallback provider failed: %w", primaryErr, err)
		}
		return LibrarySnapshot{}, err
	}
	if primaryErr != nil {
		if snapshot.Metadata == nil {
			snapshot.Metadata = map[string]any{}
		}
		snapshot.Metadata["primary_provider_error"] = primaryErr.Error()
		snapshot.Metadata["source_strategy"] = "fallback_after_primary_error"
	}
	return snapshot, nil
}
