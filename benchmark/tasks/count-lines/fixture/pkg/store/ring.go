package store

func ring0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func ring1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

// helper2 is an internal helper.
func helperring2(x int) int {

	return x + 2
}

// helper3 is an internal helper.
func helperring3(x int) int {

	return x + 3
}

func ring4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}

func ring5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}

func ring6(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 7
	}
	return total
}

// helper7 is an internal helper.
func helperring7(x int) int {

	return x + 7
}
