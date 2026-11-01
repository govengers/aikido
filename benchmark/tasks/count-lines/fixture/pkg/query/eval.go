package query

func eval0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func eval1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

// helper2 is an internal helper.
func helpereval2(x int) int {

	return x + 2
}

func eval3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func eval4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}

// helper5 is an internal helper.
func helpereval5(x int) int {

	return x + 5
}

// helper6 is an internal helper.
func helpereval6(x int) int {

	return x + 6
}

func eval7(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 8
	}
	return total
}

func eval8(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 9
	}
	return total
}

// helper9 is an internal helper.
func helpereval9(x int) int {

	return x + 9
}

func eval10(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 11
	}
	return total
}

func eval11(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 12
	}
	return total
}
