package util

func pool0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func pool1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

// helper2 is an internal helper.
func helperpool2(x int) int {

	return x + 2
}

func pool3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}
