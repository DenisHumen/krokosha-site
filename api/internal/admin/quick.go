package admin

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Quick answers (docs/quick-replies.md): the pool of templates the card of a request picks
// from, and the editor of each — what it says, when it fits, the words of the client it answers,
// and the photos, videos and documents that go to the client with it.

// uploadLimit caps a form with files: ten files, the largest of them videos of 50 MB, do not fit —
// a whole album of videos goes in two answers; photos and documents fit easily.
const uploadLimit = 110 << 20

var categoryNames = map[string]string{
	"greeting": "Приветствие", "about": "О себе", "skills": "Умения", "portfolio": "Работы", "pricing": "Стоимость",
	"timeline": "Сроки", "help": "Помощь", "options": "Варианты", "details": "Детали", "call": "Созвон",
	"process": "Как работаю", "payment": "Оплата", "offer": "Предложение", "contacts": "Полезное",
	"support": "Поддержка", "nda": "Конфиденциальность", "followup": "Напоминание", "thanks": "Завершение",
	"": "Другое",
}

var momentNames = map[string]string{
	leads.MomentAny: "в любой момент", leads.MomentFirst: "первый ответ", leads.MomentTalk: "в разговоре",
	leads.MomentWaiting: "клиент молчит", leads.MomentDone: "заказ выполнен",
}

var kindNames = map[string]string{"reply": "Ответы", "reject": "Отказы"}

// mediaTypes are the types the files of templates are shown with; documents are downloads.
var mediaTypes = map[string]string{
	leads.KindJPG: "image/jpeg", leads.KindPNG: "image/png", leads.KindMP4: "video/mp4",
	leads.KindPDF: "application/pdf", leads.KindTXT: "text/plain; charset=utf-8",
	leads.KindDOCX: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

type choice struct {
	ID, Name string
	Count    int
	Current  bool
	Query    string
}

type templateGroup struct {
	Category, Name string
	Items          []leads.Template
}

type templatesData struct {
	Kind, Lang, Search string
	Kinds, Langs       []choice
	Groups             []templateGroup
	Total              int
}

func (h *Handler) templatesPage(w http.ResponseWriter, r *http.Request) {
	all, err := h.opts.Leads.Templates(r.Context(), "")
	if err != nil {
		h.fail(w, r, "cannot read the templates", err)
		return
	}
	query := r.URL.Query()
	data := templatesData{Kind: query.Get("kind"), Lang: query.Get("lang"), Search: strings.TrimSpace(query.Get("q"))}
	if data.Kind != "reject" {
		data.Kind = "reply"
	}
	if _, ok := languageNames[data.Lang]; !ok || data.Lang == "" {
		data.Lang = "ru"
	}
	count := map[string]int{}
	for _, item := range all {
		count[item.Kind+":"+item.Lang]++
		count[item.Kind]++
	}
	for _, kind := range []string{"reply", "reject"} {
		data.Kinds = append(data.Kinds, choice{ID: kind, Name: kindNames[kind], Count: count[kind], Current: kind == data.Kind,
			Query: url.Values{"kind": {kind}, "lang": {data.Lang}}.Encode()})
	}
	for _, lang := range []string{"ru", "uk", "en"} {
		data.Langs = append(data.Langs, choice{ID: lang, Name: languageNames[lang], Count: count[data.Kind+":"+lang], Current: lang == data.Lang,
			Query: url.Values{"kind": {data.Kind}, "lang": {lang}}.Encode()})
	}
	search := strings.ToLower(data.Search)
	groups := map[string]*templateGroup{}
	for _, item := range all {
		if item.Kind != data.Kind || item.Lang != data.Lang {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(item.Title+"\n"+item.Body+"\n"+strings.Join(item.Keywords, " ")), search) {
			continue
		}
		group := groups[item.Category]
		if group == nil {
			group = &templateGroup{Category: item.Category, Name: categoryNames[item.Category]}
			groups[item.Category] = group
		}
		group.Items = append(group.Items, item)
		data.Total++
	}
	for _, category := range append(append([]string(nil), leads.Categories...), "") {
		if group := groups[category]; group != nil {
			data.Groups = append(data.Groups, *group)
			delete(groups, category)
		}
	}
	for _, group := range groups { // a category the code does not know yet
		data.Groups = append(data.Groups, *group)
	}
	h.render(w, r, http.StatusOK, "templates", view{Title: "Шаблоны", Nav: "leads", Data: data})
}

type templateData struct {
	Template   leads.Template
	New        bool
	Keywords   string
	Categories []choice
	Moments    []choice
	Directions []choice
	Langs      []choice
	Kinds      []choice
	Problems   []string // what was wrong with the files just sent
}

func (h *Handler) templateNew(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	item := leads.Template{Kind: query.Get("kind"), Lang: query.Get("lang"), Moment: leads.MomentAny}
	if item.Kind != "reject" {
		item.Kind = "reply"
	}
	if _, ok := languageNames[item.Lang]; !ok || item.Lang == "" {
		item.Lang = "ru"
	}
	h.showTemplate(w, r, http.StatusOK, item, "", nil)
}

func (h *Handler) templateEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.notFound(w, r, "Шаблон не найден")
		return
	}
	item, err := h.opts.Leads.Template(r.Context(), id)
	if errors.Is(err, leads.ErrNotFound) {
		h.notFound(w, r, "Шаблон не найден: возможно, его удалили.")
		return
	}
	if err != nil {
		h.fail(w, r, "cannot read a template", err)
		return
	}
	h.showTemplate(w, r, http.StatusOK, *item, "", nil)
}

func (h *Handler) showTemplate(w http.ResponseWriter, r *http.Request, status int, item leads.Template, problem string, problems []string) {
	data := templateData{Template: item, New: item.ID == 0, Keywords: strings.Join(item.Keywords, ", "), Problems: problems}
	for _, category := range append([]string{""}, leads.Categories...) {
		data.Categories = append(data.Categories, choice{ID: category, Name: categoryNames[category], Current: category == item.Category})
	}
	for _, moment := range leads.Moments {
		data.Moments = append(data.Moments, choice{ID: moment, Name: momentNames[moment], Current: moment == item.Moment})
	}
	for _, option := range h.opts.Form().Directions {
		data.Directions = append(data.Directions, choice{ID: option.ID, Name: h.directionName(option.ID), Current: containsString(item.Directions, option.ID)})
	}
	for _, lang := range []string{"ru", "uk", "en"} {
		data.Langs = append(data.Langs, choice{ID: lang, Name: languageNames[lang], Current: lang == item.Lang})
	}
	for _, kind := range []string{"reply", "reject"} {
		data.Kinds = append(data.Kinds, choice{ID: kind, Name: kindNames[kind], Current: kind == item.Kind})
	}
	title := "Новый шаблон"
	if item.ID > 0 {
		title = "Шаблон · " + item.Title
	}
	h.render(w, r, status, "template", view{Title: title, Nav: "leads", Error: problem, Data: data})
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// splitWords reads the keywords of the editor: separated by commas or by lines.
func splitWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
}

func (h *Handler) templateSave(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if r.PostFormValue("delete") != "" && id > 0 {
		err := h.opts.Leads.DeleteTemplate(r.Context(), id)
		if errors.Is(err, leads.ErrFilesLeft) {
			h.opts.Log.Warn("a deleted template left files behind; the daily sweep will remove them", "error", err)
			err = nil
		}
		if err != nil {
			h.fail(w, r, "cannot delete a template", err)
			return
		}
		h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "template.delete", strconv.FormatInt(id, 10), "", h.attemptMeta(r).IPPrefix)
		http.Redirect(w, r, h.opts.Prefix+"/templates?ok=template-deleted", http.StatusSeeOther)
		return
	}
	item := leads.Template{
		ID: id, Kind: r.PostFormValue("kind"), Lang: r.PostFormValue("lang"), Title: r.PostFormValue("title"), Body: r.PostFormValue("body"),
		Category: r.PostFormValue("category"), Moment: r.PostFormValue("moment"), Keywords: splitWords(r.PostFormValue("keywords")),
		Directions: r.PostForm["direction"],
	}
	if id > 0 { // the files are not part of the form: they stay as they are
		if saved, err := h.opts.Leads.Template(r.Context(), id); err == nil {
			item.Media = saved.Media
		}
	}
	saved, err := h.opts.Leads.SaveTemplate(r.Context(), item)
	switch {
	case err == nil:
		http.Redirect(w, r, fmt.Sprintf("%s/templates/%d?ok=template-saved", h.opts.Prefix, saved), http.StatusSeeOther)
	case errors.Is(err, leads.ErrEmptyText):
		h.showTemplate(w, r, http.StatusBadRequest, item, "У шаблона должны быть название и текст.", nil)
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Такого шаблона уже нет.")
	case errors.Is(err, leads.ErrBadTemplate):
		h.showTemplate(w, r, http.StatusBadRequest, item, "Шаблон не сохранён: проверьте вид, язык, категорию и момент.", nil)
	default:
		h.fail(w, r, "cannot save a template", err)
	}
}

// fileProblem says in words why a file was not taken.
func fileProblem(name string, err error) string {
	switch {
	case errors.Is(err, leads.ErrFileTooBig):
		return fmt.Sprintf("«%s»: слишком большой — фото до %d МБ, видео до %d МБ, документы до %d МБ.", name,
			leads.MaxPhotoBytes>>20, leads.MaxVideoBytes>>20, leads.MaxDocumentBytes>>20)
	case errors.Is(err, leads.ErrFileType):
		return fmt.Sprintf("«%s»: не фото (JPG, PNG), не видео (MP4) и не документ (PDF, DOCX, TXT) — или внутри не то, что в названии.", name)
	case errors.Is(err, leads.ErrTooManyFiles):
		return fmt.Sprintf("«%s»: у шаблона уже %d файлов — больше Telegram не покажет одним альбомом.", name, leads.MaxOutgoingFiles)
	default:
		return fmt.Sprintf("«%s»: не сохранился.", name)
	}
}

func (h *Handler) templateMediaAdd(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.notFound(w, r, "Шаблон не найден")
		return
	}
	var problems []string
	added := 0
	if r.MultipartForm != nil {
		for _, header := range r.MultipartForm.File["files"] {
			if header.Filename == "" && header.Size == 0 {
				continue
			}
			file, err := header.Open()
			if err == nil {
				_, err = h.opts.Leads.AddTemplateMedia(r.Context(), id, header.Filename, header.Size, file)
				_ = file.Close()
			}
			switch {
			case errors.Is(err, leads.ErrNotFound):
				h.notFound(w, r, "Шаблон не найден: возможно, его удалили.")
				return
			case err != nil:
				if !errors.Is(err, leads.ErrFileType) && !errors.Is(err, leads.ErrFileTooBig) && !errors.Is(err, leads.ErrTooManyFiles) {
					h.opts.Log.Error("cannot keep a file of a template", "error", err)
				}
				problems = append(problems, fileProblem(leads.CleanFilename(header.Filename), err))
			default:
				added++
			}
		}
	}
	if added > 0 {
		h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "template.media", strconv.FormatInt(id, 10), fmt.Sprintf("файлов: %d", added), h.attemptMeta(r).IPPrefix)
	}
	if len(problems) == 0 {
		flash := "media-added"
		if added == 0 {
			flash = "template-saved"
		}
		http.Redirect(w, r, fmt.Sprintf("%s/templates/%d?ok=%s#media", h.opts.Prefix, id, flash), http.StatusSeeOther)
		return
	}
	item, err := h.opts.Leads.Template(r.Context(), id)
	if err != nil {
		h.fail(w, r, "cannot read a template", err)
		return
	}
	message := "Не все файлы добавлены."
	if added == 0 {
		message = "Файлы не добавлены."
	}
	h.showTemplate(w, r, http.StatusBadRequest, *item, message, problems)
}

func (h *Handler) templateMediaRemove(w http.ResponseWriter, r *http.Request) {
	id, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	media, err2 := strconv.ParseInt(r.PathValue("media"), 10, 64)
	if err1 != nil || err2 != nil {
		h.notFound(w, r, "Файл не найден")
		return
	}
	err := h.opts.Leads.RemoveTemplateMedia(r.Context(), id, media)
	if errors.Is(err, leads.ErrFilesLeft) {
		h.opts.Log.Warn("a removed file of a template is still on disk; the daily sweep will remove it", "error", err)
		err = nil
	}
	switch {
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Файл не найден: возможно, его уже убрали.")
	case err != nil:
		h.fail(w, r, "cannot remove a file of a template", err)
	default:
		http.Redirect(w, r, fmt.Sprintf("%s/templates/%d?ok=media-removed#media", h.opts.Prefix, id), http.StatusSeeOther)
	}
}

// templateMedia shows a file of a template: photos and videos in the page, documents as downloads.
// Its kind was told by its content when it came, so the type is what it says.
func (h *Handler) templateMedia(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("media"), 10, 64)
	if err != nil || id <= 0 {
		h.notFound(w, r, "Файл не найден")
		return
	}
	file, content, err := h.opts.Leads.OpenTemplateMedia(r.Context(), id)
	if errors.Is(err, leads.ErrNotFound) {
		h.notFound(w, r, "Файл не найден")
		return
	}
	if err != nil {
		h.fail(w, r, "cannot open a file of a template", err)
		return
	}
	defer content.Close()
	header := w.Header()
	header.Set("Content-Type", mediaTypes[file.Kind])
	disposition := "attachment"
	if leads.IsPhoto(file.Kind) || leads.IsVideo(file.Kind) {
		disposition = "inline"
	}
	if value := mime.FormatMediaType(disposition, map[string]string{"filename": file.Filename}); value != "" {
		disposition = value
	}
	header.Set("Content-Disposition", disposition)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A video is played in parts (Range): ServeContent answers those.
	stat, err := content.Stat()
	if err != nil {
		h.fail(w, r, "cannot read a file of a template", err)
		return
	}
	http.ServeContent(w, r, "", stat.ModTime(), content)
}

// reasonLine says why a template came up: «сколько стоит» · «когда» · первый ответ.
func reasonLine(reasons []leads.Reason) string {
	var parts []string
	for _, reason := range reasons {
		switch reason.Kind {
		case "word":
			parts = append(parts, "«"+reason.Text+"»")
		case "moment":
			parts = append(parts, momentNames[reason.Text])
		case "direction":
			parts = append(parts, "для этого направления")
		case "short":
			parts = append(parts, "мало подробностей")
		case "sent":
			parts = append(parts, "уже отправлен")
		}
	}
	return strings.Join(parts, " · ")
}

// wasSent: the template went into an answer of this request already.
func wasSent(reasons []leads.Reason) bool {
	for _, reason := range reasons {
		if reason.Kind == "sent" {
			return true
		}
	}
	return false
}

// mediaCount names the files of a template: «2 фото · 1 видео».
func mediaCount(media []leads.Media) string {
	photos, videos, documents := 0, 0, 0
	for _, file := range media {
		switch {
		case leads.IsPhoto(file.Kind):
			photos++
		case leads.IsVideo(file.Kind):
			videos++
		default:
			documents++
		}
	}
	var parts []string
	for _, part := range []struct {
		n    int
		name string
	}{{photos, "фото"}, {videos, "видео"}, {documents, "док."}} {
		if part.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.n, part.name))
		}
	}
	return strings.Join(parts, " · ")
}
