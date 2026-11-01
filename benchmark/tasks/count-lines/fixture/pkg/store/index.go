package store

// helper0 is an internal helper.
func helperindex0(x int) int {

	return x + 0
}

func index1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func index2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func index3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func index4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}
