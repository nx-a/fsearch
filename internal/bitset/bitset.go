// Пакет bitset реализует компактное расширяемое множество беззнаковых целых,
// хранящееся в срезе 64-битных слов. Используется для хранения плотных списков
// вхождений (очень частых триграмм) гораздо дешевле, чем []uint64 / []int.
package bitset

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// BitSet — расширяемый битсет. Нулевое значение готово к использованию.
type BitSet struct {
	words []uint64
}

// New возвращает пустой BitSet, способный хранить id до hint без реаллокации.
func New(hint uint64) *BitSet {
	b := &BitSet{}
	if hint > 0 {
		b.words = make([]uint64, wordsFor(hint))
	}
	return b
}

func wordsFor(id uint64) int {
	return int(id/64) + 1
}

func (b *BitSet) grow(words int) {
	if words <= len(b.words) {
		return
	}
	nw := make([]uint64, words)
	copy(nw, b.words)
	b.words = nw
}

// Set добавляет id в множество.
func (b *BitSet) Set(id uint64) {
	w := int(id / 64)
	b.grow(w + 1)
	b.words[w] |= 1 << (id % 64)
}

// Clear удаляет id из множества.
func (b *BitSet) Clear(id uint64) {
	w := int(id / 64)
	if w >= len(b.words) {
		return
	}
	b.words[w] &^= 1 << (id % 64)
}

// Test сообщает, присутствует ли id.
func (b *BitSet) Test(id uint64) bool {
	w := int(id / 64)
	if w >= len(b.words) {
		return false
	}
	return b.words[w]&(1<<(id%64)) != 0
}

// Count возвращает количество id в множестве.
func (b *BitSet) Count() int {
	n := 0
	for _, w := range b.words {
		n += bits.OnesCount64(w)
	}
	return n
}

// ForEach вызывает fn для каждого id в множестве по возрастанию.
func (b *BitSet) ForEach(fn func(id uint64)) {
	for wi, w := range b.words {
		base := uint64(wi) * 64
		for w != 0 {
			t := bits.TrailingZeros64(w)
			fn(base + uint64(t))
			w &= w - 1
		}
	}
}

// Marshal сериализует множество как срез little-endian слов с префиксом длины.
func (b *BitSet) Marshal() []byte {
	// Отсекаем хвостовые нулевые слова, чтобы представление было компактным.
	n := len(b.words)
	for n > 0 && b.words[n-1] == 0 {
		n--
	}
	out := make([]byte, binary.MaxVarintLen64+n*8)
	off := binary.PutUvarint(out, uint64(n))
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint64(out[off:], b.words[i])
		off += 8
	}
	return out[:off]
}

// Unmarshal декодирует BitSet, созданный Marshal.
func Unmarshal(data []byte) (*BitSet, error) {
	n, off := binary.Uvarint(data)
	if off <= 0 {
		return nil, errors.New("bitset: bad length prefix")
	}
	if len(data[off:]) < int(n)*8 {
		return nil, errors.New("bitset: truncated payload")
	}
	b := &BitSet{words: make([]uint64, n)}
	for i := 0; i < int(n); i++ {
		b.words[i] = binary.LittleEndian.Uint64(data[off:])
		off += 8
	}
	return b, nil
}
