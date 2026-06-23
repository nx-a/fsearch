// Пакет store хранит списки, записи и инвертированный триграммный индекс
// в одном файле bbolt.
//
// Раскладка бакетов:
//
//	__lists__            sysname            -> метаданные списка в JSON
//	r:<sysname>          big-endian id      -> сырой JSON записи
//	i:<sysname>:<field>  байты триграммы -> сериализованный список вхождений
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/nx-a/fsearch/internal/postings"
)

var (
	// ErrListExists возвращается при создании списка с уже занятым sysname.
	ErrListExists = errors.New("store: list already exists")
	// ErrListNotFound возвращается, когда указанный список не существует.
	ErrListNotFound = errors.New("store: list not found")
	// ErrRecordNotFound возвращается, когда запись не найдена.
	ErrRecordNotFound = errors.New("store: record not found")
)

var metaBucket = []byte("__lists__")

// ListMeta описывает хранимый список.
type ListMeta struct {
	Sysname      string   `json:"sysname"`
	SearchFields []string `json:"search_fields"`
}

// Store — обёртка над базой bbolt.
type Store struct {
	db       *bolt.DB
	readOnly bool
}

// Open открывает (или создаёт) базу по пути path в режиме чтения-записи.
func Open(path string) (*Store, error) {
	return open(path, false)
}

// OpenReadOnly открывает существующую базу только на чтение. В этом режиме
// файл блокируется разделяемой блокировкой, что позволяет нескольким процессам
// (подам-читателям) открывать собственные копии снапшота. Бакеты не создаются —
// предполагается, что снапшот уже содержит корректную раскладку.
func OpenReadOnly(path string) (*Store, error) {
	return open(path, true)
}

func open(path string, readOnly bool) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{
		ReadOnly: readOnly,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if !readOnly {
		if err := db.Update(func(tx *bolt.Tx) error {
			_, e := tx.CreateBucketIfNotExists(metaBucket)
			return e
		}); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db, readOnly: readOnly}, nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

// Snapshot пишет консистентную копию всей базы в w. Используется
// подом-писателем для отдачи снапшота подам-читателям. Копия снимается внутри
// View-транзакции, поэтому безопасна при параллельных записях.
func (s *Store) Snapshot(w io.Writer) (int64, error) {
	var n int64
	err := s.db.View(func(tx *bolt.Tx) error {
		var e error
		n, e = tx.WriteTo(w)
		return e
	})
	return n, err
}

func recBucketName(sysname string) []byte { return []byte("r:" + sysname) }
func idxBucketName(sysname, field string) []byte {
	return []byte("i:" + sysname + ":" + field)
}

func encodeID(id uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, id)
	return b
}

// DecodeID преобразует 8-байтовый ключ обратно в числовой id.
func DecodeID(key []byte) uint64 { return binary.BigEndian.Uint64(key) }

// CreateList регистрирует новый список и создаёт бакет для его записей.
func (s *Store) CreateList(meta ListMeta) error {
	if meta.Sysname == "" {
		return errors.New("store: empty sysname")
	}
	if strings.Contains(meta.Sysname, ":") {
		return errors.New("store: sysname must not contain ':'")
	}
	for _, f := range meta.SearchFields {
		if err := validateField(f); err != nil {
			return err
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(metaBucket)
		if mb.Get([]byte(meta.Sysname)) != nil {
			return ErrListExists
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if err := mb.Put([]byte(meta.Sysname), raw); err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists(recBucketName(meta.Sysname))
		return err
	})
}

// GetList возвращает метаданные списка sysname.
func (s *Store) GetList(sysname string) (ListMeta, error) {
	var meta ListMeta
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(metaBucket).Get([]byte(sysname))
		if raw == nil {
			return ErrListNotFound
		}
		return json.Unmarshal(raw, &meta)
	})
	return meta, err
}

// ListNames возвращает sysname всех хранимых списков.
func (s *Store) ListNames() ([]string, error) {
	var names []string
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(metaBucket).ForEach(func(k, _ []byte) error {
			names = append(names, string(k))
			return nil
		})
	})
	return names, err
}

// AddRecord сохраняет сырой JSON записи под вновь выделенным id и обновляет
// триграммный индекс. fieldTrigrams сопоставляет имя поля триграммам для
// индексации значения этого поля. Вся операция выполняется в одной транзакции.
func (s *Store) AddRecord(sysname string, raw []byte, fieldTrigrams map[string][]string) (uint64, error) {
	var id uint64
	err := s.db.Update(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		seq, err := rb.NextSequence()
		if err != nil {
			return err
		}
		id = seq
		if err := rb.Put(encodeID(id), raw); err != nil {
			return err
		}
		return updateIndex(tx, sysname, fieldTrigrams, id, true)
	})
	return id, err
}

// UpdateRecord заменяет содержимое существующей записи, сохраняя её id.
// Из индекса удаляются триграммы removeTrigrams и добавляются addTrigrams.
func (s *Store) UpdateRecord(sysname string, id uint64, raw []byte, removeTrigrams, addTrigrams map[string][]string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		if rb.Get(encodeID(id)) == nil {
			return ErrRecordNotFound
		}
		if err := rb.Put(encodeID(id), raw); err != nil {
			return err
		}
		if err := updateIndex(tx, sysname, removeTrigrams, id, false); err != nil {
			return err
		}
		return updateIndex(tx, sysname, addTrigrams, id, true)
	})
}

// DeleteRecord удаляет запись и исключает её id из индекса по
// переданным removeTrigrams.
func (s *Store) DeleteRecord(sysname string, id uint64, removeTrigrams map[string][]string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		if rb.Get(encodeID(id)) == nil {
			return ErrRecordNotFound
		}
		if err := rb.Delete(encodeID(id)); err != nil {
			return err
		}
		return updateIndex(tx, sysname, removeTrigrams, id, false)
	})
}

// DeleteList удаляет список целиком: метаданные, бакет записей и все
// бакеты индекса.
func (s *Store) DeleteList(sysname string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(metaBucket)
		if mb.Get([]byte(sysname)) == nil {
			return ErrListNotFound
		}
		if err := mb.Delete([]byte(sysname)); err != nil {
			return err
		}
		if err := tx.DeleteBucket(recBucketName(sysname)); err != nil && err != bolt.ErrBucketNotFound {
			return err
		}
		// Собираем имена бакетов индекса, затем удаляем их.
		prefix := []byte("i:" + sysname + ":")
		var idxNames [][]byte
		if err := tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if bytesHasPrefix(name, prefix) {
				idxNames = append(idxNames, append([]byte(nil), name...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, name := range idxNames {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		return nil
	})
}

// updateIndex добавляет (add=true) или удаляет (add=false) id из списков
// вхождений для переданных триграмм. Вызывается внутри транзакции.
func updateIndex(tx *bolt.Tx, sysname string, fieldTrigrams map[string][]string, id uint64, add bool) error {
	for field, trigrams := range fieldTrigrams {
		if len(trigrams) == 0 {
			continue
		}
		var ib *bolt.Bucket
		var err error
		if add {
			ib, err = tx.CreateBucketIfNotExists(idxBucketName(sysname, field))
			if err != nil {
				return err
			}
		} else {
			ib = tx.Bucket(idxBucketName(sysname, field))
			if ib == nil {
				continue
			}
		}
		for _, g := range trigrams {
			key := []byte(g)
			cur := ib.Get(key)
			var list *postings.List
			if cur != nil {
				list, err = postings.Unmarshal(cur)
				if err != nil {
					return fmt.Errorf("decode posting %q: %w", g, err)
				}
			} else if add {
				list = postings.New()
			} else {
				continue
			}
			if add {
				list.Add(id)
			} else {
				list.Remove(id)
			}
			if !add && list.Count() == 0 {
				if err := ib.Delete(key); err != nil {
					return err
				}
				continue
			}
			if err := ib.Put(key, list.Marshal()); err != nil {
				return err
			}
		}
	}
	return nil
}

func bytesHasPrefix(b, prefix []byte) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == string(prefix)
}

// GetRecord возвращает сырой JSON для id или nil, если записи нет.
func (s *Store) GetRecord(sysname string, id uint64) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		v := rb.Get(encodeID(id))
		if v != nil {
			out = append([]byte(nil), v...)
		}
		return nil
	})
	return out, err
}

// GetRecords возвращает сырой JSON для переданных id в одной транзакции.
// Отсутствующие id пропускаются.
func (s *Store) GetRecords(sysname string, ids []uint64) (map[uint64][]byte, error) {
	out := make(map[uint64][]byte, len(ids))
	err := s.db.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		for _, id := range ids {
			if v := rb.Get(encodeID(id)); v != nil {
				out[id] = append([]byte(nil), v...)
			}
		}
		return nil
	})
	return out, err
}

// LookupPostings загружает список вхождений для каждой триграммы. Результирующий
// срез соответствует trigrams; элемент равен nil, когда триграммы нет.
func (s *Store) LookupPostings(sysname, field string, trigrams []string) ([]*postings.List, error) {
	out := make([]*postings.List, len(trigrams))
	err := s.db.View(func(tx *bolt.Tx) error {
		ib := tx.Bucket(idxBucketName(sysname, field))
		if ib == nil {
			return nil // поле ещё ни разу не индексировалось
		}
		for i, g := range trigrams {
			if v := ib.Get([]byte(g)); v != nil {
				list, err := postings.Unmarshal(v)
				if err != nil {
					return err
				}
				out[i] = list
			}
		}
		return nil
	})
	return out, err
}

// RecordCount возвращает количество записей в списке.
func (s *Store) RecordCount(sysname string) (int, error) {
	var n int
	err := s.db.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(recBucketName(sysname))
		if rb == nil {
			return ErrListNotFound
		}
		n = rb.Stats().KeyN
		return nil
	})
	return n, err
}

// validateField защищает от случайных разделителей имён бакетов в полях.
func validateField(field string) error {
	if strings.Contains(field, ":") {
		return fmt.Errorf("store: field %q must not contain ':'", field)
	}
	return nil
}
