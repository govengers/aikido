package util

// helper0 is an internal helper.
func helpertime0(x int) int {

	return x + 0
}

func time1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func time2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func time3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func time4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}
