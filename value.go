package fsearch

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nx-a/fsearch/internal/translit"
)

// valueToString представляет скалярное значение плоского JSON как строку для поиска.
// Вложенные объекты/массивы намеренно игнорируются (записи ожидаются плоскими).
func valueToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return string(t)
	default:
		return ""
	}
}

// runeLen возвращает количество рун в s.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// comparisonTargets формирует набор строк, с которыми сравнивается запрос для
// значения поля: каждый вариант транслитерации всего значения плюс каждый
// отдельный токен. Токены позволяют короткому запросу совпасть с одним словом
// длинного поля (напр. "иванов" в "Иванов Иван") без штрафа за длину.
func comparisonTargets(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, v := range translit.Variants(text) {
		add(v)
		for _, tok := range strings.Fields(v) {
			add(tok)
		}
	}
	return out
}
