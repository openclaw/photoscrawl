package photos

import "context"

type Provider interface {
	Snapshot(ctx context.Context, libraryPath string) (LibrarySnapshot, error)
}

type LibrarySnapshot struct {
	LibraryPath         string         `json:"library_path"`
	Provider            string         `json:"provider"`
	PhotosVersion       string         `json:"photos_version"`
	AuthorizationStatus string         `json:"authorization_status,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	Assets              []Asset        `json:"assets"`
}

type Asset struct {
	LocalIdentifier  string            `json:"local_identifier"`
	MediaType        string            `json:"media_type"`
	MediaSubtypes    string            `json:"media_subtypes"`
	CreationDate     string            `json:"creation_date"`
	ModificationDate string            `json:"modification_date"`
	AddedDate        string            `json:"added_date"`
	TimezoneName     string            `json:"timezone_name"`
	Width            int64             `json:"width"`
	Height           int64             `json:"height"`
	DurationSeconds  float64           `json:"duration_seconds"`
	Favorite         bool              `json:"favorite"`
	Hidden           bool              `json:"hidden"`
	BurstIdentifier  string            `json:"burst_identifier"`
	RepresentsBurst  bool              `json:"represents_burst"`
	DeletedAt        string            `json:"deleted_at,omitempty"`
	DeletionSource   string            `json:"deletion_source,omitempty"`
	DeletionReason   string            `json:"deletion_reason,omitempty"`
	Location         *Location         `json:"location,omitempty"`
	Resources        []Resource        `json:"resources,omitempty"`
	Albums           []AlbumMembership `json:"albums,omitempty"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
}

type Resource struct {
	SourceIdentifier string         `json:"source_identifier,omitempty"`
	Type             string         `json:"type"`
	UTI              string         `json:"uti"`
	OriginalFilename string         `json:"original_filename"`
	LocalPath        string         `json:"local_path,omitempty"`
	Availability     string         `json:"availability"`
	FileSize         int64          `json:"file_size,omitempty"`
	StableHash       string         `json:"stable_hash,omitempty"`
	AvailableLocally bool           `json:"available_locally"`
	NeedsDownload    bool           `json:"needs_download"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type AlbumMembership struct {
	AlbumID    string `json:"album_id"`
	AlbumTitle string `json:"album_title"`
	AlbumKind  string `json:"album_kind"`
}

type Location struct {
	Latitude           float64  `json:"latitude"`
	Longitude          float64  `json:"longitude"`
	Altitude           *float64 `json:"altitude,omitempty"`
	HorizontalAccuracy *float64 `json:"horizontal_accuracy,omitempty"`
}
