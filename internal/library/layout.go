package library

import "strings"

// Flat vault layout: every media file lives directly in <root>/media as
// "<site>-<id>.<ext>", and sidecars live under .keepsake (meta/ for the
// .info.json files, thumbs/ for poster images). Everything human-readable —
// titles, models, uploaders — lives only in the catalogue. The legacy
// per-site/per-uploader tree remains readable; `mediavaultd migrate-flat`
// converts a vault in place.
const (
	MediaDirName  = "media"
	MetaDirName   = "meta"   // under .keepsake
	ThumbsDirName = "thumbs" // under .keepsake
)

// FlatBase returns the canonical flat-layout basename (without extension) for
// a catalogue entry: "<site>-<id>". The site is lowercased and reduced to
// [a-z0-9]; the id keeps its case (ids can be case-sensitive) but is reduced
// to filesystem-safe characters. A redundant "<site>_" prefix on the id
// (Local ids are "local_<hash>", photo ids "photo_<hash>") is collapsed so
// the name reads "local-<hash>", not "local-local_<hash>".
func FlatBase(site, id string) string {
	s := slugSite(site)
	i := sanitizeID(id)
	if p := s + "_"; len(i) > len(p) && strings.EqualFold(i[:len(p)], p) {
		i = i[len(p):]
	}
	if s == "" {
		s = "unknown"
	}
	if i == "" {
		i = "unknown"
	}
	return s + "-" + i
}

func slugSite(site string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(site)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
