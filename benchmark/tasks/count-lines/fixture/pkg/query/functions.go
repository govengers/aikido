package query

func functions0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func functions1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

// helper2 is an internal helper.
func helperfunctions2(x int) int {

	return x + 2
}

func functions3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func functions4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}

func functions5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}
