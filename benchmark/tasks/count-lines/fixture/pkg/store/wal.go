package store

func wal0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func wal1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}
