// Пакет fsearch — небольшой встраиваемый движок нечёткого поиска по плоским
// JSON-записям. Он поддерживает:
//
//   - несколько именованных списков (каждый идентифицируется sysname);
//   - нечёткий поиск на основе триграмм по заданным полям;
//   - межалфавитный поиск через русско-английскую транслитерацию;
//   - минимальную длину запроса (по умолчанию 5 символов);
//   - хранение в bbolt;
//   - кеширование частых запросов;
//   - битсет-списки вхождений для очень частых триграмм;
//   - параллельный подсчёт Левенштейна для больших наборов кандидатов.
package fsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/nx-a/fsearch/internal/levenshtein"
	"github.com/nx-a/fsearch/internal/lru"
	"github.com/nx-a/fsearch/internal/store"
	"github.com/nx-a/fsearch/internal/translit"
	"github.com/nx-a/fsearch/internal/trigram"
)

// Значения конфигурации по умолчанию.
const (
	DefaultMinQueryLen   = 5
	DefaultCacheCapacity = 1024
	DefaultCacheMinHits  = 3
	DefaultMaxCandidates = 2000
	DefaultMinScore      = 0.5
)

var (
	// ErrQueryTooShort возвращается, когда нормализованный запрос короче
	// заданной минимальной длины.
	ErrQueryTooShort = errors.New("fsearch: query too short")
	// ErrListNotFound — список не найден.
	ErrListNotFound = store.ErrListNotFound
	// ErrListExists — список с таким sysname уже существует.
	ErrListExists = store.ErrListExists
	// ErrRecordNotFound — запись не найдена.
	ErrRecordNotFound = store.ErrRecordNotFound
)

// Options настраивает Engine.
type Options struct {
	// Path — путь к файлу БД bbolt (обязательно).
	Path string
	// MinQueryLen — минимальное число символов в запросе.
	MinQueryLen int
	// CacheCapacity — сколько кешированных результатов хранить.
	CacheCapacity int
	// CacheMinHits — сколько раз должен встретиться запрос перед кешированием.
	CacheMinHits int
	// MaxCandidates ограничивает число триграммных кандидатов на запрос.
	MaxCandidates int
	// MinScore — минимальное сходство (0..1) для включения в результат.
	MinScore float64
	// ReadOnly открывает базу только на чтение (для подов-читателей). В этом
	// режиме доступны лишь операции чтения/поиска; запись вернёт ошибку bbolt.
	ReadOnly bool
}

func (o *Options) applyDefaults() {
	if o.MinQueryLen <= 0 {
		o.MinQueryLen = DefaultMinQueryLen
	}
	if o.CacheCapacity <= 0 {
		o.CacheCapacity = DefaultCacheCapacity
	}
	if o.CacheMinHits <= 0 {
		o.CacheMinHits = DefaultCacheMinHits
	}
	if o.MaxCandidates <= 0 {
		o.MaxCandidates = DefaultMaxCandidates
	}
	if o.MinScore <= 0 {
		o.MinScore = DefaultMinScore
	}
}

// Result — одно совпадение поиска.
type Result struct {
	ID     uint64         `json:"id"`
	Score  float64        `json:"score"`
	Record map[string]any `json:"record"`
}

// Engine — поисковый движок.
type Engine struct {
	store *store.Store
	cache *lru.Cache[string, []Result]
	opts  Options
}

// Open создаёт или открывает движок, хранящий данные в файле bbolt по opts.Path.
func Open(opts Options) (*Engine, error) {
	if opts.Path == "" {
		return nil, errors.New("fsearch: Options.Path is required")
	}
	opts.applyDefaults()
	var st *store.Store
	var err error
	if opts.ReadOnly {
		st, err = store.OpenReadOnly(opts.Path)
	} else {
		st, err = store.Open(opts.Path)
	}
	if err != nil {
		return nil, err
	}
	return &Engine{
		store: st,
		cache: lru.New[string, []Result](opts.CacheCapacity, opts.CacheMinHits),
		opts:  opts,
	}, nil
}

// Close закрывает базу.
func (e *Engine) Close() error { return e.store.Close() }

// ReadOnly сообщает, открыт ли движок только на чтение.
func (e *Engine) ReadOnly() bool { return e.opts.ReadOnly }

// Snapshot пишет консистентную копию базы в w (для репликации на читателей).
func (e *Engine) Snapshot(w io.Writer) (int64, error) { return e.store.Snapshot(w) }

// CreateList регистрирует новый список с заданными поисковыми полями.
func (e *Engine) CreateList(sysname string, searchFields []string) error {
	return e.store.CreateList(store.ListMeta{Sysname: sysname, SearchFields: searchFields})
}

// Lists возвращает sysname всех списков.
func (e *Engine) Lists() ([]string, error) { return e.store.ListNames() }

// ListMeta возвращает метаданные списка (sysname и поисковые поля).
func (e *Engine) ListMeta(sysname string) (store.ListMeta, error) {
	return e.store.GetList(sysname)
}

// DeleteList удаляет список целиком вместе с его записями и индексом.
func (e *Engine) DeleteList(sysname string) error {
	if err := e.store.DeleteList(sysname); err != nil {
		return err
	}
	e.cache.Purge()
	return nil
}

// GetRecord возвращает запись по id. Второе значение — false, если записи нет.
func (e *Engine) GetRecord(sysname string, id uint64) (map[string]any, bool, error) {
	raw, err := e.store.GetRecord(sysname, id)
	if err != nil {
		return nil, false, err
	}
	if raw == nil {
		return nil, false, nil
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

// AddRecord сохраняет плоскую JSON-запись (в виде декодированной map) и
// индексирует настроенные поисковые поля. Возвращает присвоенный id.
func (e *Engine) AddRecord(sysname string, record map[string]any) (uint64, error) {
	meta, err := e.store.GetList(sysname)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	id, err := e.store.AddRecord(sysname, raw, e.fieldTrigrams(meta, record))
	if err != nil {
		return 0, err
	}
	// Индекс изменился: сбрасываем кеш, чтобы не отдавать устаревшие результаты.
	e.cache.Purge()
	return id, nil
}

// UpdateRecord заменяет существующую запись новым содержимым, сохраняя id,
// и перестраивает индекс по изменённым полям.
func (e *Engine) UpdateRecord(sysname string, id uint64, record map[string]any) error {
	meta, err := e.store.GetList(sysname)
	if err != nil {
		return err
	}
	oldRaw, err := e.store.GetRecord(sysname, id)
	if err != nil {
		return err
	}
	if oldRaw == nil {
		return ErrRecordNotFound
	}
	var oldRec map[string]any
	if err := json.Unmarshal(oldRaw, &oldRec); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := e.store.UpdateRecord(sysname, id, raw, e.fieldTrigrams(meta, oldRec), e.fieldTrigrams(meta, record)); err != nil {
		return err
	}
	e.cache.Purge()
	return nil
}

// DeleteRecord удаляет запись и исключает её из индекса.
func (e *Engine) DeleteRecord(sysname string, id uint64) error {
	meta, err := e.store.GetList(sysname)
	if err != nil {
		return err
	}
	oldRaw, err := e.store.GetRecord(sysname, id)
	if err != nil {
		return err
	}
	if oldRaw == nil {
		return ErrRecordNotFound
	}
	var oldRec map[string]any
	if err := json.Unmarshal(oldRaw, &oldRec); err != nil {
		return err
	}
	if err := e.store.DeleteRecord(sysname, id, e.fieldTrigrams(meta, oldRec)); err != nil {
		return err
	}
	e.cache.Purge()
	return nil
}

// fieldTrigrams вычисляет триграммы для каждого поискового поля записи.
func (e *Engine) fieldTrigrams(meta store.ListMeta, record map[string]any) map[string][]string {
	out := make(map[string][]string, len(meta.SearchFields))
	for _, field := range meta.SearchFields {
		v, ok := record[field]
		if !ok {
			continue
		}
		text := valueToString(v)
		if text == "" {
			continue
		}
		out[field] = trigram.OfVariants(translit.Variants(text))
	}
	return out
}

// AddJSON аналогичен AddRecord, но принимает сырые байты плоского JSON.
func (e *Engine) AddJSON(sysname string, raw []byte) (uint64, error) {
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		return 0, fmt.Errorf("fsearch: invalid record JSON: %w", err)
	}
	return e.AddRecord(sysname, record)
}

// Search выполняет нечёткий поиск по query в поле field списка sysname и
// возвращает до limit ранжированных результатов.
func (e *Engine) Search(sysname, field, query string, limit int) ([]Result, error) {
	if _, err := e.store.GetList(sysname); err != nil {
		return nil, err
	}
	queryVariants := translit.Variants(query)
	if len(queryVariants) == 0 || runeLen(queryVariants[0]) < e.opts.MinQueryLen {
		return nil, ErrQueryTooShort
	}
	if limit <= 0 {
		limit = 10
	}

	cacheKey := fmt.Sprintf("%s\x00%s\x00%s\x00%d", sysname, field, queryVariants[0], limit)
	if cached, ok := e.cache.Get(cacheKey); ok {
		return cached, nil
	}

	candidates, err := e.candidates(sysname, field, queryVariants)
	if err != nil {
		return nil, err
	}

	results, err := e.rank(sysname, field, queryVariants, candidates, limit)
	if err != nil {
		return nil, err
	}

	e.cache.Add(cacheKey, results)
	return results, nil
}

// candidates собирает id записей, делящих достаточно триграмм с запросом.
func (e *Engine) candidates(sysname, field string, queryVariants []string) ([]uint64, error) {
	trigrams := trigram.OfVariants(queryVariants)
	if len(trigrams) == 0 {
		return nil, nil
	}
	lists, err := e.store.LookupPostings(sysname, field, trigrams)
	if err != nil {
		return nil, err
	}

	counts := make(map[uint64]int)
	for _, list := range lists {
		if list == nil {
			continue
		}
		list.ForEach(func(id uint64) { counts[id]++ })
	}
	if len(counts) == 0 {
		return nil, nil
	}

	// Кандидат должен делить не менее ~трети триграмм запроса. Это держит
	// набор кандидатов компактным, допуская опечатки.
	minMatch := max(1, len(trigrams)/3)

	type cand struct {
		id    uint64
		count int
	}
	cands := make([]cand, 0, len(counts))
	for id, c := range counts {
		if c >= minMatch {
			cands = append(cands, cand{id, c})
		}
	}
	// Сначала наибольшее пересечение триграмм, затем ограничиваем набор.
	sort.Slice(cands, func(i, j int) bool { return cands[i].count > cands[j].count })
	if len(cands) > e.opts.MaxCandidates {
		cands = cands[:e.opts.MaxCandidates]
	}

	ids := make([]uint64, len(cands))
	for i, c := range cands {
		ids[i] = c.id
	}
	return ids, nil
}

// rank оценивает кандидатов по расстоянию Левенштейна и возвращает лучшие результаты.
func (e *Engine) rank(sysname, field string, queryVariants []string, ids []uint64, limit int) ([]Result, error) {
	if len(ids) == 0 {
		return []Result{}, nil
	}
	raws, err := e.store.GetRecords(sysname, ids)
	if err != nil {
		return nil, err
	}

	records := make([]map[string]any, 0, len(ids))
	keptIDs := make([]uint64, 0, len(ids))
	candTargets := make([][]string, 0, len(ids))
	for _, id := range ids {
		raw, ok := raws[id]
		if !ok {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		text := valueToString(rec[field])
		if text == "" {
			continue
		}
		records = append(records, rec)
		keptIDs = append(keptIDs, id)
		candTargets = append(candTargets, comparisonTargets(text))
	}

	// Лучшее сходство по каждому кандидату по всем вариантам запроса. ScoreBatch
	// автоматически работает параллельно, когда кандидатов больше 50.
	scores := make([]float64, len(candTargets))
	for _, qv := range queryVariants {
		batch := levenshtein.ScoreBatch(qv, candTargets)
		for i, s := range batch {
			if s > scores[i] {
				scores[i] = s
			}
		}
	}

	results := make([]Result, 0, len(keptIDs))
	for i, id := range keptIDs {
		if scores[i] < e.opts.MinScore {
			continue
		}
		results = append(results, Result{
			ID:     id,
			Score:  scores[i],
			Record: records[i],
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
