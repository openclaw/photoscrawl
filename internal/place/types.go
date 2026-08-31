package place

import "time"

const (
	RawContextCommand = "place-context-raw"

	defaultRadiusMeters = 150.0
	maxCandidates       = 12

	POIStatusFound         = "found"
	POIStatusNone          = "none"
	POIStatusProviderError = "provider_error"
)

type Options struct {
	InputPath    string
	RadiusMeters float64
	CacheDir     string
}

type RawOptions struct {
	InputPath    string
	RadiusMeters float64
}

type Input struct {
	AssetID        string     `json:"asset_id,omitempty"`
	ImagePath      string     `json:"image_path,omitempty"`
	TakenAt        string     `json:"taken_at,omitempty"`
	Location       Coordinate `json:"location"`
	AccuracyMeters float64    `json:"accuracy_meters,omitempty"`
}

type Coordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Result struct {
	Input         Input          `json:"input"`
	Provider      string         `json:"provider"`
	Source        string         `json:"source"`
	RadiusMeters  float64        `json:"radius_meters"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Area          []AreaLevel    `json:"area,omitempty"`
	Address       *Address       `json:"address,omitempty"`
	MapFeatures   []MapFeature   `json:"map_features,omitempty"`
	POIStatus     string         `json:"poi_status,omitempty"`
	POIReason     string         `json:"poi_reason,omitempty"`
	POITotal      int            `json:"poi_total,omitempty"`
	POICandidates []POICandidate `json:"poi_candidates,omitempty"`
	Cached        bool           `json:"cached,omitempty"`
}

type AreaLevel struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type Address struct {
	Name                  string   `json:"name,omitempty"`
	Thoroughfare          string   `json:"thoroughfare,omitempty"`
	SubThoroughfare       string   `json:"sub_thoroughfare,omitempty"`
	Locality              string   `json:"locality,omitempty"`
	SubLocality           string   `json:"sub_locality,omitempty"`
	AdministrativeArea    string   `json:"administrative_area,omitempty"`
	SubAdministrativeArea string   `json:"sub_administrative_area,omitempty"`
	PostalCode            string   `json:"postal_code,omitempty"`
	Country               string   `json:"country,omitempty"`
	ISOCountryCode        string   `json:"iso_country_code,omitempty"`
	TimeZone              string   `json:"time_zone,omitempty"`
	AreasOfInterest       []string `json:"areas_of_interest,omitempty"`
	Formatted             string   `json:"formatted,omitempty"`
	Source                string   `json:"source,omitempty"`
}

type MapFeature struct {
	Name      string  `json:"name,omitempty"`
	Kind      string  `json:"kind,omitempty"`
	Relation  string  `json:"relation,omitempty"`
	DistanceM float64 `json:"distance_m,omitempty"`
	Source    string  `json:"source,omitempty"`
}

type POICandidate struct {
	Name       string      `json:"name"`
	Category   string      `json:"category,omitempty"`
	DistanceM  float64     `json:"distance_m,omitempty"`
	Coordinate *Coordinate `json:"coordinate,omitempty"`
	Address    *Address    `json:"address,omitempty"`
	Source     string      `json:"source"`
	Provenance []string    `json:"provenance,omitempty"`
}
