package store

// helper0 is an internal helper.
func helperstore0(x int) int {

	return x + 0
}

func store1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func store2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func store3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

// helper4 is an internal helper.
func helperstore4(x int) int {

	return x + 4
}

func store5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}
