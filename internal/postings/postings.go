// Пакет postings хранит множество id записей, связанных с триграммой.
// Редкие триграммы используют компактный разреженный список с дельта-кодированием;
// очень частые триграммы продвигаются в битсет, который при большом списке
// одновременно компактнее и быстрее в обходе.
package postings

import (
	"encoding/binary"
	"errors"
	"sort"

	"github.com/nx-a/fsearch/internal/bitset"
)

// DenseThreshold — размер списка вхождений, при превышении которого представление
// переключается с разреженного списка id на битсет.
const DenseThreshold = 1024

const (
	fmtSparse byte = 0
	fmtDense  byte = 1
)

// List — множество id записей для одной триграммы.
type List struct {
	dense bool
	ids   []uint64 // разреженный: отсортирован, без дубликатов
	bs    *bitset.BitSet
}

// New возвращает пустой список вхождений.
func New() *List { return &List{} }

// Dense сообщает, хранится ли список как битсет.
func (l *List) Dense() bool { return l.dense }

// Count возвращает количество id в списке.
func (l *List) Count() int {
	if l.dense {
		return l.bs.Count()
	}
	return len(l.ids)
}

// Add вставляет id, продвигая список в битсет при превышении DenseThreshold.
func (l *List) Add(id uint64) {
	if l.dense {
		l.bs.Set(id)
		return
	}
	i := sort.Search(len(l.ids), func(i int) bool { return l.ids[i] >= id })
	if i < len(l.ids) && l.ids[i] == id {
		return // уже присутствует
	}
	l.ids = append(l.ids, 0)
	copy(l.ids[i+1:], l.ids[i:])
	l.ids[i] = id
	if len(l.ids) > DenseThreshold {
		l.promote()
	}
}

func (l *List) promote() {
	bs := bitset.New(l.ids[len(l.ids)-1])
	for _, id := range l.ids {
		bs.Set(id)
	}
	l.dense = true
	l.bs = bs
	l.ids = nil
}

// Remove удаляет id из списка, если он там есть.
func (l *List) Remove(id uint64) {
	if l.dense {
		l.bs.Clear(id)
		return
	}
	i := sort.Search(len(l.ids), func(i int) bool { return l.ids[i] >= id })
	if i >= len(l.ids) || l.ids[i] != id {
		return
	}
	l.ids = append(l.ids[:i], l.ids[i+1:]...)
}

// Contains сообщает, есть ли id в списке.
func (l *List) Contains(id uint64) bool {
	if l.dense {
		return l.bs.Test(id)
	}
	i := sort.Search(len(l.ids), func(i int) bool { return l.ids[i] >= id })
	return i < len(l.ids) && l.ids[i] == id
}

// ForEach вызывает fn для каждого id по возрастанию.
func (l *List) ForEach(fn func(id uint64)) {
	if l.dense {
		l.bs.ForEach(fn)
		return
	}
	for _, id := range l.ids {
		fn(id)
	}
}

// Marshal сериализует список с однобайтовым тегом формата.
func (l *List) Marshal() []byte {
	if l.dense {
		payload := l.bs.Marshal()
		out := make([]byte, 1+len(payload))
		out[0] = fmtDense
		copy(out[1:], payload)
		return out
	}
	out := make([]byte, 1+binary.MaxVarintLen64*(len(l.ids)+1))
	out[0] = fmtSparse
	off := 1
	off += binary.PutUvarint(out[off:], uint64(len(l.ids)))
	var prev uint64
	for _, id := range l.ids {
		off += binary.PutUvarint(out[off:], id-prev)
		prev = id
	}
	return out[:off]
}

// Unmarshal декодирует список вхождений, созданный Marshal.
func Unmarshal(data []byte) (*List, error) {
	if len(data) == 0 {
		return nil, errors.New("postings: empty data")
	}
	switch data[0] {
	case fmtDense:
		bs, err := bitset.Unmarshal(data[1:])
		if err != nil {
			return nil, err
		}
		return &List{dense: true, bs: bs}, nil
	case fmtSparse:
		off := 1
		n, k := binary.Uvarint(data[off:])
		if k <= 0 {
			return nil, errors.New("postings: bad count")
		}
		off += k
		ids := make([]uint64, 0, n)
		var prev uint64
		for i := uint64(0); i < n; i++ {
			d, k := binary.Uvarint(data[off:])
			if k <= 0 {
				return nil, errors.New("postings: truncated ids")
			}
			off += k
			prev += d
			ids = append(ids, prev)
		}
		return &List{ids: ids}, nil
	default:
		return nil, errors.New("postings: unknown format tag")
	}
}
