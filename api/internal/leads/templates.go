package leads

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Ready-made answers and refusals (brief B10.4), and the quick answers built on them: a pool of
// templates the card of a request picks from by context (Rank), each able to carry photos, videos
// and documents that go to the client along with the text.

// Template is a ready-made answer or refusal, editable in the admin area.
type Template struct {
	ID       int64
	Kind     string // reply | reject
	Lang     string
	Title    string
	Category string // what it answers (Categories); "" — something else
	Body     string
	// Keywords are stems and phrases of a client's words: «стоим» finds «стоимость».
	Keywords []string
	// Directions of the form it is for; none — any.
	Directions []string
	Moment     string // Moments
	UsedCount  int
	UsedAt     time.Time // zero — never sent
	Media      []Media
}

// Media is a file of a template, kept in the directory of templates' files.
type Media struct {
	ID         int64
	TemplateID int64
	Filename   string
	Kind       string
	Size       int64
	SHA256     []byte
	StoredAs   string
}

// Categories of templates, in the order of the lists.
var Categories = []string{
	"greeting", "about", "skills", "portfolio", "pricing", "timeline", "help", "options", "details",
	"call", "process", "payment", "offer", "contacts", "support", "nda", "followup", "thanks",
}

// Moments of a conversation a template may be for.
const (
	MomentAny     = "any"
	MomentFirst   = "first"   // nothing has been answered yet
	MomentTalk    = "talk"    // the conversation goes on
	MomentWaiting = "waiting" // the answer is ours, and the client has been silent for days
	MomentDone    = "done"    // the order is done
)

// Moments in the order of the editor.
var Moments = []string{MomentAny, MomentFirst, MomentTalk, MomentWaiting, MomentDone}

// Errors of templates and of the files of answers.
var (
	ErrBadTemplate    = errors.New("a template is a reply or a reject, in a language of the site, of a known category and moment")
	ErrNoFilesByPhone = errors.New("a call has no files: the answer by phone is a note of the conversation")
)

var reDirection = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

const templateColumns = `id, kind, lang, title, category, body, keywords, directions, moment, used_count, used_at`

func scanTemplate(row interface{ Scan(...any) error }) (Template, error) {
	var item Template
	var keywords, directions string
	var usedAt sql.NullTime
	err := row.Scan(&item.ID, &item.Kind, &item.Lang, &item.Title, &item.Category, &item.Body, &keywords, &directions,
		&item.Moment, &item.UsedCount, &usedAt)
	item.Keywords, item.Directions = splitList(keywords), splitList(directions)
	if usedAt.Valid {
		item.UsedAt = usedAt.Time
	}
	return item, err
}

// splitList reads a comma-separated list of the database.
func splitList(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Templates lists the templates of a kind ("" = all) in the order of the editor, with their files.
func (s *Store) Templates(ctx context.Context, kind string) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateColumns+` FROM reply_templates WHERE (? = '' OR kind = ?) ORDER BY kind, lang, position, id`, kind, kind)
	if err != nil {
		return nil, err
	}
	var out []Template
	index := map[int64]int{}
	for rows.Next() {
		item, err := scanTemplate(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		index[item.ID] = len(out)
		out = append(out, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	media, err := s.templateMedia(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, file := range media {
		if at, ok := index[file.TemplateID]; ok {
			out[at].Media = append(out[at].Media, file)
		}
	}
	return out, nil
}

// Template reads one template with its files.
func (s *Store) Template(ctx context.Context, id int64) (*Template, error) {
	item, err := scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM reply_templates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if item.Media, err = s.templateMedia(ctx, id); err != nil {
		return nil, err
	}
	return &item, nil
}

// templateMedia lists the files of one template, or of all (0), in their order.
func (s *Store) templateMedia(ctx context.Context, templateID int64) ([]Media, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, template_id, filename, kind, size, sha256, stored_as FROM template_media
		WHERE (? = 0 OR template_id = ?) ORDER BY template_id, position, id`, templateID, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		var file Media
		if err := rows.Scan(&file.ID, &file.TemplateID, &file.Filename, &file.Kind, &file.Size, &file.SHA256, &file.StoredAs); err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

// CleanKeywords makes the list of a template's keywords: lower case, «ё» as «е», no repeats, each
// at most 60 characters and all of them at most 1000.
func CleanKeywords(list []string) []string {
	var out []string
	seen := map[string]bool{}
	total := 0
	for _, word := range list {
		word = strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(clean(word, false)), "ё", "е")), " ")
		word = strings.Trim(word, ",;")
		if word == "" || len([]rune(word)) > 60 || seen[word] || total+len(word)+2 > 1000 {
			continue
		}
		seen[word] = true
		total += len(word) + 2
		out = append(out, word)
	}
	return out
}

// SaveTemplate adds a template (ID 0) or changes one, and returns its id.
func (s *Store) SaveTemplate(ctx context.Context, item Template) (int64, error) {
	item.Title, item.Body = clean(item.Title, false), clean(item.Body, true)
	if item.Title == "" || item.Body == "" {
		return 0, ErrEmptyText
	}
	if item.Moment == "" {
		item.Moment = MomentAny
	}
	if (item.Kind != "reply" && item.Kind != "reject") || !languages[item.Lang] || !known(Moments, item.Moment) ||
		(item.Category != "" && !known(Categories, item.Category)) {
		return 0, ErrBadTemplate
	}
	var directions []string
	for _, direction := range item.Directions {
		if direction = strings.TrimSpace(direction); reDirection.MatchString(direction) && !known(directions, direction) {
			directions = append(directions, direction)
		}
	}
	keywords, directionList := strings.Join(CleanKeywords(item.Keywords), ", "), cut(strings.Join(directions, ","), 255)
	now := s.now().UTC()
	if item.ID == 0 {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO reply_templates (kind, lang, position, title, category, body, keywords, directions, moment, updated_at)
			SELECT ?, ?, COALESCE(MAX(position), 0) + 1, ?, ?, ?, ?, ?, ?, ? FROM reply_templates WHERE kind = ? AND lang = ?`,
			item.Kind, item.Lang, cut(item.Title, 100), item.Category, cut(item.Body, 8000), keywords, directionList, item.Moment, now,
			item.Kind, item.Lang)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE reply_templates SET kind = ?, lang = ?, title = ?, category = ?, body = ?, keywords = ?, directions = ?, moment = ?, updated_at = ?
		WHERE id = ?`,
		item.Kind, item.Lang, cut(item.Title, 100), item.Category, cut(item.Body, 8000), keywords, directionList, item.Moment, now, item.ID)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		// MySQL counts a row whose values did not change as not affected: is it there at all?
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reply_templates WHERE id = ?`, item.ID).Scan(&found); err != nil {
			return 0, err
		}
		if found == 0 {
			return 0, ErrNotFound
		}
	}
	return item.ID, nil
}

func known(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// DeleteTemplate removes a template and its files. The answers made from it keep their copies.
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	media, err := s.templateMedia(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reply_templates WHERE id = ?`, id); err != nil {
		return err
	}
	return s.removeMedia(media)
}

func (s *Store) removeMedia(media []Media) error {
	if s.media == nil {
		return nil
	}
	var failed error
	for _, file := range media {
		if err := s.media.Remove(file.StoredAs); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	if failed != nil {
		return errors.Join(ErrFilesLeft, failed)
	}
	return nil
}

// Readable is what an uploaded file gives: read in order, or at any place (multipart.File).
type Readable interface {
	io.Reader
	io.ReaderAt
}

// SaveOutgoing checks a file that is to go to a client (InspectMedia) and keeps it in dir under a
// new name. Nothing is written for a file that is not accepted.
func SaveOutgoing(dir *Files, filename string, size int64, content Readable) (Upload, error) {
	if dir == nil {
		return Upload{}, ErrNoFilesHere
	}
	kind, err := InspectMedia(filename, size, content)
	if err != nil {
		return Upload{}, err
	}
	return dir.SaveLimited(filename, kind, io.NewSectionReader(content, 0, size), MaxBytesOf(kind))
}

// AddTemplateMedia checks a file and gives it to a template: at most ten each, like an album.
func (s *Store) AddTemplateMedia(ctx context.Context, templateID int64, filename string, size int64, content Readable) (Media, error) {
	var exists, count int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM reply_templates WHERE id = ?), (SELECT COUNT(*) FROM template_media WHERE template_id = ?)`,
		templateID, templateID).Scan(&exists, &count)
	if err != nil {
		return Media{}, err
	}
	switch {
	case exists == 0:
		return Media{}, ErrNotFound
	case count >= MaxOutgoingFiles:
		return Media{}, ErrTooManyFiles
	}
	upload, err := SaveOutgoing(s.media, filename, size, content)
	if err != nil {
		return Media{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO template_media (template_id, position, created_at, filename, kind, size, sha256, stored_as)
		SELECT ?, COALESCE(MAX(position), 0) + 1, ?, ?, ?, ?, ?, ? FROM template_media WHERE template_id = ?`,
		templateID, s.now().UTC(), cut(upload.Filename, 255), upload.Kind, upload.Size, upload.SHA256, upload.StoredAs, templateID)
	if err == nil {
		var id int64
		if id, err = result.LastInsertId(); err == nil {
			return Media{ID: id, TemplateID: templateID, Filename: upload.Filename, Kind: upload.Kind, Size: upload.Size,
				SHA256: upload.SHA256, StoredAs: upload.StoredAs}, nil
		}
	}
	_ = s.media.Remove(upload.StoredAs)
	return Media{}, err
}

// RemoveTemplateMedia takes a file from a template.
func (s *Store) RemoveTemplateMedia(ctx context.Context, templateID, mediaID int64) error {
	var storedAs string
	err := s.db.QueryRowContext(ctx, `SELECT stored_as FROM template_media WHERE id = ? AND template_id = ?`, mediaID, templateID).Scan(&storedAs)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM template_media WHERE id = ?`, mediaID); err != nil {
		return err
	}
	return s.removeMedia([]Media{{StoredAs: storedAs}})
}

// OpenTemplateMedia opens a file of a template, for its preview in the admin area.
func (s *Store) OpenTemplateMedia(ctx context.Context, mediaID int64) (*Media, *os.File, error) {
	if s.media == nil {
		return nil, nil, ErrNotFound
	}
	var file Media
	err := s.db.QueryRowContext(ctx, `SELECT id, template_id, filename, kind, size, sha256, stored_as FROM template_media WHERE id = ?`, mediaID).
		Scan(&file.ID, &file.TemplateID, &file.Filename, &file.Kind, &file.Size, &file.SHA256, &file.StoredAs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	content, err := s.media.Open(file.StoredAs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &file, content, nil
}

// KnowsMedia reports whether a file on disk belongs to some template — the question of the sweep.
func (s *Store) KnowsMedia(ctx context.Context, storedAs string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM template_media WHERE stored_as = ?`, storedAs).Scan(&found)
	return found > 0, err
}

// UsedTemplates are the templates the answers of a request were made from.
func (s *Store) UsedTemplates(ctx context.Context, leadID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT templates FROM lead_messages WHERE lead_id = ? AND direction = 'out' AND templates IS NOT NULL`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int64]bool{}
	for rows.Next() {
		var list string
		if err := rows.Scan(&list); err != nil {
			return nil, err
		}
		for _, part := range splitList(list) {
			if id, err := strconv.ParseInt(part, 10, 64); err == nil {
				used[id] = true
			}
		}
	}
	return used, rows.Err()
}

// Filling is what the placeholders of a template become, besides {name} and {id}.
type Filling struct {
	Site    string // {site}: the site in the client's language, https://krokosha.com/ru/
	Account string // {account}: the request in the personal account
}

// LinksFor makes the links of a template for a request, from the public address of the site.
func LinksFor(siteURL string, lead *Lead) Filling {
	if siteURL == "" {
		return Filling{}
	}
	site := strings.TrimRight(siteURL, "/") + langPrefix(lead.Lang)
	return Filling{Site: site, Account: site + "account/#" + lead.Number()}
}

// FillTemplate puts the client's name, the number of the request and the links into a template.
func FillTemplate(body string, lead *Lead, with Filling) string {
	return strings.NewReplacer("{name}", lead.Name, "{id}", "#"+lead.Number(), "{site}", with.Site, "{account}", with.Account).Replace(body)
}

// joinIDs writes ids for a column of the database: «3,7,12».
func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

// placeholders: «?, ?, ?» for an IN of n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func idsAsArgs(ids []int64) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// existingTemplates keeps the ids of templates that are still there, each once, at most twenty.
func existingTemplates(ctx context.Context, tx *sql.Tx, ids []int64) ([]int64, error) {
	var unique []int64
	for _, id := range ids {
		if id > 0 && len(unique) < 20 && !knownID(unique, id) {
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return nil, nil
	}
	query := `SELECT id FROM reply_templates WHERE id IN (` + placeholders(len(unique)) + `)` //nolint:gosec // placeholders only
	rows, err := tx.QueryContext(ctx, query, idsAsArgs(unique)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = true
	}
	var out []int64
	for _, id := range unique {
		if found[id] {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func knownID(list []int64, id int64) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}
