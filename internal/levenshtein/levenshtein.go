// Пакет levenshtein вычисляет редакторские расстояния для ранжирования
// кандидатов нечёткого поиска. Для больших наборов кандидатов пакетный
// подсчёт распределяет работу по горутинам.
package levenshtein

import (
	"runtime"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// ParallelThreshold — количество кандидатов, выше которого ScoreBatch работает
// параллельно.
const ParallelThreshold = 50

// Distance возвращает редакторское расстояние Левенштейна между a и b, работая по
// рунам, чтобы многобайтовые (напр. кириллические) символы считались одним знаком.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	return min(a, min(b, c))
}

// BestScore возвращает наибольшее сходство в [0,1] между query и любым из
// targets, где сходство = 1 - расстояние/max(len(query), len(target)). Это
// позволяет короткому запросу совпадать с ближайшим токеном/значением без
// штрафа за разницу в длине с другими targets.
func BestScore(query string, targets []string) float64 {
	ql := utf8.RuneCountInString(query)
	best := 0.0
	for _, tgt := range targets {
		denom := ql
		if tl := utf8.RuneCountInString(tgt); tl > denom {
			denom = tl
		}
		if denom == 0 {
			continue
		}
		s := 1 - float64(Distance(query, tgt))/float64(denom)
		if s > best {
			best = s
		}
	}
	return best
}

// ScoreBatch возвращает для каждого кандидата лучшее сходство (см. BestScore)
// между query и любой из его строк-целей. Когда количество кандидатов
// превышает ParallelThreshold, работа распределяется по воркерам.
func ScoreBatch(query string, candidates [][]string) []float64 {
	out := make([]float64, len(candidates))
	if len(candidates) <= ParallelThreshold {
		for i, targets := range candidates {
			out[i] = BestScore(query, targets)
		}
		return out
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > len(candidates) {
		workers = len(candidates)
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(candidates) {
					return
				}
				out[i] = BestScore(query, candidates[i])
			}
		}()
	}
	wg.Wait()
	return out
}
