package library

import "time"

// Collection is a user-defined grouping of videos. A hidden collection's members
// are kept out of the normal library/search/model views (see notHidden); a locked
// collection is meant to require a gate in the UI before its page opens. Both
// flags exist to keep sensitive content (e.g. adult media) separate and private.
type Collection struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Hidden  bool   `json:"hidden"`
	Locked  bool   `json:"locked"`
	Count   int    `json:"count"` // number of member videos
	Created string `json:"created"`
}

// Collections lists every collection with its member count, newest first.
func (db *DB) Collections() ([]Collection, error) {
	rows, err := db.sql.Query(`
SELECT c.id, c.name, c.hidden, c.locked, COALESCE(c.created,''),
       (SELECT COUNT(*) FROM collection_items ci WHERE ci.collection_id = c.id)
FROM collections c
ORDER BY c.created DESC, c.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		var hidden, locked int
		if err := rows.Scan(&c.ID, &c.Name, &hidden, &locked, &c.Created, &c.Count); err != nil {
			return nil, err
		}
		c.Hidden, c.Locked = hidden == 1, locked == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCollection makes a new collection and returns its id.
func (db *DB) CreateCollection(name string, hidden bool) (int64, error) {
	res, err := db.sql.Exec(`INSERT INTO collections (name, hidden, created) VALUES (?,?,?)`,
		name, b2i(hidden), time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RenameCollection changes a collection's name.
func (db *DB) RenameCollection(id int64, name string) error {
	_, err := db.sql.Exec(`UPDATE collections SET name=? WHERE id=?`, name, id)
	return err
}

// SetCollectionHidden toggles whether a collection's members hide from default views.
func (db *DB) SetCollectionHidden(id int64, hidden bool) error {
	_, err := db.sql.Exec(`UPDATE collections SET hidden=? WHERE id=?`, b2i(hidden), id)
	return err
}

// SetCollectionLocked toggles a collection's locked flag.
func (db *DB) SetCollectionLocked(id int64, locked bool) error {
	_, err := db.sql.Exec(`UPDATE collections SET locked=? WHERE id=?`, b2i(locked), id)
	return err
}

// DeleteCollection removes a collection and its memberships (the videos remain).
func (db *DB) DeleteCollection(id int64) error {
	if _, err := db.sql.Exec(`DELETE FROM collection_items WHERE collection_id=?`, id); err != nil {
		return err
	}
	_, err := db.sql.Exec(`DELETE FROM collections WHERE id=?`, id)
	return err
}

// AddToCollection adds a video (site+id) to a collection. Idempotent.
func (db *DB) AddToCollection(id int64, site, videoID string) error {
	_, err := db.sql.Exec(`
INSERT INTO collection_items (collection_id, site, video_id, added) VALUES (?,?,?,?)
ON CONFLICT(collection_id, site, video_id) DO NOTHING`,
		id, site, videoID, time.Now().Format("2006-01-02 15:04:05"))
	return err
}

// RemoveFromCollection drops a video from a collection.
func (db *DB) RemoveFromCollection(id int64, site, videoID string) error {
	_, err := db.sql.Exec(`DELETE FROM collection_items WHERE collection_id=? AND site=? AND video_id=?`,
		id, site, videoID)
	return err
}

// VideosByCollection returns a collection's members, newest-added first. This
// deliberately omits the notHidden filter: opening a hidden collection is how you
// view the content you've chosen to keep out of the normal library.
func (db *DB) VideosByCollection(id int64) ([]Video, error) {
	return db.query(`
JOIN collection_items ci ON ci.site = videos.site AND ci.video_id = videos.id
WHERE ci.collection_id = ? ORDER BY ci.added DESC, videos.id`, id)
}

// CollectionsForVideo returns the ids of collections a video belongs to (so the
// UI can show/toggle membership).
func (db *DB) CollectionsForVideo(site, videoID string) ([]int64, error) {
	rows, err := db.sql.Query(`SELECT collection_id FROM collection_items WHERE site=? AND video_id=?`, site, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}
