package domain

import (
	"errors"
	"math/bits"
	"sort"
)

// apportion делит целый объём total (мл) между частями пропорционально целым весам методом наибольших
// остатков: каждая часть получает целую часть своей квоты, оставшиеся мл — части с наибольшим остатком,
// при равных остатках — по порядку. Сумма частей всегда точно равна total, поэтому FR019 «сумма по
// клиникам равна сумме по районам» выполняется без погрешности. Веса нормируются на свою сумму:
// доли с суммой 1,0001 не добавляют мл. Арифметика 128-битная, без float.
func apportion(total int64, weights []int64) ([]int64, error) {
	if total < 0 {
		return nil, errors.New("делимый объём отрицательный")
	}
	if len(weights) == 0 {
		return nil, errors.New("нет частей для деления объёма")
	}
	var sum uint64
	for _, w := range weights {
		if w < 0 {
			return nil, errors.New("отрицательный вес части")
		}
		var carry uint64
		sum, carry = bits.Add64(sum, uint64(w), 0)
		if carry != 0 {
			return nil, errors.New("сумма весов слишком велика")
		}
	}
	if sum == 0 {
		return nil, errors.New("сумма весов равна нулю")
	}
	out := make([]int64, len(weights))
	rem := make([]uint64, len(weights))
	var assigned int64
	for i, w := range weights {
		// total·w/sum < 2^63·sum, значит старшее слово меньше делителя и Div64 не паникует.
		hi, lo := bits.Mul64(uint64(total), uint64(w)) //nolint:gosec // total и w неотрицательны, проверено выше
		q, r := bits.Div64(hi, lo, sum)
		part := int64(q) //nolint:gosec // q ≤ total < 2^63
		out[i] = part
		rem[i] = r
		assigned += part
	}
	left := total - assigned
	if left == 0 {
		return out, nil
	}
	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rem[order[a]] > rem[order[b]] })
	// Σ остатков = left·sum и каждый остаток < sum, поэтому left меньше числа частей с ненулевым остатком.
	for i := int64(0); i < left; i++ {
		out[order[i]]++
	}
	return out, nil
}

// mulDivRoundHalfUp — a·b/c с округлением до целого, половина — вверх; a, b ≥ 0, c > 0.
func mulDivRoundHalfUp(a, b, c int64) (int64, error) {
	if a < 0 || b < 0 || c <= 0 {
		return 0, errors.New("некорректные аргументы деления")
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	var carry uint64
	lo, carry = bits.Add64(lo, uint64(c)/2, 0)
	hi += carry
	if hi >= uint64(c) {
		return 0, errors.New("переполнение при расчёте объёма")
	}
	q, _ := bits.Div64(hi, lo, uint64(c))
	if q > 1<<63-1 {
		return 0, errors.New("переполнение при расчёте объёма")
	}
	return int64(q), nil
}
