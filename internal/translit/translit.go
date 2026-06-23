// Пакет translit предоставляет нормализацию текста и русско-английскую
// транслитерацию, чтобы нечёткий поиск работал между алфавитами.
package translit

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// cyrToLat сопоставляет одну кириллическую руну её латинскому представлению.
var cyrToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// latPair — упорядоченное правило Latin->Cyrillic. Многосимвольные правила
// должны идти первыми, чтобы жадный алгоритм предпочитал длиннейшую последовательность.
type latPair struct {
	lat string
	cyr string
}

var latToCyr = []latPair{
	{"sch", "щ"}, {"shch", "щ"},
	{"zh", "ж"}, {"kh", "х"}, {"ts", "ц"}, {"ch", "ч"}, {"sh", "ш"},
	{"yu", "ю"}, {"ya", "я"}, {"yo", "ё"}, {"ye", "е"},
	{"a", "а"}, {"b", "б"}, {"v", "в"}, {"g", "г"}, {"d", "д"}, {"e", "е"},
	{"z", "з"}, {"i", "и"}, {"j", "й"}, {"y", "й"}, {"k", "к"}, {"l", "л"},
	{"m", "м"}, {"n", "н"}, {"o", "о"}, {"p", "п"}, {"r", "р"}, {"s", "с"},
	{"t", "т"}, {"u", "у"}, {"f", "ф"}, {"c", "к"}, {"h", "х"}, {"w", "в"},
	{"x", "кс"}, {"q", "к"},
}

// Normalize приводит строку к нижнему регистру, схлопывает пробелы и убирает
// символы, которые не являются буквами, цифрами или пробелами.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			lastSpace = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// ToLatin транслитерирует кириллические символы в латиницу, оставляя прочие
// руны без изменений.
func ToLatin(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if v, ok := cyrToLat[r]; ok {
			b.WriteString(v)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ToCyrillic транслитерирует латинские символы в кириллицу жадным поиском
// длиннейшего совпадения, оставляя прочие руны без изменений.
func ToCyrillic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		matched := false
		for _, p := range latToCyr {
			if strings.HasPrefix(s[i:], p.lat) {
				b.WriteString(p.cyr)
				i += len(p.lat)
				matched = true
				break
			}
		}
		if !matched {
			_, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// Variants возвращает нормализованные формы s, важные для межалфавитного поиска:
// сам нормализованный текст плюс его латинскую и кириллическую транслитерации.
// Дубликаты удаляются.
func Variants(s string) []string {
	base := Normalize(s)
	if base == "" {
		return nil
	}
	out := []string{base}
	add := func(v string) {
		v = Normalize(v)
		if v == "" {
			return
		}
		for _, e := range out {
			if e == v {
				return
			}
		}
		out = append(out, v)
	}
	add(ToLatin(base))
	add(ToCyrillic(base))
	return out
}
