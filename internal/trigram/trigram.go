// Пакет trigram извлекает символьные триграммы, используемые как ключи
// инвертированного индекса. Работает по рунам, поэтому кириллица и латиница
// обрабатываются единообразно.
package trigram

// Size — длина окна триграммы.
const Size = 3

// Of возвращает уникальные триграммы (скользящие окна по 3 руны) строки s.
// Ожидается, что вход уже нормализован пакетом translit.
func Of(s string) []string {
	r := []rune(s)
	if len(r) < Size {
		return nil
	}
	seen := make(map[string]struct{}, len(r))
	out := make([]string, 0, len(r))
	for i := 0; i+Size <= len(r); i++ {
		g := string(r[i : i+Size])
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out
}

// OfVariants возвращает объединение триграмм по всем вариантам текста
// (напр. оригинал + транслитерации).
func OfVariants(variants []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, v := range variants {
		for _, g := range Of(v) {
			if _, ok := seen[g]; ok {
				continue
			}
			seen[g] = struct{}{}
			out = append(out, g)
		}
	}
	return out
}
