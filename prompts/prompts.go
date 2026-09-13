package prompts

import _ "embed"

const (
	PhotoCardV1Version                   = "photo-card-v1"
	LocalMultimodalObservationsV1Version = "photoscrawl.local-multimodal-observations.v1"
	DefaultPhotoCardV1Path               = "prompts/photo-card-v1.md"
)

//go:embed local-multimodal-observations-v1.md
var LocalMultimodalObservationsV1 string

//go:embed photo-card-v1.md
var PhotoCardV1 string
