package library

import "database/sql"

// Raw accessors for the flat-layout migration: they expose the STORED
// (root-relative) path values with no hidden-collection filtering and no
// abs/rel conversion, so the migration can account for every row exactly as
// the catalogue records it.

// RawVideo is one videos row with its stored path values.
type RawVideo struct {
	Site, ID   string
	Filepath   string // as stored (relative to root, or legacy absolute)
	Filename   string
	Thumbnail  string // as stored
	Title      string
	Uploader   string
	Duration   *int
	Width      *int
	Height     *int
}

// RawVideos returns every catalogue row (hidden-collection members included).
func (db *DB) RawVideos() ([]RawVideo, error) {
	rows, err := db.sql.Query(`SELECT site, id, COALESCE(filepath,''), COALESCE(filename,''),
		COALESCE(thumbnail,''), COALESCE(title,''), COALESCE(uploader,''), duration, width, height
		FROM videos ORDER BY site, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawVideo
	for rows.Next() {
		var v RawVideo
		var dur, w, h sql.NullInt64
		if err := rows.Scan(&v.Site, &v.ID, &v.Filepath, &v.Filename, &v.Thumbnail,
			&v.Title, &v.Uploader, &dur, &w, &h); err != nil {
			return nil, err
		}
		v.Duration, v.Width, v.Height = nint(dur), nint(w), nint(h)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetVideoStoredPaths rewrites a row's path columns with already-relative values.
func (db *DB) SetVideoStoredPaths(site, id, filepathRel, filename, thumbnailRel string) error {
	_, err := db.sql.Exec(`UPDATE videos SET filepath=?, filename=?, thumbnail=? WHERE site=? AND id=?`,
		filepathRel, filename, thumbnailRel, site, id)
	return err
}

// RawPhoto is one photos row with its stored path values.
type RawPhoto struct {
	ID, Model, Filepath, Filename string
}

// RawPhotos returns every photo row.
func (db *DB) RawPhotos() ([]RawPhoto, error) {
	rows, err := db.sql.Query(`SELECT id, COALESCE(model,''), COALESCE(filepath,''), COALESCE(filename,'') FROM photos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawPhoto
	for rows.Next() {
		var p RawPhoto
		if err := rows.Scan(&p.ID, &p.Model, &p.Filepath, &p.Filename); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPhotoStoredPath rewrites a photo row's path columns.
func (db *DB) SetPhotoStoredPath(id, filepathRel, filename string) error {
	_, err := db.sql.Exec(`UPDATE photos SET filepath=?, filename=? WHERE id=?`, filepathRel, filename, id)
	return err
}

// RawModelCovers returns every model's stored cover path (skipping empties).
func (db *DB) RawModelCovers() (map[string]string, error) {
	rows, err := db.sql.Query(`SELECT name, COALESCE(cover,'') FROM model_info WHERE COALESCE(cover,'')<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, c string
		if err := rows.Scan(&n, &c); err != nil {
			return nil, err
		}
		out[n] = c
	}
	return out, rows.Err()
}

// SetModelCoverStored rewrites a model's cover with an already-relative value.
func (db *DB) SetModelCoverStored(name, coverRel string) error {
	_, err := db.sql.Exec(`UPDATE model_info SET cover=? WHERE name=?`, coverRel, name)
	return err
}

// ProbeExclusive briefly takes the database write lock, failing fast if some
// other process (a running daemon) is writing. A guard, not a guarantee.
func (db *DB) ProbeExclusive() error {
	if _, err := db.sql.Exec(`BEGIN EXCLUSIVE`); err != nil {
		return err
	}
	_, err := db.sql.Exec(`ROLLBACK`)
	return err
}
