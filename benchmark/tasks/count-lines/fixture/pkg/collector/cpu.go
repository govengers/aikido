package collector

func cpu0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func cpu1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}
