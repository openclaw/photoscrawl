package photos

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LocalMediaCandidate struct {
	Path  string `json:"path"`
	Class string `json:"class"`
	Size  int64  `json:"size"`
}

type LocalMediaIndex map[string][]LocalMediaCandidate

func BuildLocalMediaIndex(libraryPath string) (LocalMediaIndex, error) {
	return localMediaIndex(libraryPath)
}

func (index LocalMediaIndex) Candidates(localIdentifier string) []LocalMediaCandidate {
	return append([]LocalMediaCandidate(nil), index[mediaUUID(localIdentifier)]...)
}

// AttachLocalMediaPaths finds already-local Photos package media without asking
// Photos or iCloud to export/download anything.
func AttachLocalMediaPaths(snapshot *LibrarySnapshot, libraryPath string) error {
	if snapshot == nil || strings.TrimSpace(libraryPath) == "" {
		return nil
	}
	index, err := localMediaIndex(libraryPath)
	if err != nil {
		return err
	}
	if len(index) == 0 {
		return nil
	}
	for assetIndex := range snapshot.Assets {
		asset := &snapshot.Assets[assetIndex]
		uuid := mediaUUID(asset.LocalIdentifier)
		if uuid == "" {
			continue
		}
		candidates := index[uuid]
		if len(candidates) == 0 {
			continue
		}
		candidate := candidates[0]
		assigned := false
		for resourceIndex := range asset.Resources {
			resource := &asset.Resources[resourceIndex]
			if assigned || !classifiableResource(*resource) {
				continue
			}
			resource.LocalPath = candidate.Path
			resource.Availability = "local"
			resource.AvailableLocally = true
			resource.NeedsDownload = false
			if resource.FileSize == 0 {
				resource.FileSize = candidate.Size
			}
			if resource.Metadata == nil {
				resource.Metadata = map[string]any{}
			}
			resource.Metadata["local_path_class"] = candidate.Class
			resource.Metadata["local_path_source"] = "photos_library_package"
			assigned = true
		}
		if !assigned && asset.MediaType == "image" {
			asset.Resources = append(asset.Resources, Resource{
				Type:             "local_" + candidate.Class,
				UTI:              utiForPath(candidate.Path),
				OriginalFilename: filepath.Base(candidate.Path),
				LocalPath:        candidate.Path,
				Availability:     "local",
				FileSize:         candidate.Size,
				AvailableLocally: true,
				NeedsDownload:    false,
				Metadata: map[string]any{
					"local_path_class":  candidate.Class,
					"local_path_source": "photos_library_package",
				},
			})
		}
	}
	return nil
}

func localMediaIndex(libraryPath string) (LocalMediaIndex, error) {
	roots := []struct {
		path  string
		class string
	}{
		{filepath.Join(libraryPath, "resources", "derivatives"), "derivative"},
		{filepath.Join(libraryPath, "resources", "renders"), "render"},
		{filepath.Join(libraryPath, "originals"), "original"},
	}
	out := LocalMediaIndex{}
	for _, root := range roots {
		if _, err := os.Stat(root.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !classifiablePath(path) {
				return nil
			}
			uuid := mediaUUID(filepath.Base(path))
			if uuid == "" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			out[uuid] = append(out[uuid], LocalMediaCandidate{
				Path:  path,
				Class: root.class,
				Size:  info.Size(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for uuid := range out {
		sort.Slice(out[uuid], func(i, j int) bool {
			if localMediaPriority(out[uuid][i]) != localMediaPriority(out[uuid][j]) {
				return localMediaPriority(out[uuid][i]) < localMediaPriority(out[uuid][j])
			}
			if out[uuid][i].Size != out[uuid][j].Size {
				return out[uuid][i].Size < out[uuid][j].Size
			}
			return out[uuid][i].Path < out[uuid][j].Path
		})
	}
	return out, nil
}

func localMediaPriority(candidate LocalMediaCandidate) int {
	switch candidate.Class {
	case "derivative":
		return 1
	case "render":
		return 2
	case "original":
		return 3
	default:
		return 10
	}
}

func classifiableResource(resource Resource) bool {
	value := strings.ToLower(strings.Join([]string{
		resource.Type,
		resource.UTI,
		resource.OriginalFilename,
		resource.LocalPath,
	}, " "))
	return strings.Contains(value, "image") ||
		strings.Contains(value, "photo") ||
		strings.Contains(value, "heic") ||
		strings.Contains(value, "jpeg") ||
		strings.Contains(value, "jpg") ||
		strings.Contains(value, "png")
}

func classifiablePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic":
		return true
	default:
		return false
	}
}

func mediaUUID(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) < 36 {
		return ""
	}
	for start := 0; start <= len(value)-36; start++ {
		candidate := value[start : start+36]
		if isUUID(candidate) {
			return candidate
		}
	}
	return ""
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

func utiForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "public.jpeg"
	case ".png":
		return "public.png"
	case ".heic":
		return "public.heic"
	default:
		return "public.image"
	}
}
