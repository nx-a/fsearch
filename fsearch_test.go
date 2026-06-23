package fsearch

import (
	"path/filepath"
	"testing"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(Options{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestCreateAndSearchFuzzy(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("users", []string{"name"}); err != nil {
		t.Fatalf("create list: %v", err)
	}
	records := []map[string]any{
		{"name": "Иванов Иван"},
		{"name": "Петров Пётр"},
		{"name": "Сидоров Сидор"},
	}
	for _, r := range records {
		if _, err := e.AddRecord("users", r); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	// Fuzzy: one typo in "Иванов".
	res, err := e.Search("users", "name", "Иваноы", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].Record["name"] != "Иванов Иван" {
		t.Fatalf("expected Иванов Иван first, got %+v", res)
	}
}

func TestTransliterationSearch(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("users", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddRecord("users", map[string]any{"name": "Иванов"}); err != nil {
		t.Fatal(err)
	}
	// Latin query should match the Cyrillic record.
	res, err := e.Search("users", "name", "ivanov", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].Record["name"] != "Иванов" {
		t.Fatalf("transliteration match failed, got %+v", res)
	}
}

func TestMinQueryLen(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("users", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Search("users", "name", "abcd", 5); err != ErrQueryTooShort {
		t.Fatalf("expected ErrQueryTooShort, got %v", err)
	}
}

func TestUpdateRecordReindexes(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("users", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	id, err := e.AddRecord("users", map[string]any{"name": "Иванов"})
	if err != nil {
		t.Fatal(err)
	}
	// Заменяем значение поля; старое не должно находиться, новое — должно.
	if err := e.UpdateRecord("users", id, map[string]any{"name": "Петров"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.Search("users", "name", "иванов", 5); len(res) != 0 {
		t.Fatalf("old value should not match after update, got %+v", res)
	}
	res, err := e.Search("users", "name", "петров", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Record["name"] != "Петров" {
		t.Fatalf("updated value should match, got %+v", res)
	}
}

func TestDeleteRecord(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("users", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	id, _ := e.AddRecord("users", map[string]any{"name": "Сидоров"})
	if err := e.DeleteRecord("users", id); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.Search("users", "name", "сидоров", 5); len(res) != 0 {
		t.Fatalf("deleted record should not match, got %+v", res)
	}
	if err := e.DeleteRecord("users", id); err != ErrRecordNotFound {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestDeleteList(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("temp", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddRecord("temp", map[string]any{"name": "что-то"}); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteList("temp"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ListMeta("temp"); err != ErrListNotFound {
		t.Fatalf("expected ErrListNotFound after delete, got %v", err)
	}
}

func TestMultipleLists(t *testing.T) {
	e := newEngine(t)
	if err := e.CreateList("alpha", []string{"title"}); err != nil {
		t.Fatal(err)
	}
	if err := e.CreateList("beta", []string{"title"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddRecord("alpha", map[string]any{"title": "hello world"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddRecord("beta", map[string]any{"title": "different text"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.Search("alpha", "title", "hello", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected isolation between lists, got %d results", len(res))
	}
}
