package leads

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// mp4File is the start of an MPEG-4 file: the size of the first box, «ftyp» and the brand.
func mp4File(brand string) []byte {
	return append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', brand[0], brand[1], brand[2], brand[3], 0, 0, 2, 0}, bytes.Repeat([]byte{1}, 400)...)
}

func TestInspectMediaTellsWhatGoesToAClient(t *testing.T) {
	for name, tc := range map[string]struct {
		file    string
		content []byte
		size    int64
		kind    string
		err     error
	}{
		"a photo":                  {"cat.JPG", jpgFile(), 0, KindJPG, nil},
		"a screenshot":             {"plan.png", pngFile(), 0, KindPNG, nil},
		"a video":                  {"demo.mp4", mp4File("isom"), 0, KindMP4, nil},
		"a video of an old phone":  {"clip.m4v", mp4File("M4V "), 0, KindMP4, nil},
		"a proposal":               {"offer.pdf", pdfFile(), 0, KindPDF, nil},
		"QuickTime named as mp4":   {"clip.mp4", mp4File("qt  "), 0, "", ErrFileType},
		"QuickTime":                {"clip.mov", mp4File("qt  "), 0, "", ErrFileType},
		"a program named as video": {"demo.mp4", programFile(), 0, "", ErrFileType},
		"a photo over 10 MB":       {"big.jpg", jpgFile(), MaxPhotoBytes + 1, "", ErrFileTooBig},
		"a video of 50 MB":         {"long.mp4", mp4File("mp42"), MaxVideoBytes, KindMP4, nil},
		"a video over 50 MB":       {"longer.mp4", mp4File("mp42"), MaxVideoBytes + 1, "", ErrFileTooBig},
		"a document over 20 MB":    {"big.pdf", pdfFile(), MaxDocumentBytes + 1, "", ErrFileTooBig},
	} {
		size := tc.size
		if size == 0 {
			size = int64(len(tc.content))
		}
		kind, err := InspectMedia(tc.file, size, bytes.NewReader(tc.content))
		if kind != tc.kind || !errors.Is(err, tc.err) {
			t.Errorf("%s: %q, %v; want %q, %v", name, kind, err, tc.kind, tc.err)
		}
	}
}

// The pool of quick answers is there in every language of the site, sorted by what each answers.
func TestThePoolOfQuickAnswers(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	all, err := store.Templates(context.Background(), "reply")
	if err != nil {
		t.Fatal(err)
	}
	byLang := map[string]map[string]int{}
	for _, item := range all {
		if byLang[item.Lang] == nil {
			byLang[item.Lang] = map[string]int{}
		}
		byLang[item.Lang][item.Category]++
		if !slices.Contains(Moments, item.Moment) {
			t.Errorf("%s «%s»: moment %q", item.Lang, item.Title, item.Moment)
		}
		if item.Category != "followup" && len(item.Keywords) == 0 {
			t.Errorf("%s «%s»: no keywords", item.Lang, item.Title)
		}
	}
	for _, lang := range []string{"en", "uk", "ru"} {
		for _, category := range Categories {
			if byLang[lang][category] == 0 {
				t.Errorf("%s: no template «%s»", lang, category)
			}
		}
	}
}

func TestTemplatesKnowWhenTheyFit(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	id, err := store.SaveTemplate(ctx, Template{
		Kind: "reply", Lang: "uk", Title: "Ціна мережі", Category: "pricing", Body: "Мережа офісу — від {id}.",
		Keywords:   []string{" Ціна ", "вартість", "ЦІНА", "", "Ёлка"},
		Directions: []string{"networks", "Bad Direction", "networks", "highload-lan"},
		Moment:     MomentTalk,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Template(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.Keywords, []string{"ціна", "вартість", "елка"}) || !slices.Equal(saved.Directions, []string{"networks", "highload-lan"}) ||
		saved.Moment != MomentTalk || saved.Category != "pricing" || saved.UsedCount != 0 || !saved.UsedAt.IsZero() {
		t.Errorf("saved: %+v", saved)
	}
	if _, err := store.Template(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing template: %v", err)
	}
}

func TestTemplatesCarryFilesIntoAnswers(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(time.Hour)
	store := NewStore(f.db, func() time.Time { return f.now })
	store.UseFiles(f.files)
	mediaDir := filepath.Join(f.filesDir, "templates")
	store.UseMedia(NewFiles(mediaDir))
	ctx := context.Background()

	id, err := store.SaveTemplate(ctx, Template{Kind: "reply", Lang: "ru", Title: "Примеры", Category: "portfolio", Body: "Вот примеры работ."})
	if err != nil {
		t.Fatal(err)
	}
	photo, err := store.AddTemplateMedia(ctx, id, "шкаф.jpg", int64(len(jpgFile())), bytes.NewReader(jpgFile()))
	if err != nil {
		t.Fatal(err)
	}
	video, err := store.AddTemplateMedia(ctx, id, "стойка.mp4", int64(len(mp4File("isom"))), bytes.NewReader(mp4File("isom")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTemplateMedia(ctx, id, "setup.exe", int64(len(programFile())), bytes.NewReader(programFile())); !errors.Is(err, ErrFileType) {
		t.Errorf("a program for a template: %v", err)
	}
	if _, err := store.AddTemplateMedia(ctx, 99999, "a.jpg", int64(len(jpgFile())), bytes.NewReader(jpgFile())); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file for a missing template: %v", err)
	}
	template, _ := store.Template(ctx, id)
	if len(template.Media) != 2 || template.Media[0].ID != photo.ID || template.Media[1].Kind != KindMP4 {
		t.Fatalf("the files of the template: %+v", template.Media)
	}

	// An answer with the files of the template and one attached by hand.
	lead := f.seed(nil)
	own, err := SaveOutgoing(f.files, "смета.pdf", int64(len(pdfFile())), bytes.NewReader(pdfFile()))
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.ReplyWith(ctx, lead, "denis", Answer{
		Text: "Здравствуйте! Вот примеры.", Templates: []int64{id, 99999, id}, Media: []int64{photo.ID, video.ID}, Files: []Upload{own},
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := store.MessageFiles(ctx, lead, message)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].Filename != "шкаф.jpg" || files[1].Kind != KindMP4 || files[2].Filename != "смета.pdf" {
		t.Fatalf("the files of the answer: %+v", files)
	}
	// A copy of the template's photo, not the photo itself: one file on disk under two names.
	original, _ := os.Stat(filepath.Join(mediaDir, photo.StoredAs))
	copied, _ := os.Stat(filepath.Join(f.filesDir, files[0].StoredAs))
	if files[0].StoredAs == photo.StoredAs || original == nil || copied == nil || !os.SameFile(original, copied) {
		t.Errorf("the answer's photo is not a link of the template's: %s / %s", photo.StoredAs, files[0].StoredAs)
	}
	used, err := store.UsedTemplates(ctx, lead)
	if err != nil || len(used) != 1 || !used[id] {
		t.Errorf("templates of the request: %v %v", used, err)
	}
	if template, _ = store.Template(ctx, id); template.UsedCount != 1 || !template.UsedAt.Equal(f.now.UTC().Truncate(time.Millisecond)) {
		t.Errorf("the use of the template: %d at %v", template.UsedCount, template.UsedAt)
	}

	// The template goes; what was sent stays with the request.
	if err := store.DeleteTemplate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(mediaDir); len(entries) != 0 {
		t.Errorf("files of a deleted template are left: %d", len(entries))
	}
	_, content, err := store.OpenAttachment(ctx, lead, files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(content)
	_ = content.Close()
	if !bytes.Equal(body, jpgFile()) {
		t.Error("the sent photo went with its template")
	}

	// Only files: no text is needed. Eleven are too many for an album; a call carries none.
	extra, _ := SaveOutgoing(f.files, "схема.png", int64(len(pngFile())), bytes.NewReader(pngFile()))
	if _, err := store.ReplyWith(ctx, lead, "denis", Answer{Files: []Upload{extra}}); err != nil {
		t.Errorf("an answer of one file: %v", err)
	}
	if _, err := store.ReplyWith(ctx, lead, "denis", Answer{Text: "много", Files: make([]Upload, MaxOutgoingFiles+1)}); !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("eleven files: %v", err)
	}
	phone := f.seed(func(v map[string]string) { v["contact_method"], v["contact_value"] = "phone", "+380671234567" })
	lost, _ := SaveOutgoing(f.files, "фото.jpg", int64(len(jpgFile())), bytes.NewReader(jpgFile()))
	if _, err := store.ReplyWith(ctx, phone, "denis", Answer{Text: "Позвонил", Files: []Upload{lost}}); !errors.Is(err, ErrNoFilesByPhone) {
		t.Errorf("files with a call: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.filesDir, lost.StoredAs)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the file of an answer that was not stored is left on disk")
	}
}

// The pool as the migration seeds it, on questions clients really ask.
func TestThePoolAnswersTypicalQuestions(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	all, err := store.Templates(context.Background(), "reply")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		situation Situation
		top       []string
	}{
		"price and time, first": {
			Situation{Lang: "ru", Moment: MomentFirst, Last: "Здравствуйте! Сколько стоит настроить MikroTik в офисе и когда сможете сделать?"},
			[]string{"greeting", "pricing", "timeline"},
		},
		"what have you done": {
			Situation{Lang: "ru", Moment: MomentTalk, Last: "А что вы уже делали похожее? Есть примеры или кейсы?"},
			[]string{"portfolio"},
		},
		"can you help, Ukrainian": {
			Situation{Lang: "uk", Moment: MomentTalk, Last: "Чи зможете допомогти з налаштуванням серверів? Яка вартість?"},
			[]string{"help", "pricing"},
		},
		"what do you do, English": {
			Situation{Lang: "en", Moment: MomentTalk, Last: "Who are you exactly — a company or a freelancer? What do you do?"},
			[]string{"about"},
		},
		"payment and documents": {
			Situation{Lang: "ru", Moment: MomentTalk, Last: "Как будет оплата? Нужен договор и акт для бухгалтерии."},
			[]string{"payment"},
		},
	} {
		got := Rank(all, tc.situation)
		var top []string
		for _, item := range got {
			if item.Top {
				top = append(top, item.Template.Category)
			}
		}
		for _, want := range tc.top {
			if !slices.Contains(top, want) {
				t.Errorf("%s: the first three are %v, «%s» is not among them", name, top, want)
			}
		}
	}
}
